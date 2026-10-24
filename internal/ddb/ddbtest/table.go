// Package ddbtest is an in-memory stand-in for the DynamoDB calls package
// ddb makes, so handlers can be tested without a network.
package ddbtest

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Table is a tiny in-memory table keyed by escalation_id and seq. It
// honours the two conditions this package relies on: attribute_not_exists
// and seq >= :first.
type Table struct {
	mu    sync.Mutex
	items map[string]map[int]map[string]types.AttributeValue
}

// ErrConditionFailed is what DynamoDB returns when a condition is not met.
var ErrConditionFailed = &types.ConditionalCheckFailedException{Message: aws.String("The conditional request failed")}

// NewTable returns an empty table.
func NewTable() *Table {
	return &Table{items: map[string]map[int]map[string]types.AttributeValue{}}
}

func keyOf(item map[string]types.AttributeValue) (string, int) {
	e := item["escalation_id"].(*types.AttributeValueMemberS).Value
	n, _ := strconv.Atoi(item["seq"].(*types.AttributeValueMemberN).Value)
	return e, n
}

// PutItem implements ddb.Client.
func (f *Table) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, n := keyOf(in.Item)
	if f.items[e] == nil {
		f.items[e] = map[int]map[string]types.AttributeValue{}
	}
	existing, exists := f.items[e][n]
	if in.ConditionExpression != nil {
		switch cond := *in.ConditionExpression; cond {
		case "attribute_not_exists(seq)":
			if exists {
				return nil, ErrConditionFailed
			}
		case "version = :v":
			want := in.ExpressionAttributeValues[":v"].(*types.AttributeValueMemberN).Value
			got, ok := existing["version"].(*types.AttributeValueMemberN)
			if !exists || !ok || got.Value != want {
				return nil, ErrConditionFailed
			}
		default:
			panic("fake does not support condition " + cond)
		}
	}
	f.items[e][n] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

// GetItem implements ddb.Client.
func (f *Table) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, n := keyOf(in.Key)
	return &dynamodb.GetItemOutput{Item: f.items[e][n]}, nil
}

// Query implements ddb.Client.
func (f *Table) Query(_ context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(*in.KeyConditionExpression, "seq >= :first") {
		panic("fake supports only the seq >= :first query")
	}
	e := in.ExpressionAttributeValues[":e"].(*types.AttributeValueMemberS).Value
	first, _ := strconv.Atoi(in.ExpressionAttributeValues[":first"].(*types.AttributeValueMemberN).Value)
	var seqs []int
	for n := range f.items[e] {
		if n >= first {
			seqs = append(seqs, n)
		}
	}
	sort.Ints(seqs)
	out := &dynamodb.QueryOutput{}
	for _, n := range seqs {
		out.Items = append(out.Items, project(f.items[e][n], in))
	}
	return out, nil
}

// project returns only the attributes a ProjectionExpression names, as
// DynamoDB does, so a test can show what a reader was never handed.
func project(item map[string]types.AttributeValue, in *dynamodb.QueryInput) map[string]types.AttributeValue {
	if in.ProjectionExpression == nil {
		return item
	}
	out := map[string]types.AttributeValue{}
	for _, name := range strings.Split(*in.ProjectionExpression, ",") {
		name = strings.TrimSpace(name)
		if real, ok := in.ExpressionAttributeNames[name]; ok {
			name = real
		}
		if v, ok := item[name]; ok {
			out[name] = v
		}
	}
	return out
}

// Items returns every item stored under an escalation, in seq order.
func (f *Table) Items(escalationID string) []map[string]types.AttributeValue {
	f.mu.Lock()
	defer f.mu.Unlock()
	var seqs []int
	for n := range f.items[escalationID] {
		seqs = append(seqs, n)
	}
	sort.Ints(seqs)
	var out []map[string]types.AttributeValue
	for _, n := range seqs {
		out = append(out, f.items[escalationID][n])
	}
	return out
}
