package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"time"

	"github.com/aisolutions/reachee/internal/audit"
	"github.com/aisolutions/reachee/internal/circuit"
	"github.com/aisolutions/reachee/internal/config"
	"github.com/aisolutions/reachee/internal/dignity"
	"github.com/aisolutions/reachee/internal/dispatcher"
	"github.com/aisolutions/reachee/internal/empathy"
	"github.com/aisolutions/reachee/internal/heartmap"
	"github.com/aisolutions/reachee/internal/providers"
	"github.com/aisolutions/reachee/internal/providers/mock"
)

// escalate runs one escalation end to end against mock providers and returns
// the resulting chain with the key that signed it. Both the demo and the
// bundle export build on this, so what the demo prints and what a third party
// verifies are the same events.
func escalate() ([]audit.Event, ed25519.PublicKey, dispatcher.Receipt, error) {
	var zero dispatcher.Receipt

	cfg, err := config.Load()
	if err != nil {
		return nil, nil, zero, err
	}

	policy, err := heartmap.LoadFile(cfg.HeartMapPath)
	if err != nil {
		return nil, nil, zero, err
	}
	store := heartmap.NewMemoryStore()
	store.Put(policy)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, zero, err
	}
	sink := &audit.MemorySink{}

	// Tier 1 is down. Reachee should report the broken promise and walk on.
	d := &dispatcher.Dispatcher{
		Policies:   store,
		Dignity:    dignity.New(nil),
		Breakers:   circuit.NewRegistry(circuit.DefaultConfig(), nil),
		Translator: empathy.NewTranslator(nil, cfg.EmpathyTimeout),
		Audit:      audit.NewLogger(priv, sink, nil),
		Providers: map[string]providers.Messenger{
			heartmap.ChannelWhatsApp: mock.Failing("whatsapp-socialmessaging", heartmap.ChannelWhatsApp, -1),
			heartmap.ChannelEmail:    mock.New("ses-v2", heartmap.ChannelEmail),
			heartmap.ChannelSMS:      mock.New("pinpoint-sms-voice-v2", heartmap.ChannelSMS),
		},
	}

	receipt, err := d.Dispatch(context.Background(), dispatcher.Request{
		RequestID:         "req-" + time.Now().UTC().Format("20060102T150405"),
		AgentID:           "scout-close-reconciler",
		AgentAutonomy:     "Supervised",
		RecipientID:       policy.ID,
		CorrelationID:     "q4-close-variance",
		Priority:          providers.P1,
		Subject:           "Q4 close variance needs your call",
		Payload:           "Reconciliation flagged a variance above the declared tolerance and wants your input before it proceeds.",
		RequestedResponse: "Approve",
	})
	if err != nil {
		return nil, nil, zero, err
	}
	return sink.All(), pub, receipt, nil
}

// runExport writes a verifiable bundle to path: the public key plus every
// event in the exact shape it was signed. Hand that file and the verifier to
// someone who should confirm a human decided, without reading what was said.
func runExport(path string) error {
	events, pub, _, err := escalate()
	if err != nil {
		return err
	}
	if err := audit.Verify(events, pub); err != nil {
		return fmt.Errorf("refusing to export a chain that does not verify: %w", err)
	}
	data, err := audit.MarshalBundle(audit.Export(events, pub))
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s: %d events, public key %x\n", path, len(events), pub)
	return nil
}
