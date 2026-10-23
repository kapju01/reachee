package ddb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aisolutions/reachee/internal/ddb/ddbtest"
)

func TestReplyLifecycle(t *testing.T) {
	ctx, tbl, now := context.Background(), ddbtest.NewTable(), time.Now()

	if err := MintReply(ctx, tbl, "t", "e", "n1", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A later tier supersedes an earlier, undelivered link.
	if err := MintReply(ctx, tbl, "t", "e", "n2", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := SpendReply(ctx, tbl, "t", "e", "n1", "direction", "", now); !errors.Is(err, ErrNonceMismatch) {
		t.Fatalf("superseded nonce: err = %v", err)
	}
	if _, err := SpendReply(ctx, tbl, "t", "e", "n2", "direction", "", now); !errors.Is(err, ErrNotArmed) {
		t.Fatalf("before arm: err = %v", err)
	}
	r, _ := GetReply(ctx, tbl, "t", "e")
	if !r.SpentAt.IsZero() {
		t.Fatal("a refused-not-ready answer must not spend the nonce")
	}

	if err := ArmReply(ctx, tbl, "t", "e", "task-token", now); err != nil {
		t.Fatal(err)
	}
	if err := MintReply(ctx, tbl, "t", "e", "n3", now); err == nil {
		t.Fatal("minting over an armed slot must be refused")
	}
	got, err := SpendReply(ctx, tbl, "t", "e", "n2", "denial", "not today", now)
	if err != nil || got.TaskToken != "task-token" || got.Decision != "denial" || got.Answer != "not today" {
		t.Fatalf("spend = %+v, %v", got, err)
	}
	if _, err := SpendReply(ctx, tbl, "t", "e", "n2", "direction", "", now); !errors.Is(err, ErrSpent) {
		t.Fatalf("replay: err = %v", err)
	}
}

func TestClosedSlotRefusesLateAnswers(t *testing.T) {
	ctx, tbl, now := context.Background(), ddbtest.NewTable(), time.Now()
	_ = MintReply(ctx, tbl, "t", "e", "n", now.Add(time.Minute))
	_ = ArmReply(ctx, tbl, "t", "e", "tok", now)
	if err := CloseReply(ctx, tbl, "t", "e", "timeout", now); err != nil {
		t.Fatal(err)
	}
	if _, err := SpendReply(ctx, tbl, "t", "e", "n", "direction", "", now); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v", err)
	}
}

func TestArmRequiresAMintedLink(t *testing.T) {
	ctx, tbl := context.Background(), ddbtest.NewTable()
	if err := ArmReply(ctx, tbl, "t", "e", "tok", time.Now()); err == nil {
		t.Fatal("armed a hold that no link can answer")
	}
}

// The slot sits below the request record and must never appear in a chain.
func TestReplySlotStaysOutOfTheChain(t *testing.T) {
	ctx, tbl := context.Background(), ddbtest.NewTable()
	_ = MintReply(ctx, tbl, "t", "e", "n", time.Now().Add(time.Minute))
	_ = PutRequest(ctx, tbl, "t", Request{EscalationID: "e"})
	sink, err := NewSink(ctx, tbl, "t", "e")
	if err != nil || len(sink.All()) != 0 {
		t.Fatalf("chain should be empty, got %d events, %v", len(sink.All()), err)
	}
}
