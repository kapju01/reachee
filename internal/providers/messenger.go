// Package providers defines the contract every channel implements. The
// dispatcher knows only this interface, so adding AWS End User Messaging
// Social, SES v2, or Pinpoint SMS Voice v2 does not touch dispatcher logic.
package providers

import (
	"context"
	"time"
)

// Priority mirrors the escalation tiers declared by calling agents.
type Priority string

// Priority values.
const (
	P0 Priority = "P0"
	P1 Priority = "P1"
	P2 Priority = "P2"
	P3 Priority = "P3"
)

// Message is one delivery attempt's payload.
type Message struct {
	RequestID   string
	RecipientID string
	Address     string
	Subject     string
	Body        string
	Priority    Priority
	Metadata    map[string]string
}

// Receipt is what a provider returns when it keeps its promise.
type Receipt struct {
	ProviderName  string
	ProviderMsgID string
	AcceptedAt    time.Time
}

// Messenger is the provider contract.
type Messenger interface {
	Send(ctx context.Context, msg Message) (Receipt, error)
	Name() string
	Channel() string
	HealthCheck(ctx context.Context) error
}
