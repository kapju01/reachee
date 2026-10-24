// Command attempt is the Lambda the state machine invokes to walk exactly one
// tier of the Heart-Map ladder. The state machine owns the order and the
// waiting; attempt owns one rung and the receipt that says what it did.
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log"
	"net/url"
	"time"
	_ "time/tzdata" // provided.al2023 ships no zoneinfo; quiet hours need it

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"github.com/aisolutions/reachee/internal/audit"
	"github.com/aisolutions/reachee/internal/circuit"
	"github.com/aisolutions/reachee/internal/config"
	"github.com/aisolutions/reachee/internal/ddb"
	"github.com/aisolutions/reachee/internal/dignity"
	"github.com/aisolutions/reachee/internal/dispatcher"
	"github.com/aisolutions/reachee/internal/empathy"
	"github.com/aisolutions/reachee/internal/heartmap"
	"github.com/aisolutions/reachee/internal/providers"
	"github.com/aisolutions/reachee/internal/providers/ses"
	"github.com/aisolutions/reachee/internal/replytoken"
	"github.com/aisolutions/reachee/internal/signing"
)

// signingKey is fetched once, at init, and reused by every invocation this
// execution environment serves.
var signingKey ed25519.PrivateKey

// attempter holds what outlives one invocation. The dignity pipeline and the
// breakers live here so that coalescing, the attention budget, and an open
// breaker carry across warm invocations. They do not survive a cold start or
// span concurrent environments; that gap is stated in the README.
type attempter struct {
	db         ddb.Client
	table      string
	policies   heartmap.Store
	key        ed25519.PrivateKey
	dignity    *dignity.Pipeline
	breakers   *circuit.Registry
	translator *empathy.Translator
	providers  map[string]providers.Messenger
	now        func() time.Time

	// replyKey signs the answer link; replyURL is the GET /reply endpoint.
	replyKey []byte
	replyURL string
}

type attemptInput struct {
	Action       string `json:"action"`
	EscalationID string `json:"escalation_id"`
	Tier         int    `json:"tier"`
}

// attemptOutput is what the state machine routes on. TimeoutSeconds is the
// walked tier's declared timeout, which AwaitAnswer reads through
// TimeoutSecondsPath, so the window comes from the Heart-Map and not from
// anything a caller supplied. NextTier is absent when there is nowhere left
// to go, or when the tier delivered.
type attemptOutput struct {
	Status         string `json:"status"`
	NextTier       *int   `json:"next_tier,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (a *attempter) handle(ctx context.Context, in attemptInput) (attemptOutput, error) {
	if in.Action != "attempt" {
		return attemptOutput{}, fmt.Errorf("attempt: unsupported action %q", in.Action)
	}
	stored, err := ddb.GetRequest(ctx, a.db, a.table, in.EscalationID)
	if err != nil {
		return attemptOutput{}, err
	}
	policy, err := a.policies.Resolve(stored.RecipientID)
	if err != nil {
		return attemptOutput{}, err
	}
	idx := -1
	for i, r := range policy.Ladder {
		if r.Tier == in.Tier {
			idx = i
			break
		}
	}
	if idx < 0 {
		return attemptOutput{}, fmt.Errorf("attempt: tier %d is not on the ladder for %s", in.Tier, policy.ID)
	}
	rung := policy.Ladder[idx]
	out := attemptOutput{TimeoutSeconds: int(rung.Timeout / time.Second)}

	sink, err := ddb.NewSink(ctx, a.db, a.table, in.EscalationID)
	if err != nil {
		return attemptOutput{}, err
	}
	d := &dispatcher.Dispatcher{
		Policies:   a.policies,
		Dignity:    a.dignity,
		Breakers:   a.breakers,
		Translator: a.translator,
		Audit:      audit.NewLogger(a.key, sink, a.now),
		Providers:  a.providers,
		Now:        a.now,
	}
	req := dispatcher.Request{
		RequestID:         stored.EscalationID,
		AgentID:           stored.AgentID,
		RecipientID:       stored.RecipientID,
		CorrelationID:     stored.CorrelationID,
		Priority:          providers.Priority(stored.Priority),
		Subject:           stored.Subject,
		Payload:           stored.Payload,
		RequestedResponse: stored.RequestedResponse,
	}

	// The attention gates run once, before the first tier. A later tier is
	// the same event failing over, not a new claim on the person's attention.
	if idx == 0 {
		decision, _, err := d.Gate(req, policy)
		if err != nil {
			return attemptOutput{}, err
		}
		if !decision.Delivers() {
			out.Status = string(decision.Verdict)
			return out, nil
		}
	}

	link, expires, err := a.mintLink(ctx, stored.EscalationID, stored.RecipientID, rung.Timeout)
	if err != nil {
		return attemptOutput{}, err
	}
	translated := d.Translate(ctx, req, policy)
	translated.Body += "\n\nRequested response: " + req.RequestedResponse +
		"\n\nAnswer here: " + link +
		"\nThe link records one answer and expires at " + expires.In(policy.Location()).Format("15:04 MST") + "."

	res, err := d.AttemptTier(ctx, req, policy, rung, translated)
	if err != nil {
		return attemptOutput{}, err
	}
	out.Status = res.Status
	if res.Status == dispatcher.StatusSent {
		return out, nil
	}
	if idx+1 < len(policy.Ladder) {
		next := policy.Ladder[idx+1].Tier
		out.NextTier = &next
		return out, nil
	}
	if _, err := d.Exhausted(req); err != nil {
		return attemptOutput{}, err
	}
	return out, nil
}

// mintLink opens the reply slot and returns the link the message carries.
// The expiry is this tier's declared timeout, measured from before the send,
// so the link always dies no later than the execution stops waiting.
func (a *attempter) mintLink(ctx context.Context, escalationID, recipientID string, window time.Duration) (string, time.Time, error) {
	nonce, err := replytoken.NewNonce()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := a.now().Add(window)
	if err := ddb.MintReply(ctx, a.db, a.table, escalationID, nonce, expires); err != nil {
		return "", time.Time{}, err
	}
	token, err := replytoken.Mint(a.replyKey, replytoken.Claims{
		EscalationID: escalationID,
		RecipientID:  recipientID,
		Expiry:       expires.Unix(),
		Nonce:        nonce,
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return a.replyURL + "?t=" + url.QueryEscape(token), expires, nil
}

func main() {
	a, err := initialize(context.Background())
	if err != nil {
		log.Fatalf("attempt: init: %v", err)
	}
	lambda.Start(a.handle)
}

// initialize runs once per execution environment, in the Lambda init phase.
// Only email has a provider. WhatsApp and SMS are on the ladder and
// deliberately unregistered, so their tiers break their promise out loud.
func initialize(ctx context.Context) (*attempter, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.Sender == "" {
		return nil, errors.New("REACHEE_SENDER is not set")
	}
	if cfg.ReplyURL == "" {
		return nil, errors.New("REACHEE_REPLY_URL is not set")
	}
	policy, err := heartmap.Load(cfg.HeartMapDoc)
	if err != nil {
		return nil, fmt.Errorf("heart-map: %w", err)
	}
	store := heartmap.NewMemoryStore()
	store.Put(policy)

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	signingKey, err = signing.Load(ctx, secretsmanager.NewFromConfig(awsCfg), cfg.SigningKeyPath)
	if err != nil {
		return nil, err
	}
	email, err := ses.New(sesv2.NewFromConfig(awsCfg), cfg.Sender)
	if err != nil {
		return nil, err
	}
	return &attempter{
		db:         dynamodb.NewFromConfig(awsCfg),
		table:      cfg.AuditTableName,
		policies:   store,
		key:        signingKey,
		dignity:    dignity.New(nil),
		breakers:   circuit.NewRegistry(circuit.DefaultConfig(), nil),
		translator: empathy.NewTranslator(nil, cfg.EmpathyTimeout),
		providers:  map[string]providers.Messenger{heartmap.ChannelEmail: email},
		now:        time.Now,
		replyKey:   replytoken.KeyFrom(signingKey),
		replyURL:   cfg.ReplyURL,
	}, nil
}
