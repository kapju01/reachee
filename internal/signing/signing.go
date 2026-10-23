// Package signing loads the Ed25519 key that signs receipts. Handlers call
// Load once, at init, and keep the key for the life of the execution
// environment: a Secrets Manager round trip on every invocation is latency
// criterion 01 cannot spare.
package signing

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// Client is the one Secrets Manager call this package makes.
type Client interface {
	GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, optFns ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// Load fetches the secret and decodes it as the hex Ed25519 seed that
// `reachee keygen` prints. Errors never include the secret's value.
func Load(ctx context.Context, c Client, secretID string) (ed25519.PrivateKey, error) {
	if secretID == "" {
		return nil, errors.New("signing: no secret id configured")
	}
	out, err := c.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(secretID)})
	if err != nil {
		return nil, fmt.Errorf("signing: get secret: %w", err)
	}
	if out == nil || out.SecretString == nil {
		return nil, errors.New("signing: secret has no string value; put the keygen seed in it")
	}
	return Parse(*out.SecretString)
}

// Parse decodes a hex Ed25519 seed into a private key.
func Parse(seedHex string) (ed25519.PrivateKey, error) {
	seed, err := hex.DecodeString(strings.TrimSpace(seedHex))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("signing: secret is not a %d-byte hex seed", ed25519.SeedSize)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
