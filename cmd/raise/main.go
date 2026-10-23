// Command raise is the Lambda behind POST /raise. It records what Reachee
// promises, starts the execution that keeps the promise, and returns. It
// never touches a provider: the calling agent gets a receipt, not a wait.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
	_ "time/tzdata" // provided.al2023 ships no zoneinfo; quiet hours need it

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/sfn"

	"github.com/aisolutions/reachee/internal/audit"
	"github.com/aisolutions/reachee/internal/config"
	"github.com/aisolutions/reachee/internal/ddb"
	"github.com/aisolutions/reachee/internal/heartmap"
	"github.com/aisolutions/reachee/internal/providers"
	"github.com/aisolutions/reachee/internal/signing"
)

// signingKey is fetched once, at init, and reused by every invocation this
// execution environment serves.
var signingKey ed25519.PrivateKey

// maxBody bounds what one escalation may carry. It is far above a decision's
// worth of text and far below a DynamoDB item.
const maxBody = 32 << 10

// promise is Reachee's own commitment, stated from its own capability. It
// does not and cannot promise that a person will answer.
const promise = "attempt delivery on the declared ladder in order and report each outcome"

// Starter is the one Step Functions call raise makes.
type Starter interface {
	StartExecution(ctx context.Context, in *sfn.StartExecutionInput, optFns ...func(*sfn.Options)) (*sfn.StartExecutionOutput, error)
}

type raiser struct {
	db           ddb.Client
	sfn          Starter
	table        string
	stateMachine string
	policies     heartmap.Store
	key          ed25519.PrivateKey
	now          func() time.Time
}

type raiseRequest struct {
	AgentID           string `json:"agent_id"`
	RecipientID       string `json:"recipient_id"`
	CorrelationID     string `json:"correlation_id"`
	Priority          string `json:"priority"`
	Subject           string `json:"subject"`
	Payload           string `json:"payload"`
	RequestedResponse string `json:"requested_response"`
}

type raiseResponse struct {
	EscalationID string `json:"escalation_id,omitempty"`
	ReceiptHash  string `json:"receipt_hash,omitempty"`
	ChainHead    string `json:"chain_head,omitempty"`
	Error        string `json:"error,omitempty"`
}

// executionInput is the whole of what the state machine is told. Subject and
// payload stay in the request record, out of the execution history.
type executionInput struct {
	EscalationID string `json:"escalation_id"`
	Tier         int    `json:"tier"`
}

func (r raiseRequest) validate() error {
	var missing []string
	for name, v := range map[string]string{
		"agent_id": r.AgentID, "recipient_id": r.RecipientID, "priority": r.Priority,
		"subject": r.Subject, "payload": r.Payload, "requested_response": r.RequestedResponse,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
	}
	switch providers.Priority(r.Priority) {
	case providers.P0, providers.P1, providers.P2, providers.P3:
	default:
		return fmt.Errorf("priority must be one of P0, P1, P2, P3")
	}
	return nil
}

func (h *raiser) handle(ctx context.Context, ev events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	body := ev.Body
	if ev.IsBase64Encoded {
		b, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return respond(http.StatusBadRequest, raiseResponse{Error: "body is not valid base64"})
		}
		body = string(b)
	}
	if len(body) > maxBody {
		return respond(http.StatusRequestEntityTooLarge, raiseResponse{Error: fmt.Sprintf("body exceeds %d bytes", maxBody)})
	}

	var req raiseRequest
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return respond(http.StatusBadRequest, raiseResponse{Error: "body is not a valid escalation: " + err.Error()})
	}
	if err := req.validate(); err != nil {
		return respond(http.StatusBadRequest, raiseResponse{Error: err.Error()})
	}

	policy, err := h.policies.Resolve(req.RecipientID)
	if err != nil {
		return respond(http.StatusUnprocessableEntity, raiseResponse{Error: "no Heart-Map policy for recipient_id"})
	}

	id := audit.NewEventID()
	if err := ddb.PutRequest(ctx, h.db, h.table, ddb.Request{
		EscalationID:      id,
		AgentID:           req.AgentID,
		RecipientID:       req.RecipientID,
		CorrelationID:     req.CorrelationID,
		Priority:          req.Priority,
		Subject:           req.Subject,
		Payload:           req.Payload,
		RequestedResponse: req.RequestedResponse,
		CreatedAt:         h.now(),
	}); err != nil {
		return respond(http.StatusInternalServerError, raiseResponse{Error: "escalation not recorded; nothing was promised"})
	}

	sink, err := ddb.NewSink(ctx, h.db, h.table, id)
	if err != nil {
		return respond(http.StatusInternalServerError, raiseResponse{EscalationID: id, Error: "chain unavailable; nothing was promised"})
	}
	logger := audit.NewLogger(h.key, sink, h.now)
	meta := map[string]string{
		"agent_id": req.AgentID,
		"priority": req.Priority,
		"promise":  promise,
	}
	if req.CorrelationID != "" {
		meta["correlation_id"] = req.CorrelationID
	}
	promised, err := logger.Emit(audit.Event{
		RequestID:   id,
		RecipientID: req.RecipientID,
		EventType:   audit.TypePromised,
		MessageHash: audit.HashContent(req.Payload),
		Metadata:    meta,
	})
	if err != nil {
		return respond(http.StatusInternalServerError, raiseResponse{EscalationID: id, Error: "promise not recorded; nothing was started"})
	}

	input, _ := json.Marshal(executionInput{EscalationID: id, Tier: policy.Ladder[0].Tier})
	_, startErr := h.sfn.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: aws.String(h.stateMachine),
		Name:            aws.String(id),
		Input:           aws.String(string(input)),
	})
	if startErr != nil {
		// The promise was made and cannot be kept. Say so in the chain and to
		// the caller, rather than leaving a PROMISED receipt nothing will act on.
		failed, err := logger.Emit(audit.Event{
			RequestID:   id,
			RecipientID: req.RecipientID,
			EventType:   audit.TypeFailed,
			MessageHash: audit.HashContent(req.Payload),
			Metadata:    map[string]string{"reason": "promise broken: execution did not start"},
		})
		if err != nil {
			return respond(http.StatusInternalServerError, raiseResponse{EscalationID: id, Error: "execution did not start and the failure could not be recorded"})
		}
		head, _ := audit.SignedHash(failed)
		return respond(http.StatusBadGateway, raiseResponse{EscalationID: id, ReceiptHash: head, ChainHead: head, Error: "execution did not start; the broken promise is recorded"})
	}

	// At this point the PROMISED receipt is the whole chain, so its hash and
	// the chain head are the same value. They are returned separately because
	// they answer different questions: which receipt is mine, and where does
	// the chain I will later be shown have to start from.
	hash, err := audit.SignedHash(promised)
	if err != nil {
		return respond(http.StatusInternalServerError, raiseResponse{EscalationID: id, Error: "receipt hash unavailable"})
	}
	all := sink.All()
	head, err := audit.SignedHash(all[len(all)-1])
	if err != nil {
		return respond(http.StatusInternalServerError, raiseResponse{EscalationID: id, Error: "chain head unavailable"})
	}
	return respond(http.StatusAccepted, raiseResponse{EscalationID: id, ReceiptHash: hash, ChainHead: head})
}

func respond(status int, body raiseResponse) (events.APIGatewayV2HTTPResponse, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return events.APIGatewayV2HTTPResponse{}, err
	}
	return events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers:    map[string]string{"content-type": "application/json"},
		Body:       string(b),
	}, nil
}

func main() {
	h, err := initialize(context.Background())
	if err != nil {
		log.Fatalf("raise: init: %v", err)
	}
	lambda.Start(h.handle)
}

// initialize runs once per execution environment, in the Lambda init phase.
// Any failure here stops the function before it can serve a request, which
// is the loud failure a missing key or policy deserves.
func initialize(ctx context.Context) (*raiser, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.StateMachineARN == "" {
		return nil, errors.New("REACHEE_STATE_MACHINE is not set")
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
	return &raiser{
		db:           dynamodb.NewFromConfig(awsCfg),
		sfn:          sfn.NewFromConfig(awsCfg),
		table:        cfg.AuditTableName,
		stateMachine: cfg.StateMachineARN,
		policies:     store,
		key:          signingKey,
		now:          time.Now,
	}, nil
}
