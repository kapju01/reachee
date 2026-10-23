package audit

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
)

// Bundle is the portable form of a chain. It carries the verifying key and
// every event in the exact shape that was signed, with the signature filled
// in. Nothing in it is message content: only content hashes travel.
//
// A Bundle is what you hand to someone who should be able to confirm a human
// was reached and decided, without being able to read what was said.
type Bundle struct {
	PublicKey string           `json:"public_key"`
	Genesis   string           `json:"genesis"`
	Events    []map[string]any `json:"events"`
}

// Export builds a Bundle from a chain and its verifying key.
func Export(events []Event, pub ed25519.PublicKey) Bundle {
	out := Bundle{
		PublicKey: hex.EncodeToString(pub),
		Genesis:   GenesisHash,
		Events:    make([]map[string]any, 0, len(events)),
	}
	for _, e := range events {
		m := CanonicalMap(e)
		m["signature"] = e.Signature
		out.Events = append(out.Events, m)
	}
	return out
}

// MarshalBundle writes a Bundle with the same encoder settings the chain is
// signed under, so the bytes a verifier reads are the bytes that were signed.
func MarshalBundle(b Bundle) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(b); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
