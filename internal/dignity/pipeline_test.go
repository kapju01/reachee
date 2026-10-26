package dignity

import (
	"testing"
	"time"

	"github.com/aisolutions/reachee/internal/heartmap"
	"github.com/aisolutions/reachee/internal/providers"
)

func testPolicy() heartmap.Policy {
	chicago, _ := time.LoadLocation("America/Chicago")
	return heartmap.Policy{
		ID:          "justin-kaplan",
		DisplayName: "Justin Kaplan",
		QuietHours:  heartmap.QuietHours{Location: chicago, Start: "22:00", End: "07:00", P0Bypass: true},
		RateLimit:   heartmap.RateLimit{MaxPerHour: 6, Burst: 3, CoalesceWindow: 90 * time.Second},
	}
}

func daytime() time.Time {
	chicago, _ := time.LoadLocation("America/Chicago")
	return time.Date(2026, 10, 14, 10, 0, 0, 0, chicago)
}

func TestCoalescesWithinTheWindow(t *testing.T) {
	now := daytime()
	p := New(func() time.Time { return now })
	policy := testPolicy()

	first := p.Evaluate(Escalation{RequestID: "r1", RecipientID: policy.ID, CorrelationID: "q4", Priority: providers.P1}, policy)
	if first.Verdict != Deliver {
		t.Fatalf("expected the first escalation to deliver, got %s", first.Verdict)
	}
	now = now.Add(30 * time.Second)
	second := p.Evaluate(Escalation{RequestID: "r2", RecipientID: policy.ID, CorrelationID: "q4", Priority: providers.P1}, policy)
	if second.Verdict != Coalesced || second.CoalescedTo != "r1" {
		t.Fatalf("expected r2 to coalesce into r1, got %+v", second)
	}
	if got := p.CoalescedMembers(policy.ID, "q4"); len(got) != 2 {
		t.Fatalf("expected two members in the group, got %d", len(got))
	}
}

func TestCoalescingWindowExpires(t *testing.T) {
	now := daytime()
	p := New(func() time.Time { return now })
	policy := testPolicy()

	p.Evaluate(Escalation{RequestID: "r1", RecipientID: policy.ID, CorrelationID: "q4", Priority: providers.P2}, policy)
	now = now.Add(91 * time.Second)
	second := p.Evaluate(Escalation{RequestID: "r2", RecipientID: policy.ID, CorrelationID: "q4", Priority: providers.P2}, policy)
	if second.Verdict != Deliver {
		t.Fatalf("expected delivery once the window expires, got %s", second.Verdict)
	}
}

func TestDistinctCorrelationsDoNotCoalesce(t *testing.T) {
	now := daytime()
	p := New(func() time.Time { return now })
	policy := testPolicy()

	p.Evaluate(Escalation{RequestID: "r1", RecipientID: policy.ID, CorrelationID: "q4", Priority: providers.P2}, policy)
	second := p.Evaluate(Escalation{RequestID: "r2", RecipientID: policy.ID, CorrelationID: "payroll", Priority: providers.P2}, policy)
	if second.Verdict != Deliver {
		t.Fatalf("a different correlation id must not coalesce, got %s", second.Verdict)
	}
}

func TestRateLimitSuppressesLowPriority(t *testing.T) {
	now := daytime()
	p := New(func() time.Time { return now })
	policy := testPolicy()

	for i := 0; i < 3; i++ {
		d := p.Evaluate(Escalation{RequestID: "r", RecipientID: policy.ID, Priority: providers.P2}, policy)
		if d.Verdict != Deliver {
			t.Fatalf("burst escalation %d should deliver, got %s", i, d.Verdict)
		}
	}
	d := p.Evaluate(Escalation{RequestID: "r4", RecipientID: policy.ID, Priority: providers.P2}, policy)
	if d.Verdict != Suppressed {
		t.Fatalf("expected suppression past the burst, got %s", d.Verdict)
	}
}

func TestRateLimitRefills(t *testing.T) {
	now := daytime()
	p := New(func() time.Time { return now })
	policy := testPolicy()

	for i := 0; i < 3; i++ {
		p.Evaluate(Escalation{RequestID: "r", RecipientID: policy.ID, Priority: providers.P2}, policy)
	}
	// Six per hour refills one token in ten minutes.
	now = now.Add(11 * time.Minute)
	d := p.Evaluate(Escalation{RequestID: "r5", RecipientID: policy.ID, Priority: providers.P2}, policy)
	if d.Verdict != Deliver {
		t.Fatalf("expected a refilled token to allow delivery, got %s (%s)", d.Verdict, d.Reason)
	}
}

func TestPriorityOverridesAnExhaustedBucket(t *testing.T) {
	now := daytime()
	p := New(func() time.Time { return now })
	policy := testPolicy()

	for i := 0; i < 3; i++ {
		p.Evaluate(Escalation{RequestID: "r", RecipientID: policy.ID, Priority: providers.P2}, policy)
	}
	d := p.Evaluate(Escalation{RequestID: "r4", RecipientID: policy.ID, Priority: providers.P0}, policy)
	if d.Verdict != Bypassed || !d.Delivers() {
		t.Fatalf("P0 must still reach the human, got %s", d.Verdict)
	}
}

func TestQuietHoursDeferBelowP0(t *testing.T) {
	chicago, _ := time.LoadLocation("America/Chicago")
	now := time.Date(2026, 10, 14, 23, 30, 0, 0, chicago)
	p := New(func() time.Time { return now })
	policy := testPolicy()

	d := p.Evaluate(Escalation{RequestID: "r1", RecipientID: policy.ID, Priority: providers.P1}, policy)
	if d.Verdict != Deferred {
		t.Fatalf("expected deferral inside quiet hours, got %s", d.Verdict)
	}
	if d.DeliverAt.In(chicago).Hour() != 7 {
		t.Fatalf("expected deferral to 07:00 local, got %s", d.DeliverAt.In(chicago))
	}
}

func TestQuietHoursP0BypassIsLogged(t *testing.T) {
	chicago, _ := time.LoadLocation("America/Chicago")
	now := time.Date(2026, 10, 14, 23, 30, 0, 0, chicago)
	p := New(func() time.Time { return now })
	policy := testPolicy()

	d := p.Evaluate(Escalation{RequestID: "r1", RecipientID: policy.ID, Priority: providers.P0}, policy)
	if d.Verdict != Bypassed || !d.Delivers() {
		t.Fatalf("expected a logged P0 bypass, got %s", d.Verdict)
	}
}

func TestQuietHoursWithoutBypassDefersEvenP0(t *testing.T) {
	chicago, _ := time.LoadLocation("America/Chicago")
	now := time.Date(2026, 10, 14, 23, 30, 0, 0, chicago)
	p := New(func() time.Time { return now })
	policy := testPolicy()
	policy.QuietHours.P0Bypass = false

	d := p.Evaluate(Escalation{RequestID: "r1", RecipientID: policy.ID, Priority: providers.P0}, policy)
	if d.Verdict != Deferred {
		t.Fatalf("with no declared bypass, even P0 defers, got %s", d.Verdict)
	}
}

// The budget the gate spends is the one the Heart-Map declares. testPolicy
// matches the loader's defaults, so this one is loaded from a document that
// declares a burst no default supplies.
func TestDeclaredBurstIsTheBudgetSpent(t *testing.T) {
	policy, err := heartmap.Load("contact:\n  id: someone\n  channels:\n    email:\n      address: a@b.invalid\n" +
		"  failover_ladder:\n    - tier: 1\n      channel: email\n" +
		"  rate_limit:\n    max_per_hour: 11\n    burst: 5\n    coalesce_window_seconds: 240\n")
	if err != nil {
		t.Fatal(err)
	}
	now := daytime()
	p := New(func() time.Time { return now })
	for i := 0; i < 5; i++ {
		if d := p.Evaluate(Escalation{RequestID: "r", RecipientID: policy.ID, Priority: providers.P2}, policy); d.Verdict != Deliver {
			t.Fatalf("reach %d of a declared burst of 5 = %s, want delivered", i+1, d.Verdict)
		}
	}
	if d := p.Evaluate(Escalation{RequestID: "r6", RecipientID: policy.ID, Priority: providers.P2}, policy); d.Verdict != Suppressed {
		t.Fatalf("reach 6 = %s, want suppressed past the declared burst", d.Verdict)
	}
}
