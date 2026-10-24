package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"

	"github.com/aisolutions/reachee/internal/audit"
	"github.com/aisolutions/reachee/internal/ddb"
	"github.com/aisolutions/reachee/internal/ddb/ddbtest"
	"github.com/aisolutions/reachee/internal/heartmap"
)

type fakeSFN struct {
	in  *sfn.StartExecutionInput
	err error
}

func (f *fakeSFN) StartExecution(_ context.Context, in *sfn.StartExecutionInput, _ ...func(*sfn.Options)) (*sfn.StartExecutionOutput, error) {
	f.in = in
	return &sfn.StartExecutionOutput{}, f.err
}

const testPolicy = `contact:
  id: on-call
  display_name: On Call
  channels:
    whatsapp:
      number: "+10000000001"
    email:
      address: on-call@example.invalid
  failover_ladder:
    - tier: 1
      channel: whatsapp
      timeout_seconds: 300
    - tier: 2
      channel: email
      timeout_seconds: 900
`

func newRaiser(t *testing.T, startErr error) (*raiser, *ddbtest.Table, *fakeSFN, ed25519.PublicKey) {
	t.Helper()
	policy, err := heartmap.Load(testPolicy)
	if err != nil {
		t.Fatal(err)
	}
	store := heartmap.NewMemoryStore()
	store.Put(policy)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	tbl := ddbtest.NewTable()
	f := &fakeSFN{err: startErr}
	return &raiser{
		db: tbl, sfn: f, table: "t", stateMachine: "arn:sm",
		policies: store, key: priv,
		now: func() time.Time { return time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC) },
	}, tbl, f, pub
}

const payload = "Patient in bed 4: potassium 6.1, hold the scheduled dose?"

func validBody() string {
	b, _ := json.Marshal(map[string]string{
		"agent_id": "scout-meds", "recipient_id": "on-call", "correlation_id": "bed-4-k",
		"priority": "P1", "subject": "Hold dose?", "payload": payload,
		"requested_response": "hold or give",
	})
	return string(b)
}

func call(t *testing.T, h *raiser, body string) (int, raiseResponse) {
	t.Helper()
	res, err := h.handle(context.Background(), events.APIGatewayV2HTTPRequest{Body: body})
	if err != nil {
		t.Fatal(err)
	}
	var out raiseResponse
	if err := json.Unmarshal([]byte(res.Body), &out); err != nil {
		t.Fatalf("response is not JSON: %q", res.Body)
	}
	return res.StatusCode, out
}

func TestRaiseRecordsThePromiseAndStartsTheExecution(t *testing.T) {
	h, tbl, f, pub := newRaiser(t, nil)
	status, out := call(t, h, validBody())
	if status != http.StatusAccepted || out.EscalationID == "" {
		t.Fatalf("status %d, out %+v", status, out)
	}

	sink, err := ddb.NewSink(context.Background(), tbl, "t", out.EscalationID)
	if err != nil {
		t.Fatal(err)
	}
	events := sink.All()
	if len(events) != 1 || events[0].EventType != audit.TypePromised {
		t.Fatalf("want exactly one PROMISED receipt, got %d", len(events))
	}
	if err := audit.Verify(events, pub); err != nil {
		t.Fatal(err)
	}
	meta := events[0].Metadata
	if _, ok := meta["correlation_id"]; ok {
		t.Fatal("the caller's correlation id reached the chain in plaintext")
	}
	if got, want := meta["correlation_hash"], sha256hex("bed-4-k"); got != want {
		t.Fatalf("correlation_hash = %q, want the SHA-256 of what the caller sent, %q", got, want)
	}
	stored, err := ddb.GetRequest(context.Background(), tbl, "t", out.EscalationID)
	if err != nil || stored.CorrelationID != "bed-4-k" {
		t.Fatalf("the request record must keep the plaintext for coalescing, got %q (%v)", stored.CorrelationID, err)
	}
	hash, _ := audit.SignedHash(events[0])
	if out.ReceiptHash != hash || out.ChainHead != hash {
		t.Fatalf("receipt_hash and chain_head must be the PROMISED receipt's signed hash")
	}

	if aws.ToString(f.in.Name) != out.EscalationID || aws.ToString(f.in.StateMachineArn) != "arn:sm" {
		t.Fatalf("execution not named for the escalation: %+v", f.in)
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(aws.ToString(f.in.Input)), &input); err != nil {
		t.Fatal(err)
	}
	if len(input) != 2 || input["escalation_id"] != out.EscalationID || input["tier"] != float64(1) {
		t.Fatalf("execution input must be exactly {escalation_id, tier: 1}, got %v", input)
	}

	req, err := ddb.GetRequest(context.Background(), tbl, "t", out.EscalationID)
	if err != nil || req.Payload != payload {
		t.Fatalf("request record not stored for attempt to send: %v", err)
	}
}

// The chain and the execution input carry hashes and ids, never the payload.
func TestPayloadStaysOutOfTheChainAndTheExecution(t *testing.T) {
	h, tbl, f, _ := newRaiser(t, nil)
	_, out := call(t, h, validBody())

	sink, _ := ddb.NewSink(context.Background(), tbl, "t", out.EscalationID)
	raw, _ := json.Marshal(sink.All())
	if strings.Contains(string(raw), "potassium") {
		t.Fatal("payload text reached the chain")
	}
	if strings.Contains(aws.ToString(f.in.Input), "potassium") {
		t.Fatal("payload text reached the execution input")
	}
}

func TestRaiseRejectsBadRequestsWithoutWritingAnything(t *testing.T) {
	cases := map[string]struct {
		body string
		want int
	}{
		"not json":          {`{`, http.StatusBadRequest},
		"unknown field":     {strings.Replace(validBody(), `"agent_id"`, `"tier":3,"agent_id"`, 1), http.StatusBadRequest},
		"missing payload":   {strings.Replace(validBody(), payload, " ", 1), http.StatusBadRequest},
		"bad priority":      {strings.Replace(validBody(), `"P1"`, `"P9"`, 1), http.StatusBadRequest},
		"unknown recipient": {strings.Replace(validBody(), `"on-call"`, `"nobody"`, 1), http.StatusUnprocessableEntity},
		"too large":         {strings.Replace(validBody(), payload, strings.Repeat("x", maxBody), 1), http.StatusRequestEntityTooLarge},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, f, _ := newRaiser(t, nil)
			status, out := call(t, h, c.body)
			if status != c.want || out.Error == "" {
				t.Fatalf("status %d (want %d), out %+v", status, c.want, out)
			}
			if out.EscalationID != "" || f.in != nil {
				t.Fatal("a rejected request must not create an escalation or start an execution")
			}
		})
	}
}

// A promise that cannot be kept is reported as broken, in the chain and to
// the caller, rather than left as a PROMISED receipt nothing will act on.
func TestFailedStartIsRecordedAsABrokenPromise(t *testing.T) {
	h, tbl, _, pub := newRaiser(t, errors.New("ExecutionLimitExceeded"))
	status, out := call(t, h, validBody())
	if status != http.StatusBadGateway || out.Error == "" || out.EscalationID == "" {
		t.Fatalf("status %d, out %+v", status, out)
	}
	sink, _ := ddb.NewSink(context.Background(), tbl, "t", out.EscalationID)
	events := sink.All()
	if len(events) != 2 || events[1].EventType != audit.TypeFailed {
		t.Fatalf("want PROMISED then FAILED, got %d events", len(events))
	}
	if err := audit.Verify(events, pub); err != nil {
		t.Fatal(err)
	}
	head, _ := audit.SignedHash(events[1])
	if out.ChainHead != head {
		t.Fatal("chain_head must point at the FAILED receipt")
	}
}

// sha256hex is computed here, not through audit.CorrelationHash, so the test
// pins the rule a caller reproduces rather than the function that applies it.
func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
