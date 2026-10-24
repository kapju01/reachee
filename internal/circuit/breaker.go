// Package circuit implements the per-provider breaker. A broken provider is
// skipped rather than retried into the ground, and every state change is
// reported as signal rather than suppressed as noise.
package circuit

import (
	"context"
	"errors"
	"reflect"
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
// Cooldown is fifteen minutes because a half-open probe is a reach: on a
// provisioned channel it sends a real message to a real person. Fifteen
// minutes allows four probes an hour per breaker against a Heart-Map that
// permits six reaches an hour, so re-probing a broken channel can never spend
// the attention budget on its own.
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
		Cooldown:            15 * time.Minute,
	}
}

// Observation is one recorded outcome inside the rolling window.
type Observation struct {
	At      time.Time `json:"at"`
	Success bool      `json:"success"`
}

// Breaker guards one provider in one region.
type Breaker struct {
	mu           sync.Mutex
	name         string
	cfg          Config
	now          func() time.Time
	state        State
	observations []Observation
	consecutive  int
	openedAt     time.Time
	probing      bool
	probeAt      time.Time
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
// is admitted per cooldown. A probe whose outcome never arrives, because the
// invocation running it died, lapses after one cooldown; once the state is
// persisted, a claim that never lapsed would hold the breaker half-open for
// good.
func (b *Breaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.currentLocked() {
	case Closed:
		return true
	case HalfOpen:
		if b.probing && b.now().Sub(b.probeAt) < b.cfg.Cooldown {
			return false
		}
		b.probing = true
		b.probeAt = b.now()
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
			if !o.Success {
				failures++
			}
		}
		if float64(failures)/float64(len(b.observations)) > b.cfg.ErrorRate {
			b.trip()
		}
	}
	return b.state
}

// Trip opens the breaker at once and records the failure. It is for failures
// that cannot be transient, such as a channel with no provider registered:
// waiting out the consecutive-failure threshold would spend two more attempts
// proving what the first already showed.
func (b *Breaker) Trip() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.record(false)
	b.trip()
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
	b.observations = append(b.observations, Observation{At: now, Success: success})
	cutoff := now.Add(-b.cfg.Window)
	kept := b.observations[:0]
	for _, o := range b.observations {
		if o.At.After(cutoff) {
			kept = append(kept, o)
		}
	}
	b.observations = kept
}

// Snapshot is a breaker's complete position, as a Store keeps it. State is
// the stored state, not advanced for the cooldown; restoring it into a breaker
// and reading State applies the clock.
type Snapshot struct {
	State        State         `json:"state"`
	Consecutive  int           `json:"consecutive"`
	OpenedAt     time.Time     `json:"opened_at"`
	Probing      bool          `json:"probing"`
	ProbeAt      time.Time     `json:"probe_at"`
	Observations []Observation `json:"observations"`
}

// Snapshot returns the breaker's position.
func (b *Breaker) Snapshot() Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return Snapshot{
		State: b.state, Consecutive: b.consecutive, OpenedAt: b.openedAt,
		Probing: b.probing, ProbeAt: b.probeAt,
		Observations: append([]Observation(nil), b.observations...),
	}
}

// Restore replaces the breaker's position with s. A zero Snapshot is a new,
// closed breaker.
func (b *Breaker) Restore(s Snapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = s.State
	if b.state == "" {
		b.state = Closed
	}
	b.consecutive = s.Consecutive
	b.openedAt = s.OpenedAt
	b.probing = s.Probing
	b.probeAt = s.ProbeAt
	b.observations = append([]Observation(nil), s.Observations...)
}

// ErrConflict is what a Store returns when the version it was asked to
// replace is no longer the stored one.
var ErrConflict = errors.New("circuit: breaker changed underneath this write")

// Store keeps breaker positions outside any one process, so a breaker opened
// by one invocation is open for the next, cold start or not. Load returns a
// zero Snapshot and version 0 for a breaker never written. Save replaces the
// position only if the stored version is still version, and returns
// ErrConflict otherwise.
type Store interface {
	Load(ctx context.Context, name string) (Snapshot, int64, error)
	Save(ctx context.Context, name string, s Snapshot, version int64) error
}

// syncAttempts bounds how often Use reloads and reapplies after a conflict.
// A conflict means another invocation moved the breaker first; three is far
// more contention than one escalation's traffic produces.
const syncAttempts = 3

// Registry holds one breaker per provider name, and with a Store, keeps each
// one's position in it.
type Registry struct {
	mu       sync.Mutex
	cfg      Config
	now      func() time.Time
	store    Store
	breakers map[string]*Breaker
}

// NewRegistry builds a registry whose breakers live in this process only.
func NewRegistry(cfg Config, now func() time.Time) *Registry {
	return &Registry{cfg: cfg, now: now, breakers: map[string]*Breaker{}}
}

// NewStoredRegistry builds a registry whose breakers are loaded from and
// saved to store on every Use.
func NewStoredRegistry(cfg Config, now func() time.Time, store Store) *Registry {
	r := NewRegistry(cfg, now)
	r.store = store
	return r
}

// Use runs op against the named breaker. With a Store, the breaker is loaded
// first and, if op moved it, saved conditional on the version loaded, so two
// invocations cannot fork it. On a conflict the breaker is reloaded and op
// runs again against the newer position, so op must set its results afresh
// each time it runs. Without a Store, op runs against the in-process breaker.
func (r *Registry) Use(ctx context.Context, name string, op func(*Breaker)) error {
	if r.store == nil {
		op(r.For(name))
		return nil
	}
	for i := 0; i < syncAttempts; i++ {
		before, version, err := r.store.Load(ctx, name)
		if err != nil {
			return err
		}
		b := New(name, r.cfg, r.now)
		b.Restore(before)
		op(b)
		after := b.Snapshot()
		if reflect.DeepEqual(normalize(before), normalize(after)) {
			return nil
		}
		err = r.store.Save(ctx, name, after, version)
		if !errors.Is(err, ErrConflict) {
			return err
		}
	}
	return ErrConflict
}

// normalize makes a never-written breaker and a restored closed one compare
// equal, so reading a breaker does not write it.
func normalize(s Snapshot) Snapshot {
	if s.State == "" {
		s.State = Closed
	}
	if len(s.Observations) == 0 {
		s.Observations = nil
	}
	return s
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
