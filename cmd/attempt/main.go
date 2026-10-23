// Command attempt is the Lambda the state machine invokes to walk exactly one
// tier of the contact policy. Until that lands it refuses every invocation,
// so an execution stops at its first attempt rather than reporting a delivery
// that never happened.
package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aws/aws-lambda-go/lambda"
)

// errNotImplemented names this function so the failed state in the
// execution history says which handler is missing.
var errNotImplemented = errors.New("attempt: not implemented")

func handle(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, errNotImplemented
}

func main() { lambda.Start(handle) }
