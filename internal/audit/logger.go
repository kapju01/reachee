package audit

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// GenesisHash anchors the first event of a chain.
const GenesisHash = "0000000000000000000000000000000000000000000000000000000000000000"

// Sink persists signed events. MemorySink backs local runs; DynamoDB plus an
// S3 Object Lock archive back the deployed stack.
type Sink interface {
	Append(Event) error
	All() []Event
}

// MemorySink is an in-memory Sink.
type MemorySink struct {
	mu     sync.Mutex
	events []Event
}

// Append implements Sink.
func (s *MemorySink) Append(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
	return nil
}

// All implements Sink.
func (s *MemorySink) All() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.events))
	copy(out, s.events)
	return out
}

// Logger signs and chains events.
type Logger struct {
	mu       sync.Mutex
	priv     ed25519.PrivateKey
	sink     Sink
	prevHash string
	now      func() time.Time
}

// NewLogger builds a Logger over a sink. Passing a nil clock uses time.Now.
func NewLogger(priv ed25519.PrivateKey, sink Sink, now func() time.Time) *Logger {
	if now == nil {
		now = time.Now
	}
	prev := GenesisHash
	if existing := sink.All(); len(existing) > 0 {
		last := existing[len(existing)-1]
		h, err := SignedHash(last)
		if err == nil {
			prev = h
		}
	}
	return &Logger{priv: priv, sink: sink, prevHash: prev, now: now}
}

// Emit signs, chains, and persists one event. The caller supplies everything
// except EventID, Timestamp, PrevHash, and Signature.
func (l *Logger) Emit(e Event) (Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	e.EventID = NewEventID()
	e.Timestamp = l.now().UTC()
	e.PrevHash = l.prevHash
	e.Signature = ""
	if e.Metadata == nil {
		e.Metadata = map[string]string{}
	}

	canonical, err := Canonical(e)
	if err != nil {
		return Event{}, err
	}
	e.Signature = hex.EncodeToString(ed25519.Sign(l.priv, canonical))

	if err := l.sink.Append(e); err != nil {
		return Event{}, err
	}
	h, err := SignedHash(e)
	if err != nil {
		return Event{}, err
	}
	l.prevHash = h
	return e, nil
}

// TimestampFormat is the single textual form of an event timestamp. It is
// written into the canonical map and into every export, so no verifier ever
// has to parse and reformat a time to reproduce the bytes that were signed.
const TimestampFormat = "2006-01-02T15:04:05.000000Z07:00"

// CanonicalMap is the event as the chain sees it: a plain string-keyed map,
// with Signature blanked. Both Canonical and any chain export build on this,
// so what a verifier reads is exactly what was signed.
//
// Why a map and not the struct: encoding/json emits struct fields in
// declaration order and map keys in sorted order. An independent verifier
// written in another language sorts keys, so signing the struct would make
// agreement a coincidence of field order. Sorting on both sides makes it a
// property.
func CanonicalMap(e Event) map[string]any {
	md := e.Metadata
	if md == nil {
		md = map[string]string{}
	}
	return map[string]any{
		"event_id":      e.EventID,
		"timestamp":     e.Timestamp.UTC().Truncate(time.Microsecond).Format(TimestampFormat),
		"prev_hash":     e.PrevHash,
		"request_id":    e.RequestID,
		"recipient_id":  e.RecipientID,
		"provider_name": e.ProviderName,
		"event_type":    e.EventType,
		"message_hash":  e.MessageHash,
		"metadata":      md,
		"signature":     "",
	}
}

// Canonical returns the deterministic byte form signed for an event. Signature
// is excluded: it signs everything else.
//
// HTML escaping is turned off. encoding/json escapes <, > and & by default,
// which no other language's JSON encoder does, and a single angle bracket in a
// metadata value would otherwise break cross-language verification.
func Canonical(e Event) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(CanonicalMap(e)); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// SignedHash is the SHA-256 of the canonical form concatenated with the
// signature. The next event references this value as PrevHash.
func SignedHash(e Event) (string, error) {
	canonical, err := Canonical(e)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(canonical)
	h.Write([]byte(e.Signature))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ErrChainBroken reports a chain that fails verification.
var ErrChainBroken = errors.New("audit: chain verification failed")

// Verify walks a chain and checks every signature and every back-reference.
// It needs only the public key, so any third party can run it offline.
func Verify(events []Event, pub ed25519.PublicKey) error {
	prev := GenesisHash
	for i, e := range events {
		if e.PrevHash != prev {
			return fmt.Errorf("%w: event %d (%s) expected prev_hash %s, found %s", ErrChainBroken, i, e.EventID, prev, e.PrevHash)
		}
		canonical, err := Canonical(e)
		if err != nil {
			return err
		}
		sig, err := hex.DecodeString(e.Signature)
		if err != nil {
			return fmt.Errorf("%w: event %d (%s) has a malformed signature", ErrChainBroken, i, e.EventID)
		}
		if !ed25519.Verify(pub, canonical, sig) {
			return fmt.Errorf("%w: event %d (%s) signature does not verify", ErrChainBroken, i, e.EventID)
		}
		prev, err = SignedHash(e)
		if err != nil {
			return err
		}
	}
	return nil
}
