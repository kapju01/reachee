// Package dispatcher walks the Heart-Map ladder in order, one tier at a time,
// emitting an audit event for every promise kept, broken, or skipped.
package dispatcher

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aisolutions/reachee/internal/audit"
	"github.com/aisolutions/reachee/internal/circuit"
	"github.com/aisolutions/reachee/internal/dignity"
	"github.com/aisolutions/reachee/internal/empathy"
	"github.com/aisolutions/reachee/internal/heartmap"
	"github.com/aisolutions/reachee/internal/providers"
)

// Request is the calling contract for a Scout agent.
type Request struct {
	RequestID         string
	AgentID           string
	AgentAutonomy     string
	RecipientID       string
	CorrelationID     string
	Priority          providers.Priority
	Subject           string
	Payload           string
	RequestedResponse string
	Metadata          map[string]string
}

// Receipt is what Reachee hands back to the calling agent.
type Receipt struct {
	RequestID          string
	ReceiptID          string
	Delivered          bool
	DeliveredVia       string
	DeliveredAt        time.Time
	ExpectedResponseBy time.Time
	Verdict            dignity.Verdict
	Reason             string
	EmpathySource      empathy.Source
	ReceiptHash        string
	Signature          string
}

// ErrLadderExhausted means every tier declined or failed.
var ErrLadderExhausted = errors.New("dispatcher: ladder exhausted without delivery")

// Dispatcher wires policy, dignity, breakers, providers, and the audit log.
type Dispatcher struct {
	Policies   heartmap.Store
	Dignity    *dignity.Pipeline
	Breakers   *circuit.Registry
	Translator *empathy.Translator
	Audit      *audit.Logger
	Providers  map[string]providers.Messenger // keyed by channel name
	Now        func() time.Time
}

func (d *Dispatcher) now() time.Time {
	if d.Now == nil {
		return time.Now()
	}
	return d.Now()
}

// Tier status values report what walking one rung did.
const (
	StatusSent    = "SENT"
	StatusFailed  = "FAILED"
	StatusSkipped = "SKIPPED"
)

// TierResult is the outcome of walking one rung. Err says why a rung that was
// not SENT did not deliver; it has already been written into Event.
type TierResult struct {
	Status   string
	Event    audit.Event
	Provider string
	Receipt  providers.Receipt
	Err      error
}

// Dispatch runs one escalation end to end, in process. The deployed stack
// walks the same steps one Lambda invocation at a time: Gate and the first
// AttemptTier in one, each later AttemptTier in its own.
func (d *Dispatcher) Dispatch(ctx context.Context, req Request) (Receipt, error) {
	policy, err := d.Policies.Resolve(req.RecipientID)
	if err != nil {
		return Receipt{}, err
	}

	decision, ev, err := d.Gate(req, policy)
	if err != nil {
		return Receipt{}, err
	}
	if !decision.Delivers() {
		return Receipt{
			RequestID: req.RequestID,
			ReceiptID: ev.EventID,
			Verdict:   decision.Verdict,
			Reason:    decision.Reason,
			Signature: ev.Signature,
		}, nil
	}

	translated := d.Translate(ctx, req, policy)

	var lastErr error
	for _, rung := range policy.Ladder {
		res, err := d.AttemptTier(ctx, req, policy, rung, translated)
		if err != nil {
			return Receipt{}, err
		}
		if res.Status != StatusSent {
			lastErr = res.Err
			continue
		}
		hash, err := audit.SignedHash(res.Event)
		if err != nil {
			return Receipt{}, err
		}
		return Receipt{
			RequestID:          req.RequestID,
			ReceiptID:          res.Event.EventID,
			Delivered:          true,
			DeliveredVia:       res.Provider,
			DeliveredAt:        res.Receipt.AcceptedAt,
			ExpectedResponseBy: d.now().Add(rung.Timeout),
			Verdict:            decision.Verdict,
			Reason:             decision.Reason,
			EmpathySource:      translated.Source,
			ReceiptHash:        hash,
			Signature:          res.Event.Signature,
		}, nil
	}

	if _, err := d.Exhausted(req); err != nil {
		return Receipt{}, err
	}
	if lastErr == nil {
		lastErr = ErrLadderExhausted
	}
	return Receipt{
		RequestID: req.RequestID,
		Verdict:   decision.Verdict,
		Reason:    lastErr.Error(),
	}, fmt.Errorf("%w: %v", ErrLadderExhausted, lastErr)
}

// Gate runs the dignity pipeline once for an escalation and records its
// decision. It must run once per escalation, not once per tier: a second
// evaluation would find the coalescing window the first one opened and merge
// the escalation into itself, and would spend a second rate-limit token on
// the same event. The returned event is the verdict receipt when the
// escalation does not deliver, and the zero Event otherwise.
func (d *Dispatcher) Gate(req Request, policy heartmap.Policy) (dignity.Decision, audit.Event, error) {
	decision := d.Dignity.Evaluate(dignity.Escalation{
		RequestID:     req.RequestID,
		RecipientID:   req.RecipientID,
		CorrelationID: req.CorrelationID,
		Priority:      req.Priority,
	}, policy)
	contentHash := audit.HashContent(req.Payload)

	if !decision.Delivers() {
		ev, err := d.emit(req, "", verdictEventType(decision.Verdict), contentHash, map[string]string{
			"reason":        decision.Reason,
			"verdict":       string(decision.Verdict),
			"coalesced_to":  decision.CoalescedTo,
			"deliver_after": decision.DeliverAt.Format(time.RFC3339),
		})
		return decision, ev, err
	}
	if decision.Verdict == dignity.Bypassed {
		if _, err := d.emit(req, "", audit.TypeBypassed, contentHash, map[string]string{
			"reason": decision.Reason,
		}); err != nil {
			return decision, audit.Event{}, err
		}
	}
	return decision, audit.Event{}, nil
}

// Translate renders the message a person will read.
func (d *Dispatcher) Translate(ctx context.Context, req Request, policy heartmap.Policy) empathy.Result {
	return d.Translator.Translate(ctx, empathy.Request{
		RecipientName: policy.DisplayName,
		Subject:       req.Subject,
		RawContent:    req.Payload,
		Priority:      string(req.Priority),
	})
}

// AttemptTier walks exactly one rung and writes exactly one receipt for it:
// SENT, FAILED, or ESCALATED when an open breaker skips the tier. A returned
// error means the receipt could not be written, which is the one failure that
// cannot be reported in the chain itself.
//
// A channel with no provider registered is a broken promise, not a gap to
// step over silently. It trips that channel's breaker at once, because no
// number of retries will make an unprovisioned channel deliver.
func (d *Dispatcher) AttemptTier(ctx context.Context, req Request, policy heartmap.Policy, rung heartmap.Rung, translated empathy.Result) (TierResult, error) {
	contentHash := audit.HashContent(req.Payload)
	tier := strconv.Itoa(rung.Tier)

	provider, registered := d.Providers[rung.Channel]
	breakerName := "unprovisioned:" + rung.Channel
	providerName := ""
	if registered {
		breakerName = provider.Name()
		providerName = provider.Name()
	}
	breaker := d.Breakers.For(breakerName)

	if !breaker.Allow() {
		state := breaker.State()
		ev, err := d.emit(req, providerName, audit.TypeEscalated, contentHash, map[string]string{
			"tier":          tier,
			"channel":       rung.Channel,
			"reason":        "circuit breaker is " + string(state) + ", tier skipped",
			"breaker_state": string(state),
		})
		return TierResult{
			Status: StatusSkipped, Event: ev, Provider: providerName,
			Err: fmt.Errorf("dispatcher: %s breaker %s", breakerName, state),
		}, err
	}

	if !registered {
		state := breaker.Trip()
		cause := fmt.Errorf("dispatcher: no provider registered for channel %q", rung.Channel)
		ev, err := d.emit(req, "", audit.TypeFailed, contentHash, map[string]string{
			"tier":          tier,
			"channel":       rung.Channel,
			"reason":        "promise broken: no provider registered for channel " + rung.Channel,
			"breaker_state": string(state),
		})
		return TierResult{Status: StatusFailed, Event: ev, Err: cause}, err
	}

	address, ok := policy.Address(rung.Channel)
	if !ok {
		cause := fmt.Errorf("dispatcher: contact %s has no address on channel %q", policy.ID, rung.Channel)
		ev, err := d.emit(req, providerName, audit.TypeFailed, contentHash, map[string]string{
			"tier":    tier,
			"channel": rung.Channel,
			"reason":  "promise broken: contact has no address on channel " + rung.Channel,
		})
		return TierResult{Status: StatusFailed, Event: ev, Provider: providerName, Err: cause}, err
	}

	attemptCtx, cancel := context.WithTimeout(ctx, rung.Timeout)
	receipt, sendErr := provider.Send(attemptCtx, providers.Message{
		RequestID:   req.RequestID,
		RecipientID: req.RecipientID,
		Address:     address,
		Subject:     req.Subject,
		Body:        translated.Body,
		Priority:    req.Priority,
		Metadata:    req.Metadata,
	})
	cancel()

	if sendErr != nil {
		state := breaker.Failure()
		ev, err := d.emit(req, providerName, audit.TypeFailed, contentHash, map[string]string{
			"tier":          tier,
			"channel":       rung.Channel,
			"reason":        "promise broken by provider: " + sendErr.Error(),
			"breaker_state": string(state),
		})
		return TierResult{Status: StatusFailed, Event: ev, Provider: providerName, Err: sendErr}, err
	}

	breaker.Success()
	ev, err := d.emit(req, providerName, audit.TypeSent, contentHash, map[string]string{
		"tier":            tier,
		"channel":         rung.Channel,
		"provider_msg_id": receipt.ProviderMsgID,
		"empathy_source":  string(translated.Source),
		"empathy_reason":  translated.Reason,
		"agent_id":        req.AgentID,
		"agent_autonomy":  req.AgentAutonomy,
	})
	return TierResult{Status: StatusSent, Event: ev, Provider: providerName, Receipt: receipt}, err
}

// Exhausted records that every tier was walked and none delivered. Reaching
// nobody is reported as the failure it is.
func (d *Dispatcher) Exhausted(req Request) (audit.Event, error) {
	return d.emit(req, "", audit.TypeFailed, audit.HashContent(req.Payload), map[string]string{
		"reason": "ladder exhausted without delivery",
	})
}

func (d *Dispatcher) emit(req Request, provider, eventType, contentHash string, meta map[string]string) (audit.Event, error) {
	clean := map[string]string{}
	for k, v := range meta {
		if v != "" {
			clean[k] = v
		}
	}
	if req.CorrelationID != "" {
		clean["correlation_id"] = req.CorrelationID
	}
	clean["priority"] = string(req.Priority)
	return d.Audit.Emit(audit.Event{
		RequestID:    req.RequestID,
		RecipientID:  req.RecipientID,
		ProviderName: provider,
		EventType:    eventType,
		MessageHash:  contentHash,
		Metadata:     clean,
	})
}

func verdictEventType(v dignity.Verdict) string {
	switch v {
	case dignity.Coalesced:
		return audit.TypeCoalesced
	case dignity.Deferred:
		return audit.TypeDeferred
	default:
		return audit.TypeSuppressed
	}
}
