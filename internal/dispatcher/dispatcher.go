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

// Dispatch runs one escalation end to end.
func (d *Dispatcher) Dispatch(ctx context.Context, req Request) (Receipt, error) {
	policy, err := d.Policies.Resolve(req.RecipientID)
	if err != nil {
		return Receipt{}, err
	}

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
		if err != nil {
			return Receipt{}, err
		}
		return Receipt{
			RequestID: req.RequestID,
			ReceiptID: ev.EventID,
			Verdict:   decision.Verdict,
			Reason:    decision.Reason,
			Signature: ev.Signature,
		}, nil
	}

	if decision.Verdict == dignity.Bypassed {
		if _, err := d.emit(req, "", audit.TypeBypassed, contentHash, map[string]string{
			"reason": decision.Reason,
		}); err != nil {
			return Receipt{}, err
		}
	}

	translated := d.Translator.Translate(ctx, empathy.Request{
		RecipientName: policy.DisplayName,
		Subject:       req.Subject,
		RawContent:    req.Payload,
		Priority:      string(req.Priority),
	})

	var lastErr error
	for _, rung := range policy.Ladder {
		provider, ok := d.Providers[rung.Channel]
		if !ok {
			lastErr = fmt.Errorf("dispatcher: no provider registered for channel %q", rung.Channel)
			continue
		}
		address, ok := policy.Address(rung.Channel)
		if !ok {
			lastErr = fmt.Errorf("dispatcher: contact %s has no address on channel %q", policy.ID, rung.Channel)
			continue
		}

		breaker := d.Breakers.For(provider.Name())
		if !breaker.Allow() {
			if _, err := d.emit(req, provider.Name(), audit.TypeEscalated, contentHash, map[string]string{
				"tier":          strconv.Itoa(rung.Tier),
				"channel":       rung.Channel,
				"reason":        "circuit breaker is " + string(breaker.State()) + ", tier skipped",
				"breaker_state": string(breaker.State()),
			}); err != nil {
				return Receipt{}, err
			}
			lastErr = fmt.Errorf("dispatcher: %s breaker %s", provider.Name(), breaker.State())
			continue
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
			if _, err := d.emit(req, provider.Name(), audit.TypeFailed, contentHash, map[string]string{
				"tier":          strconv.Itoa(rung.Tier),
				"channel":       rung.Channel,
				"reason":        "promise broken by provider: " + sendErr.Error(),
				"breaker_state": string(state),
			}); err != nil {
				return Receipt{}, err
			}
			lastErr = sendErr
			continue
		}

		breaker.Success()
		ev, err := d.emit(req, provider.Name(), audit.TypeSent, contentHash, map[string]string{
			"tier":            strconv.Itoa(rung.Tier),
			"channel":         rung.Channel,
			"provider_msg_id": receipt.ProviderMsgID,
			"empathy_source":  string(translated.Source),
			"empathy_reason":  translated.Reason,
			"agent_id":        req.AgentID,
			"agent_autonomy":  req.AgentAutonomy,
		})
		if err != nil {
			return Receipt{}, err
		}
		hash, err := audit.SignedHash(ev)
		if err != nil {
			return Receipt{}, err
		}
		return Receipt{
			RequestID:          req.RequestID,
			ReceiptID:          ev.EventID,
			Delivered:          true,
			DeliveredVia:       provider.Name(),
			DeliveredAt:        receipt.AcceptedAt,
			ExpectedResponseBy: d.now().Add(rung.Timeout),
			Verdict:            decision.Verdict,
			Reason:             decision.Reason,
			EmpathySource:      translated.Source,
			ReceiptHash:        hash,
			Signature:          ev.Signature,
		}, nil
	}

	if _, err := d.emit(req, "", audit.TypeFailed, contentHash, map[string]string{
		"reason": "ladder exhausted without delivery",
	}); err != nil {
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
