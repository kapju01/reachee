package ddb

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/aisolutions/reachee/internal/audit"
	"github.com/aisolutions/reachee/internal/ddb/ddbtest"
)

func TestRequestRoundTripsAndIsWrittenOnce(t *testing.T) {
	ctx := context.Background()
	tbl := ddbtest.NewTable()
	r := Request{
		EscalationID: "esc-1", AgentID: "agent", RecipientID: "justin-kaplan",
		Priority: "P1", Subject: "s", Payload: "p", RequestedResponse: "approve or deny",
		CreatedAt: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC),
	}
	if err := PutRequest(ctx, tbl, "t", r); err != nil {
		t.Fatal(err)
	}
	if err := PutRequest(ctx, tbl, "t", r); err == nil {
		t.Fatal("a second raise of the same escalation must be refused")
	}
	got, err := GetRequest(ctx, tbl, "t", "esc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != r {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, r)
	}
	if _, err := GetRequest(ctx, tbl, "t", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// The chain survives being written by one invocation and continued by
// another, and verifies end to end. The request record is not part of it.
func TestChainContinuesAcrossSinksAndVerifies(t *testing.T) {
	ctx := context.Background()
	tbl := ddbtest.NewTable()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := PutRequest(ctx, tbl, "t", Request{EscalationID: "esc-1", Payload: "secret body"}); err != nil {
		t.Fatal(err)
	}

	first, err := NewSink(ctx, tbl, "t", "esc-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audit.NewLogger(priv, first, nil).Emit(audit.Event{RequestID: "esc-1", EventType: audit.TypePromised}); err != nil {
		t.Fatal(err)
	}

	second, err := NewSink(ctx, tbl, "t", "esc-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := audit.NewLogger(priv, second, nil).Emit(audit.Event{RequestID: "esc-1", EventType: audit.TypeSent}); err != nil {
		t.Fatal(err)
	}

	events := second.All()
	if len(events) != 2 {
		t.Fatalf("want 2 chained events, got %d", len(events))
	}
	if err := audit.Verify(events, pub); err != nil {
		t.Fatal(err)
	}
}

// A sink must see only its own escalation. Chaining across escalations
// would make every chain depend on unrelated events.
func TestSinkIsScopedToOneEscalation(t *testing.T) {
	ctx := context.Background()
	tbl := ddbtest.NewTable()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	a, _ := NewSink(ctx, tbl, "t", "esc-a")
	if _, err := audit.NewLogger(priv, a, nil).Emit(audit.Event{RequestID: "esc-a", EventType: audit.TypePromised}); err != nil {
		t.Fatal(err)
	}
	b, _ := NewSink(ctx, tbl, "t", "esc-b")
	if len(b.All()) != 0 {
		t.Fatal("a new escalation's sink must start empty")
	}
	ev, err := audit.NewLogger(priv, b, nil).Emit(audit.Event{RequestID: "esc-b", EventType: audit.TypePromised})
	if err != nil {
		t.Fatal(err)
	}
	if ev.PrevHash != audit.GenesisHash {
		t.Fatal("a new escalation's first event must chain from genesis, not another escalation")
	}
}

// Two writers holding the same head must not both succeed.
func TestRacingWritersCannotForkTheChain(t *testing.T) {
	ctx := context.Background()
	tbl := ddbtest.NewTable()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)

	x, _ := NewSink(ctx, tbl, "t", "esc-1")
	y, _ := NewSink(ctx, tbl, "t", "esc-1")
	if _, err := audit.NewLogger(priv, x, nil).Emit(audit.Event{EventType: audit.TypeSent}); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.NewLogger(priv, y, nil).Emit(audit.Event{EventType: audit.TypeFailed}); err == nil {
		t.Fatal("the second writer at the same seq must fail")
	}
}
