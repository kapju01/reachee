package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/aisolutions/reachee/internal/audit"
	"github.com/aisolutions/reachee/internal/ddb"
	"github.com/aisolutions/reachee/internal/ddb/ddbtest"
)

const (
	id       = "01a16b5f-f8a7-747a-a48b-48300767bc42"
	payload  = "systolic 214 PAYLOAD-MUST-NOT-LEAK"
	subject  = "SUBJECT-MUST-NOT-LEAK"
	note     = "NOTE-MUST-NOT-LEAK reach out today"
	nonce    = "NONCE-MUST-NOT-LEAK"
	token    = "TASKTOKEN-MUST-NOT-LEAK"
	secretRe = "MUST-NOT-LEAK"
)

// recording wraps the fake so a test can see exactly what was asked of it.
type recording struct {
	*ddbtest.Table
	queries []*dynamodb.QueryInput
}

func (r *recording) Query(ctx context.Context, in *dynamodb.QueryInput, opt ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	r.queries = append(r.queries, in)
	return r.Table.Query(ctx, in, opt...)
}

type fixture struct {
	r    *reader
	db   *recording
	tbl  *ddbtest.Table
	pub  ed25519.PublicKey
	log  *audit.Logger
	sink *ddb.Sink
}

// newFixture stores everything a real escalation stores: the request record
// with its payload and subject, a reply slot holding the nonce, task token,
// and note, and a signed chain. Only the chain may come back out.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tbl := ddbtest.NewTable()
	now := time.Date(2026, 10, 24, 0, 46, 54, 0, time.UTC)
	if err := ddb.PutRequest(ctx, tbl, "t", ddb.Request{
		EscalationID: id, AgentID: "rpm-vitals-monitor-07", RecipientID: "on-call",
		Priority: "P1", Subject: subject, Payload: payload, RequestedResponse: "direct outreach",
	}); err != nil {
		t.Fatal(err)
	}
	if err := ddb.MintReply(ctx, tbl, "t", id, nonce, now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := ddb.ArmReply(ctx, tbl, "t", id, token, now); err != nil {
		t.Fatal(err)
	}
	sink, err := ddb.NewSink(ctx, tbl, "t", id)
	if err != nil {
		t.Fatal(err)
	}
	db := &recording{Table: tbl}
	return &fixture{
		r:  &reader{db: db, table: "t", pub: pub},
		db: db, tbl: tbl, pub: pub,
		log:  audit.NewLogger(priv, sink, func() time.Time { return now }),
		sink: sink,
	}
}

func (f *fixture) emit(t *testing.T, eventType, provider string, meta map[string]string) {
	t.Helper()
	if _, err := f.log.Emit(audit.Event{
		RequestID: id, RecipientID: "on-call", ProviderName: provider, EventType: eventType,
		MessageHash: audit.HashContent(payload), Metadata: meta,
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) answered(t *testing.T, decision, terminal, released string) {
	t.Helper()
	f.emit(t, audit.TypePromised, "", map[string]string{"promise": "attempt delivery"})
	f.emit(t, audit.TypeSent, "ses-v2", map[string]string{"provider_msg_id": "m-1"})
	if _, err := ddb.SpendReply(context.Background(), f.tbl, "t", id, nonce, decision, note, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.emit(t, audit.TypeReceived, "", map[string]string{"via": "reply link"})
	f.emit(t, audit.TypeAcknowledged, "", map[string]string{"intent": decision})
	f.emit(t, terminal, "", map[string]string{"intent": decision, "released": released})
}

func (f *fixture) get(t *testing.T, path string, query map[string]string) events.APIGatewayV2HTTPResponse {
	t.Helper()
	out, err := f.r.handle(context.Background(), events.APIGatewayV2HTTPRequest{
		RouteKey: "GET /escalation/{id}", PathParameters: map[string]string{"id": path}, QueryStringParameters: query,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func decode(t *testing.T, body string) summary {
	t.Helper()
	var s summary
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDirectionReportsReleased(t *testing.T) {
	f := newFixture(t)
	f.answered(t, "direction", audit.TypeResolved, "true")
	out := f.get(t, id, nil)
	if out.StatusCode != 200 {
		t.Fatalf("status %d: %s", out.StatusCode, out.Body)
	}
	s := decode(t, out.Body)
	if s.Decision != "direction" || s.Outcome != audit.TypeResolved || !s.Released {
		t.Fatalf("got decision=%q outcome=%q released=%v", s.Decision, s.Outcome, s.Released)
	}
	if len(s.Receipts) != 5 || s.Receipts[1].ProviderName != "ses-v2" {
		t.Fatalf("receipts = %+v", s.Receipts)
	}
	for i := 1; i < len(s.Receipts); i++ {
		if s.Receipts[i].PrevHash != s.Receipts[i-1].Hash {
			t.Fatalf("receipt %d does not link to the one before it", i)
		}
	}
	if s.ChainHead != s.Receipts[len(s.Receipts)-1].Hash {
		t.Fatal("chain head is not the last receipt's hash")
	}
}

// A denial completes the chain without a release. The summary must not read
// a terminal receipt as a release just because one exists.
func TestDenialIsNotReleased(t *testing.T) {
	f := newFixture(t)
	f.answered(t, "denial", audit.TypeDenied, "false")
	s := decode(t, f.get(t, id, nil).Body)
	if s.Decision != "denial" || s.Outcome != audit.TypeDenied || s.Released {
		t.Fatalf("got decision=%q outcome=%q released=%v", s.Decision, s.Outcome, s.Released)
	}
}

func TestWaitingHasNoOutcome(t *testing.T) {
	f := newFixture(t)
	f.emit(t, audit.TypePromised, "", nil)
	f.emit(t, audit.TypeSent, "ses-v2", nil)
	s := decode(t, f.get(t, id, nil).Body)
	if s.Decision != "" || s.Outcome != "" || s.Released {
		t.Fatalf("an open escalation reported decision=%q outcome=%q released=%v", s.Decision, s.Outcome, s.Released)
	}
}

// The bundle is exactly what the exporter produces for the same chain, so
// what verify/ checks in CI is what this endpoint serves.
func TestBundleIsTheExport(t *testing.T) {
	f := newFixture(t)
	f.answered(t, "direction", audit.TypeResolved, "true")
	out := f.get(t, id, map[string]string{"format": "bundle"})
	if out.StatusCode != 200 {
		t.Fatalf("status %d: %s", out.StatusCode, out.Body)
	}
	chain := f.sink.All()
	if err := audit.Verify(chain, f.pub); err != nil {
		t.Fatal(err)
	}
	want, err := audit.MarshalBundle(audit.Export(chain, f.pub))
	if err != nil {
		t.Fatal(err)
	}
	if out.Body != string(want) {
		t.Fatalf("bundle differs from the export of the same chain:\n%s", out.Body)
	}
}

// The payload, subject, note, nonce, and task token sit in the same
// partition. None may appear in either response, and the read itself must ask
// for the event attribute only, on the terms the role grants.
func TestNothingButTheChainLeavesTheTable(t *testing.T) {
	f := newFixture(t)
	f.answered(t, "direction", audit.TypeResolved, "true")
	for _, q := range []map[string]string{nil, {"format": "bundle"}} {
		if body := f.get(t, id, q).Body; strings.Contains(body, secretRe) {
			t.Fatalf("format %v leaked stored content: %s", q, body)
		}
	}
	for _, in := range f.db.queries {
		if in.Select != types.SelectSpecificAttributes || *in.ProjectionExpression != "#ev" || in.ExpressionAttributeNames["#ev"] != "event" {
			t.Fatalf("query asked for more than the event attribute: %+v", in)
		}
		if v := in.ExpressionAttributeValues[":first"].(*types.AttributeValueMemberN).Value; v != "1" {
			t.Fatalf("query starts at seq %s, which reaches the request record or the reply slot", v)
		}
	}
}

func TestRefusals(t *testing.T) {
	f := newFixture(t)
	f.emit(t, audit.TypePromised, "", nil)
	for name, c := range map[string]struct {
		id    string
		query map[string]string
		want  int
	}{
		"not a uuid":         {"../reply", nil, 400},
		"not a v7":           {"01a16b5f-f8a7-447a-a48b-48300767bc42", nil, 400},
		"unknown format":     {id, map[string]string{"format": "csv"}, 400},
		"no such escalation": {"01a16b5f-f8a7-747a-a48b-000000000000", nil, 404},
	} {
		t.Run(name, func(t *testing.T) {
			if got := f.get(t, c.id, c.query).StatusCode; got != c.want {
				t.Fatalf("status %d, want %d", got, c.want)
			}
		})
	}
}

func TestParsePublicKey(t *testing.T) {
	if _, err := parsePublicKey("85c96a3b16a44e63e39da3ce3078119356ba0b5a9d60929c811e7c1d3b90b23e\n"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "zz", "85c96a3b"} {
		if _, err := parsePublicKey(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
