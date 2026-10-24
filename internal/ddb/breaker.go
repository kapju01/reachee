package ddb

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/aisolutions/reachee/internal/circuit"
)

// BreakerPrefix marks a reserved partition. Escalation ids are UUIDv7, which
// never begin with "#", so a breaker record can never share a partition with
// an escalation, and the status role's partition-key pattern cannot match it.
const BreakerPrefix = "#breaker/"

// breakerSeq is where a breaker record sits inside its own partition.
const breakerSeq = 0

// BreakerStore keeps circuit breaker positions in the escalations table, one
// partition per breaker, so a breaker opened by one invocation is open for
// the next whether or not it lands on the same execution environment.
type BreakerStore struct {
	Client Client
	Table  string
	Now    func() time.Time
}

// Load implements circuit.Store. The read is consistent: a stale position is
// how a breaker that just opened would let the next escalation through.
func (s BreakerStore) Load(ctx context.Context, name string) (circuit.Snapshot, int64, error) {
	out, err := s.Client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(s.Table),
		Key:            key(BreakerPrefix+name, breakerSeq),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return circuit.Snapshot{}, 0, fmt.Errorf("ddb: load breaker %s: %w", name, err)
	}
	if len(out.Item) == 0 {
		return circuit.Snapshot{}, 0, nil
	}
	raw, ok := out.Item["breaker"].(*types.AttributeValueMemberS)
	ver, ok2 := out.Item["version"].(*types.AttributeValueMemberN)
	if !ok || !ok2 {
		return circuit.Snapshot{}, 0, fmt.Errorf("ddb: breaker %s record is malformed", name)
	}
	version, err := strconv.ParseInt(ver.Value, 10, 64)
	if err != nil {
		return circuit.Snapshot{}, 0, fmt.Errorf("ddb: breaker %s version: %w", name, err)
	}
	var snap circuit.Snapshot
	if err := json.Unmarshal([]byte(raw.Value), &snap); err != nil {
		return circuit.Snapshot{}, 0, fmt.Errorf("ddb: breaker %s: %w", name, err)
	}
	return snap, version, nil
}

// Save implements circuit.Store. The write is conditional on the version
// Load returned, so two invocations moving one breaker cannot fork it; the
// loser gets circuit.ErrConflict and reapplies its outcome to the winner's.
func (s BreakerStore) Save(ctx context.Context, name string, snap circuit.Snapshot, version int64) error {
	body, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	item := key(BreakerPrefix+name, breakerSeq)
	item["kind"] = &types.AttributeValueMemberS{Value: "breaker"}
	item["state"] = &types.AttributeValueMemberS{Value: string(snap.State)}
	item["breaker"] = &types.AttributeValueMemberS{Value: string(body)}
	item["version"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(version+1, 10)}
	item["updated_at"] = &types.AttributeValueMemberS{Value: now().UTC().Format(time.RFC3339Nano)}
	in := &dynamodb.PutItemInput{TableName: aws.String(s.Table), Item: item}
	if version == 0 {
		in.ConditionExpression = aws.String("attribute_not_exists(seq)")
	} else {
		in.ConditionExpression = aws.String("version = :v")
		in.ExpressionAttributeValues = map[string]types.AttributeValue{
			":v": &types.AttributeValueMemberN{Value: strconv.FormatInt(version, 10)},
		}
	}
	if _, err := s.Client.PutItem(ctx, in); err != nil {
		if conditionFailed(err) {
			return circuit.ErrConflict
		}
		return fmt.Errorf("ddb: save breaker %s: %w", name, err)
	}
	return nil
}
