// Package dignity spends human attention deliberately. Coalescing, rate
// limiting, and quiet hours all run before any provider is touched, so a
// suppressed escalation costs a human nothing and still costs the audit log
// an honest entry.
package dignity

import (
	"sync"
	"time"

	"github.com/aisolutions/reachee/internal/heartmap"
	"github.com/aisolutions/reachee/internal/providers"
)

// Verdict is the pipeline's decision about one escalation.
type Verdict string

// Verdict values.
const (
	Deliver    Verdict = "DELIVER"
	Coalesced  Verdict = "COALESCED"
	Deferred   Verdict = "DEFERRED"
	Suppressed Verdict = "SUPPRESSED"
	Bypassed   Verdict = "BYPASSED" // P0 through quiet hours, logged for morning review
)

// Decision carries the verdict plus the reason a human or an auditor will read.
type Decision struct {
	Verdict     Verdict
	Reason      string
	DeliverAt   time.Time
	CoalescedTo string // request id of the group this merged into
}

// Delivers reports whether the escalation proceeds to the provider ladder.
func (d Decision) Delivers() bool { return d.Verdict == Deliver || d.Verdict == Bypassed }

// Escalation is the subset of a request the pipeline reasons over.
type Escalation struct {
	RequestID     string
	RecipientID   string
	CorrelationID string
	Priority      providers.Priority
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

type group struct {
	leadRequestID string
	expiresAt     time.Time
	members       []string
}

// Pipeline holds coalescing groups and rate limit buckets. State is in memory
// here and moves to DynamoDB with TTLs in the deployed stack; the dispatcher
// does not care which.
type Pipeline struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]*bucket
	groups  map[string]*group
}

// New builds a Pipeline. Passing a nil clock uses time.Now.
func New(now func() time.Time) *Pipeline {
	if now == nil {
		now = time.Now
	}
	return &Pipeline{now: now, buckets: map[string]*bucket{}, groups: map[string]*group{}}
}

// Evaluate applies coalescing, then quiet hours, then rate limiting.
func (p *Pipeline) Evaluate(e Escalation, policy heartmap.Policy) Decision {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()

	if e.CorrelationID != "" && policy.RateLimit.CoalesceWindow > 0 {
		key := e.RecipientID + ":" + e.CorrelationID
		if g, ok := p.groups[key]; ok && g.expiresAt.After(now) {
			g.members = append(g.members, e.RequestID)
			return Decision{
				Verdict:     Coalesced,
				Reason:      "merged into an open coalescing window for the same correlation id",
				CoalescedTo: g.leadRequestID,
			}
		}
		p.groups[key] = &group{
			leadRequestID: e.RequestID,
			expiresAt:     now.Add(policy.RateLimit.CoalesceWindow),
			members:       []string{e.RequestID},
		}
	}

	if policy.InQuietHours(now) {
		if e.Priority != providers.P0 {
			return Decision{
				Verdict:   Deferred,
				Reason:    "recipient is inside declared quiet hours",
				DeliverAt: policy.NextAllowed(now),
			}
		}
		if !policy.QuietHours.P0Bypass {
			return Decision{
				Verdict:   Deferred,
				Reason:    "quiet hours with no P0 bypass declared",
				DeliverAt: policy.NextAllowed(now),
			}
		}
		if !p.takeToken(e, policy, now) {
			return Decision{Verdict: Bypassed, Reason: "P0 bypassed quiet hours over an exhausted rate limit", DeliverAt: now}
		}
		return Decision{Verdict: Bypassed, Reason: "P0 bypassed quiet hours", DeliverAt: now}
	}

	if !p.takeToken(e, policy, now) {
		if e.Priority == providers.P0 || e.Priority == providers.P1 {
			return Decision{Verdict: Bypassed, Reason: "rate limit exhausted, priority overrides", DeliverAt: now}
		}
		return Decision{Verdict: Suppressed, Reason: "recipient rate limit exhausted for this hour"}
	}
	return Decision{Verdict: Deliver, Reason: "within policy", DeliverAt: now}
}

func (p *Pipeline) takeToken(e Escalation, policy heartmap.Policy, now time.Time) bool {
	max := float64(policy.RateLimit.MaxPerHour)
	if max <= 0 {
		return true
	}
	capacity := float64(policy.RateLimit.Burst)
	if capacity <= 0 {
		capacity = max
	}
	b, ok := p.buckets[e.RecipientID]
	if !ok {
		b = &bucket{tokens: capacity, lastRefill: now}
		p.buckets[e.RecipientID] = b
	}
	elapsed := now.Sub(b.lastRefill).Hours()
	if elapsed > 0 {
		b.tokens += elapsed * max
		if b.tokens > capacity {
			b.tokens = capacity
		}
		b.lastRefill = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// CoalescedMembers returns the request ids merged into a correlation group.
func (p *Pipeline) CoalescedMembers(recipientID, correlationID string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	g, ok := p.groups[recipientID+":"+correlationID]
	if !ok {
		return nil
	}
	out := make([]string, len(g.members))
	copy(out, g.members)
	return out
}
