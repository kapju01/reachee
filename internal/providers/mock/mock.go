// Package mock provides a recording Messenger for local development and tests.
// It stands in for the three AWS CDS providers until those land in week three.
package mock

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/aisolutions/reachee/internal/providers"
)

// Provider is a scriptable Messenger.
type Provider struct {
	mu       sync.Mutex
	name     string
	channel  string
	failures int // fail this many sends before succeeding; -1 fails forever
	sent     []providers.Message
	now      func() time.Time
}

// ErrProviderDown is the canned transport failure.
var ErrProviderDown = errors.New("mock: provider unavailable")

// New builds a mock that always succeeds.
func New(name, channel string) *Provider {
	return &Provider{name: name, channel: channel, now: time.Now}
}

// Failing builds a mock that fails the first n sends. Pass -1 to fail always.
func Failing(name, channel string, n int) *Provider {
	p := New(name, channel)
	p.failures = n
	return p
}

// Name implements providers.Messenger.
func (p *Provider) Name() string { return p.name }

// Channel implements providers.Messenger.
func (p *Provider) Channel() string { return p.channel }

// HealthCheck implements providers.Messenger.
func (p *Provider) HealthCheck(context.Context) error { return nil }

// Send implements providers.Messenger.
func (p *Provider) Send(_ context.Context, msg providers.Message) (providers.Receipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failures != 0 {
		if p.failures > 0 {
			p.failures--
		}
		return providers.Receipt{}, ErrProviderDown
	}
	p.sent = append(p.sent, msg)
	return providers.Receipt{
		ProviderName:  p.name,
		ProviderMsgID: "mock-" + msg.RequestID,
		AcceptedAt:    p.now(),
	}, nil
}

// Sent returns everything this mock accepted.
func (p *Provider) Sent() []providers.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]providers.Message, len(p.sent))
	copy(out, p.sent)
	return out
}
