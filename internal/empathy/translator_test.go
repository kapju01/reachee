package empathy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type stubModel struct {
	body  string
	err   error
	delay time.Duration
}

func (s stubModel) Humanize(ctx context.Context, _ Request) (string, error) {
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return s.body, s.err
}

func req() Request {
	return Request{RecipientName: "Justin", Subject: "Q4 close variance", RawContent: "variance above tolerance, review required", Priority: "P1"}
}

func TestModelPathIsMarkedAsModel(t *testing.T) {
	tr := NewTranslator(stubModel{body: "Justin, an agent needs your input."}, time.Second)
	got := tr.Translate(context.Background(), req())
	if got.Source != SourceModel {
		t.Fatalf("expected the model source, got %s", got.Source)
	}
	if got.Reason != "" {
		t.Fatalf("the model path carries no fallback reason, got %q", got.Reason)
	}
}

func TestModelErrorFallsBackAndDiscloses(t *testing.T) {
	tr := NewTranslator(stubModel{err: errors.New("bedrock unavailable")}, time.Second)
	got := tr.Translate(context.Background(), req())
	if got.Source != SourceFallback {
		t.Fatalf("expected the fallback source, got %s", got.Source)
	}
	if !strings.HasPrefix(got.Body, "[fallback]") {
		t.Fatalf("the fallback must be visible in the payload, got %q", got.Body)
	}
	if got.Reason == "" {
		t.Fatal("the fallback must record why it ran")
	}
}

func TestTimeoutFallsBack(t *testing.T) {
	tr := NewTranslator(stubModel{body: "too late", delay: 200 * time.Millisecond}, 20*time.Millisecond)
	got := tr.Translate(context.Background(), req())
	if got.Source != SourceFallback {
		t.Fatalf("expected a fallback past the deadline, got %s", got.Source)
	}
}

func TestEmptyModelBodyFallsBack(t *testing.T) {
	tr := NewTranslator(stubModel{body: "   "}, time.Second)
	if got := tr.Translate(context.Background(), req()); got.Source != SourceFallback {
		t.Fatalf("an empty body is not a translation, got %s", got.Source)
	}
}

func TestNilModelFallsBack(t *testing.T) {
	tr := NewTranslator(nil, time.Second)
	if got := tr.Translate(context.Background(), req()); got.Source != SourceFallback {
		t.Fatalf("expected a fallback with no model configured, got %s", got.Source)
	}
}
