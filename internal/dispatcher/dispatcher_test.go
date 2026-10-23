package dispatcher

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/aisolutions/reachee/internal/audit"
	"github.com/aisolutions/reachee/internal/circuit"
	"github.com/aisolutions/reachee/internal/dignity"
	"github.com/aisolutions/reachee/internal/empathy"
	"github.com/aisolutions/reachee/internal/heartmap"
	"github.com/aisolutions/reachee/internal/providers"
	"github.com/aisolutions/reachee/internal/providers/mock"
)

type harness struct {
	d        *Dispatcher
	sink     *audit.MemorySink
	pub      ed25519.PublicKey
	whatsapp *mock.Provider
	ses      *mock.Provider
	sms      *mock.Provider
}

func newHarness(t *testing.T, waFailures, sesFailures int, now func() time.Time) *harness {
	t.Helper()
	chicago, _ := time.LoadLocation("America/Chicago")
	policy := heartmap.Policy{
		ID:          "justin-kaplan",
		DisplayName: "Justin Kaplan",
		Channels: map[string]heartmap.Channel{
			heartmap.ChannelWhatsApp: {Name: heartmap.ChannelWhatsApp, Address: "+10000000001"},
			heartmap.ChannelEmail:    {Name: heartmap.ChannelEmail, Address: "justin@example.invalid"},
			heartmap.ChannelSMS:      {Name: heartmap.ChannelSMS, Address: "+10000000002", Lifeline: true},
		},
		Ladder: []heartmap.Rung{
			{Tier: 1, Channel: heartmap.ChannelWhatsApp, Timeout: 300 * time.Second},
			{Tier: 2, Channel: heartmap.ChannelEmail, Timeout: 900 * time.Second},
			{Tier: 3, Channel: heartmap.ChannelSMS, Timeout: 60 * time.Second},
		},
		QuietHours: heartmap.QuietHours{Location: chicago, Start: "22:00", End: "07:00", P0Bypass: true},
		RateLimit:  heartmap.RateLimit{MaxPerHour: 6, Burst: 3, CoalesceWindow: 90 * time.Second},
	}
	store := heartmap.NewMemoryStore()
	store.Put(policy)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sink := &audit.MemorySink{}

	wa := mock.Failing("whatsapp-socialmessaging", heartmap.ChannelWhatsApp, waFailures)
	ses := mock.Failing("ses-v2", heartmap.ChannelEmail, sesFailures)
	sms := mock.New("pinpoint-sms-voice-v2", heartmap.ChannelSMS)

	return &harness{
		d: &Dispatcher{
			Policies:   store,
			Dignity:    dignity.New(now),
			Breakers:   circuit.NewRegistry(circuit.DefaultConfig(), now),
			Translator: empathy.NewTranslator(nil, time.Second),
			Audit:      audit.NewLogger(priv, sink, now),
			Providers: map[string]providers.Messenger{
				heartmap.ChannelWhatsApp: wa,
				heartmap.ChannelEmail:    ses,
				heartmap.ChannelSMS:      sms,
			},
			Now: now,
		},
		sink: sink, pub: pub, whatsapp: wa, ses: ses, sms: sms,
	}
}

func request(id string, p providers.Priority) Request {
	return Request{
		RequestID:     id,
		AgentID:       "scout-close-reconciler",
		AgentAutonomy: "Supervised",
		RecipientID:   "justin-kaplan",
		CorrelationID: "q4-close-" + id,
		Priority:      p,
		Subject:       "Q4 close variance",
		Payload:       "variance above tolerance, review required",
	}
}

func daytime() time.Time {
	chicago, _ := time.LoadLocation("America/Chicago")
	return time.Date(2026, 10, 14, 10, 0, 0, 0, chicago)
}

func TestFirstTierDeliversWhenHealthy(t *testing.T) {
	now := daytime()
	h := newHarness(t, 0, 0, func() time.Time { return now })
	r, err := h.d.Dispatch(context.Background(), request("r1", providers.P1))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Delivered || r.DeliveredVia != "whatsapp-socialmessaging" {
		t.Fatalf("expected tier 1 delivery, got %+v", r)
	}
	if len(h.whatsapp.Sent()) != 1 {
		t.Fatalf("expected one WhatsApp send, got %d", len(h.whatsapp.Sent()))
	}
	if err := audit.Verify(h.sink.All(), h.pub); err != nil {
		t.Fatalf("audit chain must verify: %v", err)
	}
}

func TestFailoverWalksTheLadderInOrder(t *testing.T) {
	now := daytime()
	h := newHarness(t, -1, -1, func() time.Time { return now })
	r, err := h.d.Dispatch(context.Background(), request("r1", providers.P1))
	if err != nil {
		t.Fatal(err)
	}
	if r.DeliveredVia != "pinpoint-sms-voice-v2" {
		t.Fatalf("expected the lifeline tier to deliver, got %s", r.DeliveredVia)
	}
	events := h.sink.All()
	if len(events) != 3 {
		t.Fatalf("expected two broken promises and one kept, got %d events", len(events))
	}
	if events[0].EventType != audit.TypeFailed || events[1].EventType != audit.TypeFailed || events[2].EventType != audit.TypeSent {
		t.Fatalf("unexpected event sequence: %s, %s, %s", events[0].EventType, events[1].EventType, events[2].EventType)
	}
	if err := audit.Verify(events, h.pub); err != nil {
		t.Fatalf("audit chain must verify: %v", err)
	}
}

func TestOpenBreakerSkipsTheTierAsSignal(t *testing.T) {
	now := daytime()
	h := newHarness(t, -1, 0, func() time.Time { return now })

	for i := 0; i < 3; i++ {
		// Distinct correlation ids so nothing coalesces away from the provider.
		if _, err := h.d.Dispatch(context.Background(), request(string(rune('a'+i)), providers.P0)); err != nil {
			t.Fatal(err)
		}
	}
	if got := h.d.Breakers.For("whatsapp-socialmessaging").State(); got != circuit.Open {
		t.Fatalf("expected the WhatsApp breaker to be OPEN, got %s", got)
	}

	before := len(h.sink.All())
	if _, err := h.d.Dispatch(context.Background(), request("r4", providers.P0)); err != nil {
		t.Fatal(err)
	}
	var skipped bool
	for _, e := range h.sink.All()[before:] {
		if e.EventType == audit.TypeEscalated && e.ProviderName == "whatsapp-socialmessaging" {
			skipped = true
			if e.Metadata["breaker_state"] != string(circuit.Open) {
				t.Fatalf("the skip event must carry the breaker state, got %q", e.Metadata["breaker_state"])
			}
		}
	}
	if !skipped {
		t.Fatal("a skipped tier must appear in the audit log as ESCALATED, not as silence")
	}
	if len(h.whatsapp.Sent()) != 0 {
		t.Fatal("an open breaker must not be dialed")
	}
}

func TestSuppressedEscalationStillProducesAnAuditEvent(t *testing.T) {
	now := daytime()
	h := newHarness(t, 0, 0, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if _, err := h.d.Dispatch(context.Background(), request(string(rune('a'+i)), providers.P3)); err != nil {
			t.Fatal(err)
		}
	}
	r, err := h.d.Dispatch(context.Background(), request("d", providers.P3))
	if err != nil {
		t.Fatal(err)
	}
	if r.Delivered {
		t.Fatal("a suppressed escalation must not be delivered")
	}
	last := h.sink.All()[len(h.sink.All())-1]
	if last.EventType != audit.TypeSuppressed {
		t.Fatalf("expected a SUPPRESSED event, got %s", last.EventType)
	}
	if last.Metadata["reason"] == "" {
		t.Fatal("a suppression must record its reason")
	}
}

func TestQuietHoursDeferralIsAudited(t *testing.T) {
	chicago, _ := time.LoadLocation("America/Chicago")
	now := time.Date(2026, 10, 14, 23, 30, 0, 0, chicago)
	h := newHarness(t, 0, 0, func() time.Time { return now })

	r, err := h.d.Dispatch(context.Background(), request("r1", providers.P2))
	if err != nil {
		t.Fatal(err)
	}
	if r.Delivered {
		t.Fatal("a deferred escalation must not reach a provider")
	}
	if h.sink.All()[0].EventType != audit.TypeDeferred {
		t.Fatalf("expected a DEFERRED event, got %s", h.sink.All()[0].EventType)
	}
	if len(h.whatsapp.Sent()) != 0 {
		t.Fatal("quiet hours must run before any provider is touched")
	}
}

func TestPayloadNeverReachesTheAuditLog(t *testing.T) {
	now := daytime()
	h := newHarness(t, 0, 0, func() time.Time { return now })
	req := request("r1", providers.P1)
	if _, err := h.d.Dispatch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	e := h.sink.All()[0]
	if e.MessageHash != audit.HashContent(req.Payload) {
		t.Fatal("the audit event must carry the content hash")
	}
	for k, v := range e.Metadata {
		if v == req.Payload {
			t.Fatalf("metadata key %q leaked the payload", k)
		}
	}
}

func TestLadderExhaustionIsReportedHonestly(t *testing.T) {
	now := daytime()
	h := newHarness(t, -1, -1, func() time.Time { return now })
	h.d.Providers[heartmap.ChannelSMS] = mock.Failing("pinpoint-sms-voice-v2", heartmap.ChannelSMS, -1)

	r, err := h.d.Dispatch(context.Background(), request("r1", providers.P0))
	if err == nil {
		t.Fatal("expected an error when the ladder is exhausted")
	}
	if r.Delivered {
		t.Fatal("nothing was delivered")
	}
	last := h.sink.All()[len(h.sink.All())-1]
	if last.EventType != audit.TypeFailed {
		t.Fatalf("expected a terminal FAILED event, got %s", last.EventType)
	}
}

func TestUnknownRecipientIsRejected(t *testing.T) {
	now := daytime()
	h := newHarness(t, 0, 0, func() time.Time { return now })
	req := request("r1", providers.P1)
	req.RecipientID = "nobody"
	if _, err := h.d.Dispatch(context.Background(), req); err == nil {
		t.Fatal("expected an error for an unknown recipient")
	}
}

// Tier 1 of the deployed ladder has no provider. That must be a named broken
// promise that opens its breaker, and the next escalation must see the skip.
func TestUnregisteredChannelBreaksThePromiseAndOpensTheBreaker(t *testing.T) {
	now := daytime()
	h := newHarness(t, 0, 0, func() time.Time { return now })
	delete(h.d.Providers, heartmap.ChannelWhatsApp)

	if _, err := h.d.Dispatch(context.Background(), request("a", providers.P1)); err != nil {
		t.Fatal(err)
	}
	events := h.sink.All()
	if len(events) != 2 || events[0].EventType != audit.TypeFailed || events[1].EventType != audit.TypeSent {
		t.Fatalf("expected FAILED then SENT, got %d events", len(events))
	}
	if events[0].Metadata["channel"] != heartmap.ChannelWhatsApp || events[0].Metadata["breaker_state"] != string(circuit.Open) {
		t.Fatalf("the broken promise must name its channel and the open breaker, got %v", events[0].Metadata)
	}

	if _, err := h.d.Dispatch(context.Background(), request("b", providers.P1)); err != nil {
		t.Fatal(err)
	}
	skip := h.sink.All()[2]
	if skip.EventType != audit.TypeEscalated || skip.Metadata["breaker_state"] != string(circuit.Open) {
		t.Fatalf("an open breaker must produce a skip receipt, got %s %v", skip.EventType, skip.Metadata)
	}
}

// AttemptTier is what one Lambda invocation runs: one rung, one receipt.
func TestAttemptTierWalksExactlyOneRung(t *testing.T) {
	now := daytime()
	h := newHarness(t, -1, 0, func() time.Time { return now })
	policy, _ := h.d.Policies.Resolve("justin-kaplan")
	req := request("one", providers.P1)
	translated := h.d.Translate(context.Background(), req, policy)

	res, err := h.d.AttemptTier(context.Background(), req, policy, policy.Ladder[0], translated)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != StatusFailed || res.Err == nil {
		t.Fatalf("tier 1 should fail, got %s", res.Status)
	}
	if got := len(h.sink.All()); got != 1 {
		t.Fatalf("one rung must write exactly one receipt, got %d", got)
	}
	if len(h.ses.Sent()) != 0 || len(h.sms.Sent()) != 0 {
		t.Fatal("walking tier 1 must not touch any other tier's provider")
	}
}

// Gate runs once per escalation. Running it again for a later tier would
// coalesce the escalation into the window it opened itself.
func TestGateTwiceCoalescesIntoItself(t *testing.T) {
	now := daytime()
	h := newHarness(t, 0, 0, func() time.Time { return now })
	policy, _ := h.d.Policies.Resolve("justin-kaplan")
	req := request("self", providers.P1)

	first, _, err := h.d.Gate(req, policy)
	if err != nil || !first.Delivers() {
		t.Fatalf("first gate should deliver, got %s %v", first.Verdict, err)
	}
	second, _, _ := h.d.Gate(req, policy)
	if second.Verdict != dignity.Coalesced {
		t.Fatalf("this test documents why Gate is called once; got %s", second.Verdict)
	}
}
