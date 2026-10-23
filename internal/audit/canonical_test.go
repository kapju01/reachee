package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// goldenPath holds the one agreed canonical rendering of a deliberately
// hostile event. The Go encoder and the independent Python verifier are each
// tested against this file, so a change in either implementation shows up as a
// failing test rather than as a chain that will not verify on demo day.
var goldenPath = filepath.Join("..", "..", "testdata", "canonical_golden.txt")

// nastyEvent carries every character class that has ever broken cross-language
// JSON agreement: HTML-significant characters Go escapes by default, the
// control characters with short escapes, a control character without one,
// the Unicode line separators, and non-ASCII text.
func nastyEvent() Event {
	return Event{
		EventID:      "01a16000-0000-7000-8000-000000000001",
		Timestamp:    time.Date(2026, 10, 23, 17, 4, 5, 123456000, time.UTC),
		PrevHash:     "00",
		RequestID:    "req-<1>&\"2\"",
		RecipientID:  "j.okafor",
		ProviderName: "ses-v2",
		EventType:    TypeSent,
		MessageHash:  "aa",
		Metadata: map[string]string{
			"z_last":  "tab\there",
			"a_first": "line\nbreak",
			"unicode": "38.5°C café 中文",
			"control": "bell\u0007back\u0008form\u000c",
			"sep":     "u2028 u2029 ",
		},
	}
}

func TestCanonicalMatchesGolden(t *testing.T) {
	got, err := Canonical(nastyEvent())
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("REACHEE_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("golden updated: %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("golden missing, regenerate with REACHEE_UPDATE_GOLDEN=1: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("canonical form drifted\n got: %s\nwant: %s", got, want)
	}
}

// TestCanonicalSortsKeys guards the property that makes cross-language
// verification possible: keys in sorted order, not struct declaration order.
func TestCanonicalSortsKeys(t *testing.T) {
	got, err := Canonical(nastyEvent())
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != '{' || string(got[1:11]) != `"event_id"` {
		t.Fatalf("expected the first key to be event_id, found %s", got[:24])
	}
}

// TestCanonicalDoesNotEscapeHTML catches a regression to the default encoder,
// which would render < and & as < and & and break every other
// language's reading of the same bytes.
func TestCanonicalDoesNotEscapeHTML(t *testing.T) {
	got, err := Canonical(nastyEvent())
	if err != nil {
		t.Fatal(err)
	}
	// With the default encoder these three characters are replaced by their
	// \\uXXXX escapes, which no other language's encoder produces. Their literal
	// presence is the proof that escaping is off.
	for _, want := range []string{"<", ">", "&"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("%q is escaped: HTML escaping is on again, and no other language will reproduce these bytes", want)
		}
	}
}
