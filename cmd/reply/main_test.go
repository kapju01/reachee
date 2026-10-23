package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/smithy-go"

	"github.com/aisolutions/reachee/internal/audit"
	"github.com/aisolutions/reachee/internal/ddb"
	"github.com/aisolutions/reachee/internal/ddb/ddbtest"
	"github.com/aisolutions/reachee/internal/replytoken"
)

type fakeSFN struct {
	calls []*sfn.SendTaskSuccessInput
	err   error
}

func (f *fakeSFN) SendTaskSuccess(_ context.Context, in *sfn.SendTaskSuccessInput, _ ...func(*sfn.Options)) (*sfn.SendTaskSuccessOutput, error) {
	f.calls = append(f.calls, in)
	return &sfn.SendTaskSuccessOutput{}, f.err
}

type fixture struct {
	h     *replier
	tbl   *ddbtest.Table
	sfn   *fakeSFN
	pub   ed25519.PublicKey
	token string
	now   time.Time
}

const escalation = "esc-1"

// newFixture is the state the email leaves behind: a request record, a
// minted slot, and the link the person holds. The execution is not yet armed.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	tbl := ddbtest.NewTable()
	if err := ddb.PutRequest(ctx, tbl, "t", ddb.Request{
		EscalationID: escalation, AgentID: "scout-meds", RecipientID: "on-call", Priority: "P1",
		Subject: "Hold dose?", Payload: "Bed 4 potassium 6.1 <b>high</b>", RequestedResponse: "hold or give",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ddb.MintReply(ctx, tbl, "t", escalation, "nonce-1", now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	key := replytoken.KeyFrom(priv)
	token, err := replytoken.Mint(key, replytoken.Claims{EscalationID: escalation, RecipientID: "on-call", Expiry: now.Add(15 * time.Minute).Unix(), Nonce: "nonce-1"})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSFN{}
	fx := &fixture{tbl: tbl, sfn: f, pub: pub, token: token, now: now}
	fx.h = &replier{db: tbl, sfn: f, table: "t", key: priv, replyKey: key, now: func() time.Time { return fx.now }}
	return fx
}

func (f *fixture) invoke(t *testing.T, v any) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(v)
	return f.h.handle(context.Background(), raw)
}

func (f *fixture) state(t *testing.T, action string, extra map[string]any) error {
	t.Helper()
	in := map[string]any{"action": action, "escalation_id": escalation}
	for k, v := range extra {
		in[k] = v
	}
	_, err := f.invoke(t, in)
	return err
}

func (f *fixture) arm(t *testing.T) {
	t.Helper()
	if err := f.state(t, "arm", map[string]any{"task_token": "sfn-task-token"}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) get(t *testing.T, token string) events.APIGatewayV2HTTPResponse {
	t.Helper()
	out, err := f.invoke(t, events.APIGatewayV2HTTPRequest{RouteKey: "GET /reply", QueryStringParameters: map[string]string{"t": token}})
	if err != nil {
		t.Fatal(err)
	}
	return out.(events.APIGatewayV2HTTPResponse)
}

func (f *fixture) post(t *testing.T, decision, text string) events.APIGatewayV2HTTPResponse {
	t.Helper()
	form := url.Values{"t": {f.token}, "decision": {decision}, "text": {text}}
	out, err := f.invoke(t, events.APIGatewayV2HTTPRequest{RouteKey: "POST /reply", Body: form.Encode()})
	if err != nil {
		t.Fatal(err)
	}
	return out.(events.APIGatewayV2HTTPResponse)
}

func (f *fixture) chain(t *testing.T) []audit.Event {
	t.Helper()
	sink, err := ddb.NewSink(context.Background(), f.tbl, "t", escalation)
	if err != nil {
		t.Fatal(err)
	}
	return sink.All()
}

func (f *fixture) slot(t *testing.T) ddb.Reply {
	t.Helper()
	r, err := ddb.GetReply(context.Background(), f.tbl, "t", escalation)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func types(events []audit.Event) string {
	var s []string
	for _, e := range events {
		s = append(s, e.EventType)
	}
	return strings.Join(s, ",")
}

// Path 1: arm stores the task token and returns nothing.
func TestArmStoresTheTaskToken(t *testing.T) {
	f := newFixture(t)
	out, err := f.invoke(t, map[string]any{"action": "arm", "escalation_id": escalation, "task_token": "sfn-task-token"})
	if err != nil || out != nil {
		t.Fatalf("arm returned %v, %v", out, err)
	}
	if got := f.slot(t).TaskToken; got != "sfn-task-token" {
		t.Fatalf("task token = %q", got)
	}
	if len(f.chain(t)) != 0 || len(f.sfn.calls) != 0 {
		t.Fatal("arm must write no receipt and call nothing")
	}
}

// Path 2: GET renders the decision and consumes nothing, because a mail
// scanner fetching the link must not be able to answer for the person.
func TestGetRendersTheDecisionAndConsumesNothing(t *testing.T) {
	f := newFixture(t)
	f.arm(t)
	before := f.slot(t)

	for range 3 {
		res := f.get(t, f.token)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status %d", res.StatusCode)
		}
		for _, want := range []string{"Hold dose?", "Bed 4 potassium 6.1", "hold or give", `name="text"`, `value="direction"`, `value="denial"`, `method="post"`} {
			if !strings.Contains(res.Body, want) {
				t.Fatalf("page is missing %q", want)
			}
		}
		if strings.Contains(res.Body, "<b>high</b>") {
			t.Fatal("payload must be escaped, not rendered as markup")
		}
		if res.Headers["cache-control"] != "no-store" || res.Headers["referrer-policy"] != "no-referrer" {
			t.Fatalf("headers = %v", res.Headers)
		}
	}
	after := f.slot(t)
	if !after.SpentAt.IsZero() || after.Nonce != before.Nonce {
		t.Fatal("GET changed the reply slot")
	}
	if len(f.chain(t)) != 0 || len(f.sfn.calls) != 0 {
		t.Fatal("GET wrote a receipt or released something")
	}
}

func TestGetRefusesBadLinks(t *testing.T) {
	f := newFixture(t)
	payload, _, _ := strings.Cut(f.token, ".")
	if res := f.get(t, payload+".forged"); res.StatusCode != http.StatusForbidden {
		t.Fatalf("forged: status %d", res.StatusCode)
	}
	f.now = f.now.Add(16 * time.Minute)
	if res := f.get(t, f.token); res.StatusCode != http.StatusGone {
		t.Fatalf("expired: status %d", res.StatusCode)
	}
}

// Path 3a: a direction is recorded, handed to the execution, and the
// Release state's invocation writes RESOLVED.
func TestPostDirectionReleases(t *testing.T) {
	f := newFixture(t)
	f.arm(t)

	res := f.post(t, "direction", "Hold tonight's dose, recheck at 6")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, "passed to the agent") {
		t.Fatalf("status %d: %s", res.StatusCode, res.Body)
	}
	if got := types(f.chain(t)); got != "RECEIVED,ACKNOWLEDGED" {
		t.Fatalf("chain = %s", got)
	}
	ack := f.chain(t)[1]
	if ack.Metadata["intent"] != "direction" || ack.Metadata["confidence"] == "" {
		t.Fatalf("ACKNOWLEDGED must carry intent and confidence, got %v", ack.Metadata)
	}

	if len(f.sfn.calls) != 1 || aws.ToString(f.sfn.calls[0].TaskToken) != "sfn-task-token" {
		t.Fatalf("SendTaskSuccess calls = %d", len(f.sfn.calls))
	}
	output := aws.ToString(f.sfn.calls[0].Output)
	var a answer
	if err := json.Unmarshal([]byte(output), &a); err != nil || a.Decision != "direction" {
		t.Fatalf("output = %s", output)
	}
	if strings.Contains(output, "recheck") {
		t.Fatal("the person's words reached the execution history")
	}

	if err := f.state(t, "release", map[string]any{"answer": a}); err != nil {
		t.Fatal(err)
	}
	events := f.chain(t)
	if got := types(events); got != "RECEIVED,ACKNOWLEDGED,RESOLVED" {
		t.Fatalf("chain = %s", got)
	}
	if events[2].Metadata["released"] != "true" {
		t.Fatal("RESOLVED must say it released")
	}
	if err := audit.Verify(events, f.pub); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(events)
	if strings.Contains(string(raw), "recheck") || strings.Contains(string(raw), "potassium") {
		t.Fatal("message content reached the chain")
	}
}

// Path 3b: a denial is recorded and handed on, Denied writes DENIED, and
// release refuses even if something routed a denial to it.
func TestPostDenialReleasesNothing(t *testing.T) {
	f := newFixture(t)
	f.arm(t)

	res := f.post(t, "denial", "")
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Body, "will not go ahead") {
		t.Fatalf("status %d: %s", res.StatusCode, res.Body)
	}
	var a answer
	_ = json.Unmarshal([]byte(aws.ToString(f.sfn.calls[0].Output)), &a)
	if a.Decision != "denial" {
		t.Fatalf("decision = %q", a.Decision)
	}

	if err := f.state(t, "release", map[string]any{"answer": a}); err == nil {
		t.Fatal("release must refuse a denial")
	}
	forged := answer{Decision: "direction", Confidence: "button"}
	if err := f.state(t, "release", map[string]any{"answer": forged}); err == nil {
		t.Fatal("release must check the recorded decision, not only its input")
	}

	if err := f.state(t, "record_denial", map[string]any{"answer": a}); err != nil {
		t.Fatal(err)
	}
	events := f.chain(t)
	if got := types(events); got != "RECEIVED,ACKNOWLEDGED,DENIED" {
		t.Fatalf("chain = %s", got)
	}
	if events[2].Metadata["released"] != "false" {
		t.Fatal("DENIED must say nothing was released")
	}
	if err := audit.Verify(events, f.pub); err != nil {
		t.Fatal(err)
	}
}

// Path 4: an answer that arrives before the execution is holding is refused
// out loud, spends nothing, and the same link works once armed.
func TestPostBeforeArmRefusesAndKeepsTheLink(t *testing.T) {
	f := newFixture(t)

	res := f.post(t, "direction", "")
	if res.StatusCode != http.StatusConflict || !strings.Contains(res.Body, "try again in a moment") {
		t.Fatalf("status %d: %s", res.StatusCode, res.Body)
	}
	if !f.slot(t).SpentAt.IsZero() || len(f.chain(t)) != 0 || len(f.sfn.calls) != 0 {
		t.Fatal("a not-ready answer must record and release nothing")
	}

	f.arm(t)
	if res := f.post(t, "direction", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("after arm: status %d", res.StatusCode)
	}
}

func TestLinkRecordsOneAnswer(t *testing.T) {
	f := newFixture(t)
	f.arm(t)
	f.post(t, "denial", "")
	res := f.post(t, "direction", "")
	if res.StatusCode != http.StatusGone || len(f.sfn.calls) != 1 {
		t.Fatalf("replay: status %d, %d hand-offs", res.StatusCode, len(f.sfn.calls))
	}
	if got := types(f.chain(t)); got != "RECEIVED,ACKNOWLEDGED" {
		t.Fatalf("replay wrote receipts: %s", got)
	}
}

// A note that contradicts the button is handed back, not guessed at, and
// the link stays good.
func TestContradictoryAnswerIsHandedBack(t *testing.T) {
	f := newFixture(t)
	f.arm(t)
	for _, c := range []struct{ button, text string }{
		{"direction", "Denied, do not proceed with this"},
		{"denial", "approved, go ahead"},
		{"maybe", ""},
	} {
		res := f.post(t, c.button, c.text)
		if res.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(res.Body, "Nothing was recorded") {
			t.Fatalf("%q/%q: status %d", c.button, c.text, res.StatusCode)
		}
	}
	if !f.slot(t).SpentAt.IsZero() || len(f.sfn.calls) != 0 {
		t.Fatal("a contradictory answer must not spend the link")
	}
	// An instruction that happens to say "do not" is still a direction.
	if res := f.post(t, "direction", "Do not give the 9pm dose"); res.StatusCode != http.StatusOK {
		t.Fatalf("plain direction refused: status %d", res.StatusCode)
	}
}

// A timeout is not consent: TIMED_OUT releases nothing, and a click after it
// is refused instead of producing receipts for a hold that ended.
func TestTimeoutClosesTheWindow(t *testing.T) {
	f := newFixture(t)
	f.arm(t)
	if err := f.state(t, "record_timeout", nil); err != nil {
		t.Fatal(err)
	}
	res := f.post(t, "direction", "")
	if res.StatusCode != http.StatusGone || len(f.sfn.calls) != 0 {
		t.Fatalf("late answer: status %d", res.StatusCode)
	}
	events := f.chain(t)
	if got := types(events); got != "TIMED_OUT" || events[0].Metadata["released"] != "false" {
		t.Fatalf("chain = %s %v", got, events[0].Metadata)
	}
}

// If the execution will not take the answer, the chain says so and the
// person is told nothing was released.
func TestRejectedHandOffIsRecorded(t *testing.T) {
	f := newFixture(t)
	f.arm(t)
	f.sfn.err = &smithy.GenericAPIError{Code: "TaskTimedOut", Message: "Task Timed Out: 'arn:...'"}

	res := f.post(t, "direction", "")
	if res.StatusCode != http.StatusBadGateway || !strings.Contains(res.Body, "Nothing has been released") {
		t.Fatalf("status %d", res.StatusCode)
	}
	events := f.chain(t)
	if got := types(events); got != "RECEIVED,ACKNOWLEDGED,FAILED" {
		t.Fatalf("chain = %s", got)
	}
	if !strings.HasSuffix(events[2].Metadata["reason"], "TaskTimedOut") {
		t.Fatalf("reason = %q", events[2].Metadata["reason"])
	}
}

// The invocation shape decides the path. An HTTP caller cannot reach arm or
// release by putting an action in the body.
func TestInvocationShapeCannotBeSpoofed(t *testing.T) {
	f := newFixture(t)
	f.arm(t)
	out, err := f.invoke(t, events.APIGatewayV2HTTPRequest{RouteKey: "POST /reply", Body: `{"action":"release","escalation_id":"esc-1"}`})
	if err != nil {
		t.Fatal(err)
	}
	if res := out.(events.APIGatewayV2HTTPResponse); res.StatusCode == http.StatusOK {
		t.Fatal("a JSON body with an action was accepted as an answer")
	}
	if len(f.chain(t)) != 0 {
		t.Fatal("spoofed body wrote a receipt")
	}
	if err := f.state(t, "anything_else", nil); err == nil {
		t.Fatal("unknown action accepted")
	}
}
