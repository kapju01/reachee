package audit

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"
)

func newTestLogger(t *testing.T) (*Logger, *MemorySink, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sink := &MemorySink{}
	return NewLogger(priv, sink, nil), sink, pub
}

func TestChainVerifies(t *testing.T) {
	l, sink, pub := newTestLogger(t)
	for i := 0; i < 5; i++ {
		if _, err := l.Emit(Event{RequestID: "req-1", EventType: TypeSent, MessageHash: HashContent("body")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := Verify(sink.All(), pub); err != nil {
		t.Fatalf("expected a valid chain, got %v", err)
	}
}

func TestFirstEventAnchorsToGenesis(t *testing.T) {
	l, sink, _ := newTestLogger(t)
	if _, err := l.Emit(Event{RequestID: "req-1", EventType: TypeSent}); err != nil {
		t.Fatal(err)
	}
	if got := sink.All()[0].PrevHash; got != GenesisHash {
		t.Fatalf("expected the genesis hash, got %s", got)
	}
}

func TestReorderingBreaksTheChain(t *testing.T) {
	l, sink, pub := newTestLogger(t)
	for i := 0; i < 3; i++ {
		if _, err := l.Emit(Event{RequestID: "req-1", EventType: TypeSent}); err != nil {
			t.Fatal(err)
		}
	}
	events := sink.All()
	events[1], events[2] = events[2], events[1]
	if err := Verify(events, pub); !errors.Is(err, ErrChainBroken) {
		t.Fatalf("expected a broken chain, got %v", err)
	}
}

func TestModifiedEventBreaksTheChain(t *testing.T) {
	l, sink, pub := newTestLogger(t)
	for i := 0; i < 3; i++ {
		if _, err := l.Emit(Event{RequestID: "req-1", EventType: TypeSent, MessageHash: HashContent("body")}); err != nil {
			t.Fatal(err)
		}
	}
	events := sink.All()
	events[1].MessageHash = HashContent("tampered body")
	if err := Verify(events, pub); !errors.Is(err, ErrChainBroken) {
		t.Fatalf("expected a broken chain, got %v", err)
	}
}

func TestWrongKeyFailsVerification(t *testing.T) {
	l, sink, _ := newTestLogger(t)
	if _, err := l.Emit(Event{RequestID: "req-1", EventType: TypeSent}); err != nil {
		t.Fatal(err)
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(sink.All(), other); !errors.Is(err, ErrChainBroken) {
		t.Fatalf("expected verification against the wrong key to fail, got %v", err)
	}
}

func TestContentNeverEntersTheLog(t *testing.T) {
	l, sink, _ := newTestLogger(t)
	secret := "variance of 2.04M in the Q4 close"
	if _, err := l.Emit(Event{RequestID: "req-1", EventType: TypeSent, MessageHash: HashContent(secret)}); err != nil {
		t.Fatal(err)
	}
	canonical, err := Canonical(sink.All()[0])
	if err != nil {
		t.Fatal(err)
	}
	if contains(string(canonical), secret) {
		t.Fatal("plaintext content must never appear in an audit event")
	}
}

func TestLoggerResumesAnExistingChain(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sink := &MemorySink{}
	first := NewLogger(priv, sink, nil)
	if _, err := first.Emit(Event{RequestID: "req-1", EventType: TypeSent}); err != nil {
		t.Fatal(err)
	}
	resumed := NewLogger(priv, sink, func() time.Time { return time.Now() })
	if _, err := resumed.Emit(Event{RequestID: "req-2", EventType: TypeSent}); err != nil {
		t.Fatal(err)
	}
	if err := Verify(sink.All(), pub); err != nil {
		t.Fatalf("a resumed logger must continue the chain, got %v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
