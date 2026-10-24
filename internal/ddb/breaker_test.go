package ddb_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aisolutions/reachee/internal/circuit"
	"github.com/aisolutions/reachee/internal/ddb"
	"github.com/aisolutions/reachee/internal/ddb/ddbtest"
)

func clock(at *time.Time) func() time.Time { return func() time.Time { return *at } }

// Two registries stand in for two cold execution environments. A breaker
// tripped in one is open in the other, which is the whole point.
func TestTrippedBreakerIsOpenForTheNextEnvironment(t *testing.T) {
	ctx := context.Background()
	tbl := ddbtest.NewTable()
	now := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	store := ddb.BreakerStore{Client: tbl, Table: "t"}
	cfg := circuit.DefaultConfig()

	first := circuit.NewStoredRegistry(cfg, clock(&now), store)
	var tripped circuit.State
	if err := first.Use(ctx, "unprovisioned:whatsapp", func(b *circuit.Breaker) { tripped = b.Trip() }); err != nil {
		t.Fatal(err)
	}
	if tripped != circuit.Open {
		t.Fatalf("Trip must open at once, got %s", tripped)
	}

	now = now.Add(5 * time.Minute)
	second := circuit.NewStoredRegistry(cfg, clock(&now), store)
	var allowed bool
	var state circuit.State
	if err := second.Use(ctx, "unprovisioned:whatsapp", func(b *circuit.Breaker) { allowed, state = b.Allow(), b.State() }); err != nil {
		t.Fatal(err)
	}
	if allowed || state != circuit.Open {
		t.Fatalf("five minutes later in a new environment: allowed=%v state=%s, want refused and OPEN", allowed, state)
	}

	now = now.Add(cfg.Cooldown)
	third := circuit.NewStoredRegistry(cfg, clock(&now), store)
	if err := third.Use(ctx, "unprovisioned:whatsapp", func(b *circuit.Breaker) { allowed, state = b.Allow(), b.State() }); err != nil {
		t.Fatal(err)
	}
	if !allowed || state != circuit.HalfOpen {
		t.Fatalf("past the cooldown: allowed=%v state=%s, want one probe admitted", allowed, state)
	}
}

// The probe claim is itself a conditional write, so a second environment
// arriving while the probe is out is refused rather than probing too.
func TestOneProbeAcrossEnvironments(t *testing.T) {
	ctx := context.Background()
	tbl := ddbtest.NewTable()
	now := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	store := ddb.BreakerStore{Client: tbl, Table: "t"}
	cfg := circuit.DefaultConfig()
	if err := circuit.NewStoredRegistry(cfg, clock(&now), store).Use(ctx, "ses-v2", func(b *circuit.Breaker) { b.Trip() }); err != nil {
		t.Fatal(err)
	}
	now = now.Add(cfg.Cooldown)

	admitted := 0
	for range 2 {
		var ok bool
		if err := circuit.NewStoredRegistry(cfg, clock(&now), store).Use(ctx, "ses-v2", func(b *circuit.Breaker) { ok = b.Allow() }); err != nil {
			t.Fatal(err)
		}
		if ok {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("%d probes admitted, want exactly one", admitted)
	}
}

// Failures below the threshold are not transitions, but they must persist,
// or a provider failing once per cold start would never open its breaker.
func TestFailureCountsSurviveEnvironments(t *testing.T) {
	ctx := context.Background()
	tbl := ddbtest.NewTable()
	now := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	store := ddb.BreakerStore{Client: tbl, Table: "t"}
	var state circuit.State
	for i := range circuit.DefaultConfig().ConsecutiveFailures {
		now = now.Add(time.Second)
		if err := circuit.NewStoredRegistry(circuit.DefaultConfig(), clock(&now), store).Use(ctx, "ses-v2", func(b *circuit.Breaker) { state = b.Failure() }); err != nil {
			t.Fatal(err)
		}
		if want := circuit.Closed; i < 2 && state != want {
			t.Fatalf("failure %d: %s", i+1, state)
		}
	}
	if state != circuit.Open {
		t.Fatalf("three failures in three environments left the breaker %s", state)
	}
}

// A read that changes nothing writes nothing.
func TestReadingAClosedBreakerWritesNothing(t *testing.T) {
	tbl := ddbtest.NewTable()
	store := ddb.BreakerStore{Client: tbl, Table: "t"}
	if err := circuit.NewStoredRegistry(circuit.DefaultConfig(), nil, store).Use(context.Background(), "ses-v2", func(b *circuit.Breaker) { b.Allow() }); err != nil {
		t.Fatal(err)
	}
	if n := len(tbl.Items(ddb.BreakerPrefix + "ses-v2")); n != 0 {
		t.Fatalf("a read of a closed breaker wrote %d items", n)
	}
}

// racing saves a second writer's change in the gap between the first
// writer's Load and Save, exactly once.
type racing struct {
	ddb.BreakerStore
	once sync.Once
	race func()
}

func (r *racing) Save(ctx context.Context, name string, s circuit.Snapshot, v int64) error {
	r.once.Do(r.race)
	return r.BreakerStore.Save(ctx, name, s, v)
}

// A lost race reapplies the outcome to the winner's position instead of
// overwriting it, so neither invocation's failure is lost.
func TestConflictReappliesRatherThanOverwrites(t *testing.T) {
	ctx := context.Background()
	tbl := ddbtest.NewTable()
	now := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	base := ddb.BreakerStore{Client: tbl, Table: "t"}
	store := &racing{BreakerStore: base}
	store.race = func() {
		if err := circuit.NewStoredRegistry(circuit.DefaultConfig(), clock(&now), base).Use(ctx, "ses-v2", func(b *circuit.Breaker) { b.Failure() }); err != nil {
			t.Fatal(err)
		}
	}
	if err := circuit.NewStoredRegistry(circuit.DefaultConfig(), clock(&now), store).Use(ctx, "ses-v2", func(b *circuit.Breaker) { b.Failure() }); err != nil {
		t.Fatal(err)
	}
	snap, version, err := base.Load(ctx, "ses-v2")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Consecutive != 2 || version != 2 {
		t.Fatalf("after two racing failures: consecutive=%d version=%d, want 2 and 2", snap.Consecutive, version)
	}
}

func TestStaleVersionIsAConflict(t *testing.T) {
	ctx := context.Background()
	store := ddb.BreakerStore{Client: ddbtest.NewTable(), Table: "t"}
	if err := store.Save(ctx, "ses-v2", circuit.Snapshot{State: circuit.Open}, 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, "ses-v2", circuit.Snapshot{State: circuit.Closed}, 0); !errors.Is(err, circuit.ErrConflict) {
		t.Fatalf("a second first-write succeeded or failed wrongly: %v", err)
	}
}
