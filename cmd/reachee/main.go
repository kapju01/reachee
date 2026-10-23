// Command reachee runs one escalation end to end against mock providers and
// prints the resulting audit chain. This is the local proof that the pipeline
// holds together before the AWS CDS providers land.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/aisolutions/reachee/internal/audit"
)

func main() {
	var err error
	switch {
	case len(os.Args) > 2 && os.Args[1] == "export":
		err = runExport(os.Args[2])
	default:
		err = run()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "reachee: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	events, pub, receipt, err := escalate()
	if err != nil {
		return err
	}

	fmt.Printf("delivered: %v via %s\n", receipt.Delivered, receipt.DeliveredVia)
	fmt.Printf("verdict: %s (%s)\n", receipt.Verdict, receipt.Reason)
	fmt.Printf("empathy source: %s\n", receipt.EmpathySource)
	fmt.Printf("receipt hash: %s\n\n", receipt.ReceiptHash)

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
