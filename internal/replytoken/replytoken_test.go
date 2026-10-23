package replytoken

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
)

func key(t *testing.T) []byte {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	return KeyFrom(priv)
}

func claims(now time.Time) Claims {
	return Claims{EscalationID: "esc-1", RecipientID: "on-call", Expiry: now.Add(15 * time.Minute).Unix(), Nonce: "n1"}
}

func TestRoundTrip(t *testing.T) {
	k, now := key(t), time.Now()
	tok, err := Mint(k, claims(now))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(tok, "+/=") {
		t.Fatalf("token is not base64url: %q", tok)
	}
	got, err := Verify(k, tok, now)
	if err != nil || got != claims(now) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestRejects(t *testing.T) {
	k, now := key(t), time.Now()
	tok, _ := Mint(k, claims(now))
	payload, sig, _ := strings.Cut(tok, ".")
	other, _ := Mint(k, Claims{EscalationID: "esc-2", RecipientID: "on-call", Expiry: now.Add(time.Hour).Unix(), Nonce: "n2"})
	otherPayload, _, _ := strings.Cut(other, ".")

	cases := map[string]struct {
		key   []byte
		token string
		at    time.Time
		want  error
	}{
		"wrong key":         {key(t), tok, now, ErrSignature},
		"swapped claims":    {k, otherPayload + "." + sig, now, ErrSignature},
		"truncated sig":     {k, payload + "." + sig[:10], now, ErrSignature},
		"no separator":      {k, payload, now, ErrMalformed},
		"empty":             {k, "", now, ErrMalformed},
		"expired":           {k, tok, now.Add(15 * time.Minute), ErrExpired},
		"long after expiry": {k, tok, now.Add(24 * time.Hour), ErrExpired},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Verify(c.key, c.token, c.at); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestMintRequiresEveryClaim(t *testing.T) {
	c := claims(time.Now())
	c.Nonce = ""
	if _, err := Mint(key(t), c); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v", err)
	}
}

// The reply key must not be the signing seed itself.
func TestDerivedKeyIsNotTheSeed(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if string(KeyFrom(priv)) == string(priv.Seed()) {
		t.Fatal("reply key equals the signing seed")
	}
}
