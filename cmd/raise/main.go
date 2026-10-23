// Command raise is the Lambda behind POST /raise. It will return a receipt
// and start the escalation's execution. Until that lands it refuses every
// invocation, so a stack deployed ahead of the handler fails loudly instead
// of answering with something that looks like a receipt.
package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/aws/aws-lambda-go/lambda"
)

// errNotImplemented names this function so the failure is attributable from
// the API response and the log alone.
var errNotImplemented = errors.New("raise: not implemented")

func handle(context.Context, json.RawMessage) (json.RawMessage, error) {
	return nil, errNotImplemented
}

func main() { lambda.Start(handle) }
