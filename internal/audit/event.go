// Package audit produces the hash-chained, Ed25519-signed record of every
// promise Reachee makes and every promise it fails to keep. Message content
// never enters the log; only its SHA-256 hash does, so the chain is safe to
// hand to a party who should not read the messages.
package audit

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// EventType values are the promise states Reachee reports.
const (
	TypeSent         = "SENT"
	TypeReceived     = "RECEIVED"
	TypeAcknowledged = "ACKNOWLEDGED"
	TypeEscalated    = "ESCALATED"
	TypeFailed       = "FAILED"
	TypeCoalesced    = "COALESCED"
	TypeSuppressed   = "SUPPRESSED"
	TypeDeferred     = "DEFERRED"
	TypeBypassed     = "BYPASSED"
	TypeBreaker      = "BREAKER_STATE_CHANGE"
)

// Event is one link in the chain. Field order is the canonical serialization
// order; do not reorder without versioning the chain format.
type Event struct {
	EventID      string            `json:"event_id"`
	Timestamp    time.Time         `json:"timestamp"`
	PrevHash     string            `json:"prev_hash"`
	RequestID    string            `json:"request_id"`
	RecipientID  string            `json:"recipient_id"`
	ProviderName string            `json:"provider_name"`
	EventType    string            `json:"event_type"`
	MessageHash  string            `json:"message_hash"`
	Metadata     map[string]string `json:"metadata"`
	Signature    string            `json:"signature"`
}

// HashContent returns the SHA-256 of message content for MessageHash.
func HashContent(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// NewEventID returns a UUIDv7 so event ids sort by time.
func NewEventID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("audit: entropy unavailable: %v", err))
	}
	ms := uint64(time.Now().UnixMilli())
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	b[6] = (b[6] & 0x0f) | 0x70
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
