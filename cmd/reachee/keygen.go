package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

// runKeygen prints a fresh Ed25519 key pair as hex, the private half as the
// 32-byte seed and the public half as the 32-byte key the verifier reads.
// It writes nothing to disk: the private half goes straight into Secrets
// Manager and the public half into demo/keys, and a key file left lying in
// the working tree is one `git add .` from being published.
func runKeygen(w io.Writer) error {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "private %s\npublic  %s\n", hex.EncodeToString(priv.Seed()), hex.EncodeToString(pub))
	return err
}
