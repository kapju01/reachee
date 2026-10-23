package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aisolutions/reachee/internal/audit"
	"github.com/aisolutions/reachee/internal/circuit"
	"github.com/aisolutions/reachee/internal/ddb"
	"github.com/aisolutions/reachee/internal/ddb/ddbtest"
	"github.com/aisolutions/reachee/internal/dignity"
	"github.com/aisolutions/reachee/internal/empathy"
	"github.com/aisolutions/reachee/internal/heartmap"
	"github.com/aisolutions/reachee/internal/providers"
	"github.com/aisolutions/reachee/internal/providers/mock"
	"github.com/aisolutions/reachee/internal/replytoken"
)

const testPolicy = `contact:
  id: on-call
  display_name: On Call
  channels:
    whatsapp:
      number: "+10000000001"
    email:
      address: on-call@example.invalid
    sms:
      number: "+10000000002"
  failover_ladder:
    - tier: 1
      channel: whatsapp
      timeout_seconds: 300
    - tier: 2
      channel: email
      timeout_seconds: 900
    - tier: 3
      channel: sms
      timeout_seconds: 60
  quiet_hours:
    timezone: America/Chicago
    start: "22:00"
    end: "07:00"
    p0_bypass: true
`

type fixture struct {
	a     *attempter
	tbl   *ddbtest.Table
	email *mock.Provider
	pub   ed25519.PublicKey
	now   *time.Time
}

func newFixture(t *testing.T, at time.Time) *fixture {
	t.Helper()
	policy, err := heartmap.Load(testPolicy)
	if err != nil {
		t.Fatal(err)
	}
	store := heartmap.NewMemoryStore()
	store.Put(policy)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := at
	clock := func() time.Time { return now }
	email := mock.New("ses-v2", heartmap.ChannelEmail)
	tbl := ddbtest.NewTable()
	return &fixture{
		a: &attempter{
			db: tbl, table: "t", policies: store, key: priv,
			dignity:    dignity.New(clock),
			breakers:   circuit.NewRegistry(circuit.DefaultConfig(), clock),
			translator: empathy.NewTranslator(nil, time.Second),
			providers:  map[string]providers.Messenger{heartmap.ChannelEmail: email},
			now:        clock,
			replyKey:   replytoken.KeyFrom(priv),
			replyURL:   "https://api.example/reply",
		},
		tbl: tbl, email: email, pub: pub, now: &now,
	}
}

func afternoon() time.Time {
	chicago, _ := time.LoadLocation("America/Chicago")
	return time.Date(2026, 10, 23, 14, 0, 0, 0, chicago)
}

func (f *fixture) raise(t *testing.T, id, correlation string) {
	t.Helper()
	if err := ddb.PutRequest(context.Background(), f.tbl, "t", ddb.Request{
		EscalationID: id, AgentID: "scout", RecipientID: "on-call", CorrelationID: correlation,
		Priority: "P1", Subject: "Hold dose?", Payload: "potassium 6.1", RequestedResponse: "hold or give",
	}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) walk(t *testing.T, id string, tier int) attemptOutput {
	t.Helper()
	out, err := f.a.handle(context.Background(), attemptInput{Action: "attempt", EscalationID: id, Tier: tier})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f *fixture) chain(t *testing.T, id string) []audit.Event {
	t.Helper()
	sink, err := ddb.NewSink(context.Background(), f.tbl, "t", id)
	if err != nil {
		t.Fatal(err)
	}
	return sink.All()
}

// The deployed walk: tier 1 has no provider and breaks its promise, tier 2
// delivers on email, and each invocation writes exactly its own receipt.
func TestTierOneBreaksThenTierTwoSends(t *testing.T) {
	f := newFixture(t, afternoon())
	f.raise(t, "esc-1", "bed-4")

	one := f.walk(t, "esc-1", 1)
	if one.Status != "FAILED" || one.NextTier == nil || *one.NextTier != 2 || one.TimeoutSeconds != 300 {
		t.Fatalf("tier 1 = %+v", one)
	}
	if got := f.a.breakers.For("unprovisioned:whatsapp").State(); got != circuit.Open {
		t.Fatalf("tier 1's breaker should be OPEN, got %s", got)
	}
	if len(f.email.Sent()) != 0 {
		t.Fatal("walking tier 1 must not touch email")
	}

	two := f.walk(t, "esc-1", 2)
	if two.Status != "SENT" || two.NextTier != nil || two.TimeoutSeconds != 900 {
		t.Fatalf("tier 2 = %+v", two)
	}
	sent := f.email.Sent()
	if len(sent) != 1 || sent[0].Address != "on-call@example.invalid" {
		t.Fatalf("expected one email to the Heart-Map address, got %d", len(sent))
	}
	if !strings.Contains(sent[0].Body, "potassium 6.1") || !strings.Contains(sent[0].Body, "Requested response: hold or give") {
		t.Fatalf("the message must carry the payload and the requested response, got %q", sent[0].Body)
	}

	events := f.chain(t, "esc-1")
	if len(events) != 2 || events[0].EventType != audit.TypeFailed || events[1].EventType != audit.TypeSent {
		t.Fatalf("want FAILED then SENT, got %d events", len(events))
	}
	if events[1].Metadata["provider_msg_id"] == "" {
		t.Fatal("SENT must carry the provider's message id")
	}
	if err := audit.Verify(events, f.pub); err != nil {
		t.Fatal(err)
	}
}

// Running the gate again at tier 2 would coalesce the escalation into the
// window tier 1 opened. Tier 2 must still send.
func TestLaterTierIsNotCoalescedIntoItself(t *testing.T) {
	f := newFixture(t, afternoon())
	f.raise(t, "esc-1", "same-correlation")
	f.walk(t, "esc-1", 1)
	if out := f.walk(t, "esc-1", 2); out.Status != "SENT" {
		t.Fatalf("tier 2 = %s, want SENT", out.Status)
	}
}

// The gate decides before any provider is touched, and its decision is a
// receipt, not silence.
func TestQuietHoursDeferBeforeAnyProvider(t *testing.T) {
	chicago, _ := time.LoadLocation("America/Chicago")
	f := newFixture(t, time.Date(2026, 10, 23, 23, 30, 0, 0, chicago))
	f.raise(t, "esc-1", "")

	out := f.walk(t, "esc-1", 1)
	if out.Status != string(dignity.Deferred) || out.NextTier != nil {
		t.Fatalf("got %+v, want DEFERRED with no next tier", out)
	}
	events := f.chain(t, "esc-1")
	if len(events) != 1 || events[0].EventType != audit.TypeDeferred {
		t.Fatalf("want one DEFERRED receipt, got %d", len(events))
	}
	if len(f.email.Sent()) != 0 {
		t.Fatal("a deferred escalation must not touch a provider")
	}
}

// The last tier failing is a ladder exhausted, written as such.
func TestLastTierFailingRecordsExhaustion(t *testing.T) {
	f := newFixture(t, afternoon())
	f.raise(t, "esc-1", "")
	f.walk(t, "esc-1", 1)

	out := f.walk(t, "esc-1", 3)
	if out.Status != "FAILED" || out.NextTier != nil {
		t.Fatalf("tier 3 = %+v", out)
	}
	events := f.chain(t, "esc-1")
	last := events[len(events)-1]
	if last.EventType != audit.TypeFailed || last.Metadata["reason"] != "ladder exhausted without delivery" {
		t.Fatalf("last receipt = %s %v", last.EventType, last.Metadata)
	}
}

func TestRejectsWhatItCannotWalk(t *testing.T) {
	f := newFixture(t, afternoon())
	f.raise(t, "esc-1", "")
	for name, in := range map[string]attemptInput{
		"unknown action":     {Action: "record_exhausted", EscalationID: "esc-1", Tier: 1},
		"tier off ladder":    {Action: "attempt", EscalationID: "esc-1", Tier: 7},
		"unknown escalation": {Action: "attempt", EscalationID: "nope", Tier: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.a.handle(context.Background(), in); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	if len(f.chain(t, "esc-1")) != 0 {
		t.Fatal("a rejected invocation must write nothing")
	}
}

// The link in the sent message must verify, name this escalation and
// recipient, carry the nonce now stored, and expire with the tier's timeout.
func TestSentMessageCarriesAVerifiableLink(t *testing.T) {
	f := newFixture(t, afternoon())
	f.raise(t, "esc-1", "")
	f.walk(t, "esc-1", 1)
	f.walk(t, "esc-1", 2)

	body := f.email.Sent()[0].Body
	_, after, ok := strings.Cut(body, "https://api.example/reply?t=")
	if !ok {
		t.Fatalf("no reply link in body: %q", body)
	}
	raw, _, _ := strings.Cut(after, "\n")
	token, err := url.QueryUnescape(raw)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := replytoken.Verify(f.a.replyKey, token, *f.now)
	if err != nil {
		t.Fatal(err)
	}
	if claims.EscalationID != "esc-1" || claims.RecipientID != "on-call" {
		t.Fatalf("claims = %+v", claims)
	}
	if got := time.Unix(claims.Expiry, 0).Sub(*f.now); got != 900*time.Second {
		t.Fatalf("link lives %s, want the tier's 900s", got)
	}
	slot, err := ddb.GetReply(context.Background(), f.tbl, "t", "esc-1")
	if err != nil || slot.Nonce != claims.Nonce {
		t.Fatalf("stored nonce does not match the link: %v", err)
	}
	if strings.Contains(fmtChain(t, f), token) {
		t.Fatal("the link reached the chain")
	}
}

func fmtChain(t *testing.T, f *fixture) string {
	t.Helper()
	var b strings.Builder
	for _, e := range f.chain(t, "esc-1") {
		b.WriteString(e.MessageHash)
		for _, v := range e.Metadata {
			b.WriteString(v)
		}
	}
	return b.String()
}
