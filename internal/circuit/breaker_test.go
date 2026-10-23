package circuit

import (
	"testing"
	"time"
)

func clockAt(t *time.Time) func() time.Time {
	return func() time.Time { return *t }
}

// Regression: the May 2026 build tripped a breaker on two observations because
// the error-rate path was evaluated against a window too small to carry a rate.
func TestErrorRateDormantBelowMinSamples(t *testing.T) {
	now := time.Now()
	cfg := DefaultConfig()
	cfg.ConsecutiveFailures = 10 // isolate the error-rate path
	b := New("whatsapp", cfg, clockAt(&now))

	b.Success()
	if got := b.Failure(); got != Closed {
		t.Fatalf("two observations must not trip the breaker, got %s", got)
	}
	if !b.Allow() {
		t.Fatal("breaker should still admit attempts")
	}
}

func TestErrorRateTripsOnceWindowIsPopulated(t *testing.T) {
	now := time.Now()
	cfg := DefaultConfig()
	cfg.ConsecutiveFailures = 100
	cfg.MinSamples = 5
	b := New("whatsapp", cfg, clockAt(&now))

	b.Success()
	b.Success()
	b.Failure()
	b.Failure()
	if b.State() != Closed {
		t.Fatalf("four observations at 50 percent must not trip, got %s", b.State())
	}
	b.Failure() // 3 of 5 failed, above the 0.5 threshold
	if b.State() != Open {
		t.Fatalf("expected OPEN once the window is populated, got %s", b.State())
	}
}

func TestConsecutiveFailuresTrip(t *testing.T) {
	now := time.Now()
	b := New("ses", DefaultConfig(), clockAt(&now))
	b.Failure()
	b.Failure()
	if b.State() != Closed {
		t.Fatalf("expected CLOSED after two failures, got %s", b.State())
	}
	b.Failure()
	if b.State() != Open {
		t.Fatalf("expected OPEN after three consecutive failures, got %s", b.State())
	}
	if b.Allow() {
		t.Fatal("an open breaker must not admit attempts")
	}
}

func TestHalfOpenProbeSingleAdmission(t *testing.T) {
	now := time.Now()
	cfg := DefaultConfig()
	b := New("sms", cfg, clockAt(&now))
	b.Failure()
	b.Failure()
	b.Failure()

	now = now.Add(cfg.Cooldown + time.Second)
	if b.State() != HalfOpen {
		t.Fatalf("expected HALF_OPEN after cooldown, got %s", b.State())
	}
	if !b.Allow() {
		t.Fatal("first probe should be admitted")
	}
	if b.Allow() {
		t.Fatal("second concurrent probe must be refused")
	}
	if got := b.Success(); got != Closed {
		t.Fatalf("a successful probe closes the breaker, got %s", got)
	}
}

func TestHalfOpenFailureReopens(t *testing.T) {
	now := time.Now()
	cfg := DefaultConfig()
	b := New("sms", cfg, clockAt(&now))
	b.Failure()
	b.Failure()
	b.Failure()
	now = now.Add(cfg.Cooldown + time.Second)
	b.Allow()
	if got := b.Failure(); got != Open {
		t.Fatalf("a failed probe reopens the breaker, got %s", got)
	}
}

func TestObservationsFallOutOfWindow(t *testing.T) {
	now := time.Now()
	cfg := DefaultConfig()
	cfg.ConsecutiveFailures = 100
	cfg.MinSamples = 3
	b := New("whatsapp", cfg, clockAt(&now))

	b.Failure()
	b.Failure()

	// Everything above ages out of the rolling window.
	now = now.Add(cfg.Window + time.Second)
	b.Success()
	b.Failure()

	if b.State() != Closed {
		t.Fatalf("stale observations must not count toward the error rate, got %s", b.State())
	}
}
