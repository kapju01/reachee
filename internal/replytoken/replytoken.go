// Package replytoken mints and checks the link a person answers from.
//
// The email goes out before the execution is holding for an answer, so the
// link cannot carry the Step Functions task token. It carries a claim
// instead: which escalation, for which recipient, until when, under which
// nonce, with an HMAC over all four. Possession of a valid link authorizes
// one decision on one escalation. It is not proof of who is holding it.
package replytoken

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Claims is what a link asserts.
type Claims struct {
	EscalationID string `json:"e"`
	RecipientID  string `json:"r"`
	Expiry       int64  `json:"x"` // unix seconds
	Nonce        string `json:"n"`
}

// Errors a link can fail with. Callers show the person which one applies.
var (
	ErrMalformed = errors.New("replytoken: malformed link")
	ErrSignature = errors.New("replytoken: link signature does not verify")
	ErrExpired   = errors.New("replytoken: link has expired")
)

// KeyFrom derives the HMAC key from the receipt signing key, so the stack
// holds one secret rather than two. The label keeps the derived key from
// being usable for anything but reply links.
func KeyFrom(priv ed25519.PrivateKey) []byte {
	m := hmac.New(sha256.New, priv.Seed())
	m.Write([]byte("reachee/reply-token/v1"))
	return m.Sum(nil)
}

// NewNonce returns 128 random bits as hex.
func NewNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

var enc = base64.RawURLEncoding

// Mint returns the token: base64url(claims) "." base64url(HMAC(claims)).
func Mint(key []byte, c Claims) (string, error) {
	if c.EscalationID == "" || c.RecipientID == "" || c.Nonce == "" || c.Expiry == 0 {
		return "", fmt.Errorf("%w: every claim is required", ErrMalformed)
	}
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	payload := enc.EncodeToString(body)
	return payload + "." + enc.EncodeToString(mac(key, payload)), nil
}

// Verify checks the signature before reading anything the token claims,
// then the expiry.
func Verify(key []byte, token string, now time.Time) (Claims, error) {
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || payload == "" || sig == "" {
		return Claims{}, ErrMalformed
	}
	got, err := enc.DecodeString(sig)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	if !hmac.Equal(got, mac(key, payload)) {
		return Claims{}, ErrSignature
	}
	body, err := enc.DecodeString(payload)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	var c Claims
	if err := json.Unmarshal(body, &c); err != nil || c.EscalationID == "" || c.Nonce == "" {
		return Claims{}, ErrMalformed
	}
	if !now.Before(time.Unix(c.Expiry, 0)) {
		return c, ErrExpired
	}
	return c, nil
}

func mac(key []byte, payload string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(payload))
	return m.Sum(nil)
}
