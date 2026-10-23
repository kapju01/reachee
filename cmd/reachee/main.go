// Command reachee runs one escalation end to end against mock providers and
// prints the resulting audit chain. This is the local proof that the pipeline
// holds together before the AWS CDS providers land.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
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

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "reachee: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	policy, err := heartmap.LoadFile(cfg.HeartMapPath)
	if err != nil {
		return err
	}
	store := heartmap.NewMemoryStore()
	store.Put(policy)

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
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
		return err
	}

	fmt.Printf("delivered: %v via %s\n", receipt.Delivered, receipt.DeliveredVia)
	fmt.Printf("verdict: %s (%s)\n", receipt.Verdict, receipt.Reason)
	fmt.Printf("empathy source: %s\n", receipt.EmpathySource)
	fmt.Printf("receipt hash: %s\n\n", receipt.ReceiptHash)

	events := sink.All()
	fmt.Printf("audit chain (%d events):\n", len(events))
	for _, e := range events {
		fmt.Printf("  %s  %-10s  %-26s  %s\n", e.Timestamp.Format(time.RFC3339), e.EventType, e.ProviderName, e.Metadata["reason"])
	}

	if err := audit.Verify(events, pub); err != nil {
		return err
	}
	fmt.Println("\nchain verified against the published public key")

	if os.Getenv("REACHEE_DUMP_CHAIN") != "" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(events)
	}
	return nil
}
