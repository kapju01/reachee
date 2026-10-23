package ddb

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// replySeq is the sort key of the reply slot: below the request record, so
// no chain query can ever return it.
const replySeq = -1

// The ways a reply slot refuses a change. Each maps to something the person
// is told plainly.
var (
	ErrNotArmed      = errors.New("ddb: escalation is not holding for an answer yet")
	ErrSpent         = errors.New("ddb: this escalation has already been answered")
	ErrClosed        = errors.New("ddb: the answer window is closed")
	ErrNonceMismatch = errors.New("ddb: link has been superseded")
)

// Reply is an escalation's answer slot. attempt mints the nonce before the
// message goes out, reply arms it with the task token once the execution is
// holding, and the first valid POST spends it. Answer is the person's own
// words; like the payload it stays here and only its hash enters the chain.
type Reply struct {
	EscalationID string
	Nonce        string
	ExpiresAt    time.Time
	TaskToken    string
	ArmedAt      time.Time
	SpentAt      time.Time
	Decision     string
	Answer       string
	ClosedAt     time.Time
	ClosedReason string
	version      int
}

// GetReply reads the slot consistently.
func GetReply(ctx context.Context, c Client, table, escalationID string) (Reply, error) {
	out, err := c.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(table),
		Key:            key(escalationID, replySeq),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return Reply{}, fmt.Errorf("ddb: get reply %s: %w", escalationID, err)
	}
	if out == nil || len(out.Item) == 0 {
		return Reply{}, fmt.Errorf("%w: %s", ErrNotFound, escalationID)
	}
	s := func(name string) string {
		if v, ok := out.Item[name].(*types.AttributeValueMemberS); ok {
			return v.Value
		}
		return ""
	}
	t := func(name string) time.Time {
		v, _ := time.Parse(time.RFC3339Nano, s(name))
		return v
	}
	version := 0
	if v, ok := out.Item["version"].(*types.AttributeValueMemberN); ok {
		version, _ = strconv.Atoi(v.Value)
	}
	return Reply{
		EscalationID: escalationID,
		Nonce:        s("nonce"),
		ExpiresAt:    t("expires_at"),
		TaskToken:    s("task_token"),
		ArmedAt:      t("armed_at"),
		SpentAt:      t("spent_at"),
		Decision:     s("decision"),
		Answer:       s("answer"),
		ClosedAt:     t("closed_at"),
		ClosedReason: s("closed_reason"),
		version:      version,
	}, nil
}

// put writes the slot only if nobody else has since the read, so every
// transition below is atomic without a server-side update expression.
func put(ctx context.Context, c Client, table string, r Reply) error {
	item := key(r.EscalationID, replySeq)
	item["kind"] = &types.AttributeValueMemberS{Value: "reply"}
	item["version"] = &types.AttributeValueMemberN{Value: strconv.Itoa(r.version + 1)}
	for name, v := range map[string]string{
		"nonce":         r.Nonce,
		"task_token":    r.TaskToken,
		"decision":      r.Decision,
		"answer":        r.Answer,
		"closed_reason": r.ClosedReason,
	} {
		if v != "" {
			item[name] = &types.AttributeValueMemberS{Value: v}
		}
	}
	for name, v := range map[string]time.Time{
		"expires_at": r.ExpiresAt, "armed_at": r.ArmedAt, "spent_at": r.SpentAt, "closed_at": r.ClosedAt,
	} {
		if !v.IsZero() {
			item[name] = &types.AttributeValueMemberS{Value: v.UTC().Format(time.RFC3339Nano)}
		}
	}
	in := &dynamodb.PutItemInput{TableName: aws.String(table), Item: item}
	if r.version == 0 {
		in.ConditionExpression = aws.String("attribute_not_exists(seq)")
	} else {
		in.ConditionExpression = aws.String("version = :v")
		in.ExpressionAttributeValues = map[string]types.AttributeValue{
			":v": &types.AttributeValueMemberN{Value: strconv.Itoa(r.version)},
		}
	}
	_, err := c.PutItem(ctx, in)
	return err
}

func conditionFailed(err error) bool {
	var cf *types.ConditionalCheckFailedException
	return errors.As(err, &cf)
}

// transition reads the slot, applies change, and writes it back, re-reading
// if another writer got there first. A lost race is re-evaluated, never
// overwritten: two POSTs on one link yield one answer and one ErrSpent.
func transition(ctx context.Context, c Client, table, escalationID string, allowMissing bool, change func(*Reply) error) (Reply, error) {
	for range 3 {
		r, err := GetReply(ctx, c, table, escalationID)
		switch {
		case errors.Is(err, ErrNotFound) && allowMissing:
			r = Reply{EscalationID: escalationID}
		case err != nil:
			return Reply{}, err
		}
		if err := change(&r); err != nil {
			return r, err
		}
		err = put(ctx, c, table, r)
		if err == nil {
			r.version++
			return r, nil
		}
		if !conditionFailed(err) {
			return Reply{}, fmt.Errorf("ddb: write reply %s: %w", escalationID, err)
		}
	}
	return Reply{}, fmt.Errorf("ddb: reply %s kept changing under us", escalationID)
}

// MintReply opens the slot for a tier about to send, replacing whatever an
// earlier tier minted. A link from a tier that never delivered was never
// seen by anyone, so superseding it costs nothing.
func MintReply(ctx context.Context, c Client, table, escalationID, nonce string, expires time.Time) error {
	_, err := transition(ctx, c, table, escalationID, true, func(r *Reply) error {
		if !r.ArmedAt.IsZero() {
			return errors.New("ddb: cannot mint over an armed reply")
		}
		*r = Reply{EscalationID: escalationID, Nonce: nonce, ExpiresAt: expires, version: r.version}
		return nil
	})
	return err
}

// ArmReply stores the task token, which only now exists. It refuses a slot
// with no nonce: a hold with no link sent is a hold nobody can answer.
func ArmReply(ctx context.Context, c Client, table, escalationID, taskToken string, now time.Time) error {
	_, err := transition(ctx, c, table, escalationID, false, func(r *Reply) error {
		switch {
		case r.Nonce == "":
			return errors.New("ddb: no link was minted for this escalation")
		case !r.ClosedAt.IsZero():
			return ErrClosed
		case !r.ArmedAt.IsZero():
			return errors.New("ddb: reply is already armed")
		}
		r.TaskToken = taskToken
		r.ArmedAt = now
		return nil
	})
	return err
}

// SpendReply marks the nonce spent and records the answer, returning the slot
// with its task token. It checks readiness before spending, so a person who
// answers before the execution is holding keeps a working link.
func SpendReply(ctx context.Context, c Client, table, escalationID, nonce, decision, answer string, now time.Time) (Reply, error) {
	return transition(ctx, c, table, escalationID, false, func(r *Reply) error {
		switch {
		case r.Nonce != nonce:
			return ErrNonceMismatch
		case !r.SpentAt.IsZero():
			return ErrSpent
		case !r.ClosedAt.IsZero():
			return ErrClosed
		case r.TaskToken == "":
			return ErrNotArmed
		}
		r.SpentAt = now
		r.Decision = decision
		r.Answer = answer
		return nil
	})
}

// CloseReply ends the window, so a link clicked after a timeout is refused
// instead of producing receipts for an execution that has stopped waiting.
func CloseReply(ctx context.Context, c Client, table, escalationID, reason string, now time.Time) error {
	_, err := transition(ctx, c, table, escalationID, true, func(r *Reply) error {
		if !r.ClosedAt.IsZero() {
			return nil
		}
		r.ClosedAt = now
		r.ClosedReason = reason
		return nil
	})
	return err
}
