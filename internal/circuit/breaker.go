// Package circuit implements the per-provider breaker. A broken provider is
// skipped rather than retried into the ground, and every state change is
// reported as signal rather than suppressed as noise.
package circuit

import (
	"sync"
	"time"
)

// State is the breaker position.
type State string

// Breaker states.
const (
	Closed   State = "CLOSED"
	Open     State = "OPEN"
	HalfOpen State = "HALF_OPEN"
)

// Config tunes a breaker.
type Config struct {
	Window              time.Duration // rolling observation window
	ConsecutiveFailures int           // trip after this many failures in a row
	ErrorRate           float64       // trip above this rate; set > 1.0 to disable
	MinSamples          int           // error-rate path stays dormant below this count
	Cooldown            time.Duration // wait before a half-open probe
}

// DefaultConfig is the Reachee baseline.
//
// ErrorRate is 0.5 but it only activates once MinSamples observations are in
// the window. The May 2026 build tripped a breaker on two samples because a
// rate threshold was evaluated against a window too small to mean anything;
// MinSamples is the fix, and the small-window case is covered by tests.
func DefaultConfig() Config {
	return Config{
		Window:              60 * time.Second,
		ConsecutiveFailures: 3,
		ErrorRate:           0.5,
		MinSamples:          5,
		Cooldown:            30 * time.Second,
	}
}

type observation struct {
	at      time.Time
	success bool
}

// Breaker guards one provider in one region.
type Breaker struct {
	mu           sync.Mutex
	name         string
	cfg          Config
	now          func() time.Time
	state        State
	observations []observation
	consecutive  int
	openedAt     time.Time
	probing      bool
}

// New builds a breaker. Passing a nil clock uses time.Now.
func New(name string, cfg Config, now func() time.Time) *Breaker {
	if now == nil {
		now = time.Now
	}
	return &Breaker{name: name, cfg: cfg, now: now, state: Closed}
}

// Name returns the guarded provider name.
func (b *Breaker) Name() string { return b.name }

// State returns the current position, advancing Open to HalfOpen once the
// cooldown has elapsed.
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.currentLocked()
}

func (b *Breaker) currentLocked() State {
	if b.state == Open && b.now().Sub(b.openedAt) >= b.cfg.Cooldown {
		b.state = HalfOpen
		b.probing = false
	}
	return b.state
}

// Allow reports whether an attempt may proceed. In HalfOpen exactly one probe
// is admitted per cooldown.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.currentLocked() {
	case Closed:
		return true
	case HalfOpen:
		if b.probing {
			return false
		}
		b.probing = true
		return true
	default:
		return false
	}
}

// Success records a kept promise. It closes a half-open breaker.
func (b *Breaker) Success() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record(true)
	b.consecutive = 0
	if b.state == HalfOpen {
		b.state = Closed
		b.observations = nil
		b.probing = false
	}
	return b.state
}

// Failure records a broken promise and trips the breaker when warranted.
func (b *Breaker) Failure() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record(false)
	b.consecutive++

	if b.state == HalfOpen {
		b.trip()
		return b.state
	}
	if b.consecutive >= b.cfg.ConsecutiveFailures {
		b.trip()
		return b.state
	}
	if b.cfg.ErrorRate <= 1.0 && len(b.observations) >= b.cfg.MinSamples {
		failures := 0
		for _, o := range b.observations {
			if !o.success {
				failures++
			}
		}
		if float64(failures)/float64(len(b.observations)) > b.cfg.ErrorRate {
			b.trip()
		}
	}
	return b.state
}

func (b *Breaker) trip() {
	b.state = Open
	b.openedAt = b.now()
	b.probing = false
	b.consecutive = 0
}

func (b *Breaker) record(success bool) {
	now := b.now()
	b.observations = append(b.observations, observation{at: now, success: success})
	cutoff := now.Add(-b.cfg.Window)
	kept := b.observations[:0]
	for _, o := range b.observations {
		if o.at.After(cutoff) {
			kept = append(kept, o)
		}
	}
	b.observations = kept
}

// Registry holds one breaker per provider name.
type Registry struct {
	mu       sync.Mutex
	cfg      Config
	now      func() time.Time
	breakers map[string]*Breaker
}

// NewRegistry builds a registry.
func NewRegistry(cfg Config, now func() time.Time) *Registry {
	return &Registry{cfg: cfg, now: now, breakers: map[string]*Breaker{}}
}

// For returns the breaker for a provider, creating it on first use.
func (r *Registry) For(name string) *Breaker {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.breakers[name]
	if !ok {
		b = New(name, r.cfg, r.now)
		r.breakers[name] = b
	}
	return b
}
