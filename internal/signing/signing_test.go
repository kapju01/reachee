package signing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

type fakeSecrets struct {
	value *string
	err   error
	calls int
}

func (f *fakeSecrets) GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	f.calls++
	return &secretsmanager.GetSecretValueOutput{SecretString: f.value}, f.err
}

func TestLoadDecodesTheKeygenSeed(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &fakeSecrets{value: aws.String(hex.EncodeToString(priv.Seed()) + "\n")}

	got, err := Load(context.Background(), f, "reachee/signing-key")
	if err != nil {
		t.Fatal(err)
	}
	if !pub.Equal(got.Public()) {
		t.Fatal("loaded key does not match the seed")
	}
}

func TestLoadRefusesBadSecrets(t *testing.T) {
	for name, f := range map[string]*fakeSecrets{
		"no value":   {},
		"not hex":    {value: aws.String("not-a-key-at-all")},
		"wrong size": {value: aws.String("abcd")},
		"api error":  {err: errors.New("AccessDenied")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(context.Background(), f, "reachee/signing-key")
			if err == nil {
				t.Fatal("bad secret accepted")
			}
			if f.value != nil && strings.Contains(err.Error(), *f.value) {
				t.Fatal("error discloses the secret value")
			}
		})
	}
	if _, err := Load(context.Background(), &fakeSecrets{}, ""); err == nil {
		t.Fatal("empty secret id accepted")
	}
}
