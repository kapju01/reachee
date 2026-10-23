package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"strings"
	"testing"
)

// The printed halves must be a matching pair: a seed that does not derive the
// printed public key would sign receipts the published key cannot verify.
func TestKeygenPrintsMatchingPair(t *testing.T) {
	var buf bytes.Buffer
	if err := runKeygen(&buf); err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(buf.String())
	if len(fields) != 4 || fields[0] != "private" || fields[2] != "public" {
		t.Fatalf("unexpected output shape: %d fields", len(fields))
	}
	seed, err := hex.DecodeString(fields[1])
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("private half is not a %d-byte hex seed", ed25519.SeedSize)
	}
	pub, err := hex.DecodeString(fields[3])
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("public half is not a %d-byte hex key", ed25519.PublicKeySize)
	}
	derived := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if !bytes.Equal(derived, pub) {
		t.Error("public half does not derive from the private half")
	}
}
