// Command reply is the Lambda behind GET /reply and the answer states of the
// state machine. It will validate the token, classify the answer, and record
// the outcome. Until that lands it refuses every invocation, so nothing can
// be released and no decision can be recorded by a stub.
package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aws/aws-lambda-go/lambda"
)

// errNotImplemented names this function so the failure is attributable from
// the API response, the execution history, and the log alone.
var errNotImplemented = errors.New("reply: not implemented")

func handle(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, errNotImplemented
}

func main() { lambda.Start(handle) }
