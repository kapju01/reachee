// Package ddb keeps one escalation in the DynamoDB table: the request that
// raised it at seq 0, and its signed chain at seq 1 onward.
//
// A Sink is scoped to exactly one escalation_id. audit.NewLogger reads All()
// to find the previous hash, so a sink spanning escalations would chain
// unrelated events together and the hash chain would prove nothing about any
// one of them.
//
// The request record holds the subject and payload, because attempt runs in a
// later invocation and has to send them. It is never part of the chain, and
// the chain never holds them: events carry only the content hash.
package ddb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/aisolutions/reachee/internal/audit"
)

// Client is the slice of the DynamoDB API this package uses, matching the
// actions the execution roles grant.
type Client interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, optFns ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

// ErrNotFound means no request record exists for the escalation.
var ErrNotFound = errors.New("ddb: escalation not found")

// requestSeq is the sort key of the request record. Chain events start at 1,
// so a query for seq >= 1 can never return it.
const requestSeq = 0

// Request is what raise accepted and attempt needs to send.
type Request struct {
	EscalationID      string
	AgentID           string
	RecipientID       string
	CorrelationID     string
	Priority          string
	Subject           string
	Payload           string
	RequestedResponse string
	CreatedAt         time.Time
}

func key(escalationID string, seq int) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"escalation_id": &types.AttributeValueMemberS{Value: escalationID},
		"seq":           &types.AttributeValueMemberN{Value: strconv.Itoa(seq)},
	}
}

// PutRequest writes the request record. It refuses to overwrite: an
// escalation is raised once.
func PutRequest(ctx context.Context, c Client, table string, r Request) error {
	item := key(r.EscalationID, requestSeq)
	item["kind"] = &types.AttributeValueMemberS{Value: "request"}
	for name, v := range map[string]string{
		"agent_id":           r.AgentID,
		"recipient_id":       r.RecipientID,
		"correlation_id":     r.CorrelationID,
		"priority":           r.Priority,
		"subject":            r.Subject,
		"payload":            r.Payload,
		"requested_response": r.RequestedResponse,
		"created_at":         r.CreatedAt.UTC().Format(time.RFC3339Nano),
	} {
		item[name] = &types.AttributeValueMemberS{Value: v}
	}
	_, err := c.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(seq)"),
	})
	if err != nil {
		return fmt.Errorf("ddb: put request %s: %w", r.EscalationID, err)
	}
	return nil
}

// GetRequest reads the request record with a consistent read, since attempt
// may run milliseconds after raise wrote it.
func GetRequest(ctx context.Context, c Client, table, escalationID string) (Request, error) {
	out, err := c.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(table),
		Key:            key(escalationID, requestSeq),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return Request{}, fmt.Errorf("ddb: get request %s: %w", escalationID, err)
	}
	if out == nil || len(out.Item) == 0 {
		return Request{}, fmt.Errorf("%w: %s", ErrNotFound, escalationID)
	}
	s := func(name string) string {
		if v, ok := out.Item[name].(*types.AttributeValueMemberS); ok {
			return v.Value
		}
		return ""
	}
	created, _ := time.Parse(time.RFC3339Nano, s("created_at"))
	return Request{
		EscalationID:      escalationID,
		AgentID:           s("agent_id"),
		RecipientID:       s("recipient_id"),
		CorrelationID:     s("correlation_id"),
		Priority:          s("priority"),
		Subject:           s("subject"),
		Payload:           s("payload"),
		RequestedResponse: s("requested_response"),
		CreatedAt:         created,
	}, nil
}

// Sink is an audit.Sink over one escalation's chain.
type Sink struct {
	mu           sync.Mutex
	client       Client
	table        string
	escalationID string
	events       []audit.Event
}

// NewSink loads the escalation's existing chain so audit.NewLogger can find
// its head. The read is consistent: a stale head would fork the chain.
func NewSink(ctx context.Context, c Client, table, escalationID string) (*Sink, error) {
	if escalationID == "" {
		return nil, errors.New("ddb: sink needs an escalation id")
	}
	s := &Sink{client: c, table: table, escalationID: escalationID}
	var start map[string]types.AttributeValue
	for {
		out, err := c.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(table),
			KeyConditionExpression: aws.String("escalation_id = :e AND seq >= :first"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":e":     &types.AttributeValueMemberS{Value: escalationID},
				":first": &types.AttributeValueMemberN{Value: "1"},
			},
			ConsistentRead:    aws.Bool(true),
			ExclusiveStartKey: start,
		})
		if err != nil {
			return nil, fmt.Errorf("ddb: load chain %s: %w", escalationID, err)
		}
		for _, item := range out.Items {
			raw, ok := item["event"].(*types.AttributeValueMemberS)
			if !ok {
				return nil, fmt.Errorf("ddb: chain %s has an item with no event", escalationID)
			}
			var e audit.Event
			if err := json.Unmarshal([]byte(raw.Value), &e); err != nil {
				return nil, fmt.Errorf("ddb: chain %s: %w", escalationID, err)
			}
			s.events = append(s.events, e)
		}
		if len(out.LastEvaluatedKey) == 0 {
			return s, nil
		}
		start = out.LastEvaluatedKey
	}
}

// Append implements audit.Sink. The write is conditional on the sequence
// number being free, so two writers racing on one escalation fail loudly
// instead of forking the chain.
func (s *Sink) Append(e audit.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	seq := len(s.events) + 1
	item := key(s.escalationID, seq)
	item["kind"] = &types.AttributeValueMemberS{Value: "event"}
	item["event_type"] = &types.AttributeValueMemberS{Value: e.EventType}
	item["event"] = &types.AttributeValueMemberS{Value: string(body)}
	_, err = s.client.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName:           aws.String(s.table),
		Item:                item,
		ConditionExpression: aws.String("attribute_not_exists(seq)"),
	})
	if err != nil {
		return fmt.Errorf("ddb: append %s seq %d: %w", s.escalationID, seq, err)
	}
	s.events = append(s.events, e)
	return nil
}

// All implements audit.Sink.
func (s *Sink) All() []audit.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]audit.Event, len(s.events))
	copy(out, s.events)
	return out
}
