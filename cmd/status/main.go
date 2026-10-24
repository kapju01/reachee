// Command status is the Lambda behind GET /escalation/{id}. It reads one
// escalation's chain and returns what the chain proves: which receipts were
// written, when, by which provider, what was decided, and whether the held
// action was released.
//
// Anyone holding the id can call it. That is deliberate: the chain carries
// hashes, never content, and it is meant to be checked by someone we do not
// control. The request record and the reply slot, which do hold the payload,
// the subject, the note, the nonce, and the task token, are outside what this
// function reads and outside what its role may read.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/aisolutions/reachee/internal/audit"
	"github.com/aisolutions/reachee/internal/config"
	"github.com/aisolutions/reachee/internal/ddb"
)

// escalationID matches the UUIDv7 raise issues. Anything else is refused
// before it costs a read.
var escalationID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type reader struct {
	db    ddb.Querier
	table string
	// pub is the published verifying key, carried into a bundle so the
	// bundle is self-describing. A verifier should still check against the
	// published copy with --pubkey, not against this one.
	pub ed25519.PublicKey
}

// receipt is one chain event as the summary shows it. Metadata is left out:
// it is signed and travels in full in the bundle, and the summary answers
// only what happened, when, and through whom.
type receipt struct {
	EventID      string    `json:"event_id"`
	EventType    string    `json:"event_type"`
	Timestamp    time.Time `json:"timestamp"`
	ProviderName string    `json:"provider_name"`
	MessageHash  string    `json:"message_hash"`
	PrevHash     string    `json:"prev_hash"`
	Hash         string    `json:"hash"`
}

// summary is the default response. Decision comes from the ACKNOWLEDGED
// receipt and Released from the terminal one, so every field is something the
// chain itself proves rather than something read from mutable state.
type summary struct {
	EscalationID string    `json:"escalation_id"`
	Decision     string    `json:"decision,omitempty"`
	Outcome      string    `json:"outcome,omitempty"`
	Released     bool      `json:"released"`
	ChainHead    string    `json:"chain_head"`
	Receipts     []receipt `json:"receipts"`
}

type failure struct {
	Error string `json:"error"`
}

func (r *reader) handle(ctx context.Context, ev events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	id := ev.PathParameters["id"]
	if !escalationID.MatchString(id) {
		return respondJSON(http.StatusBadRequest, failure{"not an escalation id"})
	}
	format := ev.QueryStringParameters["format"]
	if format != "" && format != "bundle" {
		return respondJSON(http.StatusBadRequest, failure{`format must be "bundle" or absent`})
	}
	chain, err := ddb.ReadChain(ctx, r.db, r.table, id)
	if err != nil {
		log.Printf("status: %v", err)
		return respondJSON(http.StatusBadGateway, failure{"chain unavailable"})
	}
	if len(chain) == 0 {
		return respondJSON(http.StatusNotFound, failure{"no escalation with that id"})
	}
	if format == "bundle" {
		body, err := audit.MarshalBundle(audit.Export(chain, r.pub))
		if err != nil {
			return events.APIGatewayV2HTTPResponse{}, err
		}
		return respond(http.StatusOK, body)
	}
	s, err := summarize(id, chain)
	if err != nil {
		return events.APIGatewayV2HTTPResponse{}, err
	}
	return respondJSON(http.StatusOK, s)
}

func summarize(id string, chain []audit.Event) (summary, error) {
	s := summary{EscalationID: id, Receipts: make([]receipt, 0, len(chain))}
	for _, e := range chain {
		h, err := audit.SignedHash(e)
		if err != nil {
			return summary{}, err
		}
		s.Receipts = append(s.Receipts, receipt{
			EventID: e.EventID, EventType: e.EventType, Timestamp: e.Timestamp,
			ProviderName: e.ProviderName, MessageHash: e.MessageHash, PrevHash: e.PrevHash, Hash: h,
		})
		s.ChainHead = h
		switch e.EventType {
		case audit.TypeAcknowledged:
			s.Decision = e.Metadata["intent"]
		case audit.TypeResolved, audit.TypeDenied, audit.TypeTimedOut:
			s.Outcome = e.EventType
			s.Released = e.EventType == audit.TypeResolved && e.Metadata["released"] == "true"
		case audit.TypeFailed:
			// A tier's FAILED is not an outcome; the walk goes on. A stage
			// that failed after its retries ends the hold, and says so with
			// released.
			if _, ok := e.Metadata["released"]; ok {
				s.Outcome = e.EventType
				s.Released = false
			}
		}
	}
	return s, nil
}

func respondJSON(status int, v any) (events.APIGatewayV2HTTPResponse, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return events.APIGatewayV2HTTPResponse{}, err
	}
	return respond(status, b)
}

// The chain changes while an escalation is open, so nothing may cache it.
func respond(status int, body []byte) (events.APIGatewayV2HTTPResponse, error) {
	return events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers:    map[string]string{"content-type": "application/json", "cache-control": "no-store"},
		Body:       string(body),
	}, nil
}

func main() {
	r, err := initialize(context.Background())
	if err != nil {
		log.Fatalf("status: init: %v", err)
	}
	lambda.Start(r.handle)
}

// initialize runs once per execution environment. status signs nothing, so it
// holds no private key and its role cannot read the secret.
func initialize(ctx context.Context) (*reader, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	pub, err := parsePublicKey(os.Getenv("REACHEE_PUBLIC_KEY"))
	if err != nil {
		return nil, err
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}
	return &reader{db: dynamodb.NewFromConfig(awsCfg), table: cfg.AuditTableName, pub: pub}, nil
}

func parsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("REACHEE_PUBLIC_KEY must be a 32-byte hex Ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}
