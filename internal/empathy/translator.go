// Package empathy wraps raw agent output in language that respects the
// recipient's attention. When the model is unavailable the fallback sends the
// agent's own words unaltered and says so in the audit log, through Source and
// Reason on the SENT receipt, so nobody is misled about whether the empathy
// layer ran. The recipient's message carries no debug marker.
package empathy

import (
	"context"
	"strings"
	"time"
)

// Source records which path produced a message.
type Source string

// Source values.
const (
	SourceModel    Source = "model"
	SourceFallback Source = "fallback"
)

// Request is the input to a translation.
type Request struct {
	RecipientName string
	RecipientRole string
	Subject       string
	RawContent    string
	Priority      string
}

// Result is a translated message plus honest provenance.
type Result struct {
	Body   string
	Source Source
	Reason string // why the fallback ran, empty on the model path
}

// Model is the humanizing backend. BedrockTranslator will implement it in
// week five; the interface keeps the dispatcher independent of that timing.
type Model interface {
	Humanize(ctx context.Context, req Request) (string, error)
}

// Translator calls a Model with a deadline and degrades honestly.
type Translator struct {
	model   Model
	timeout time.Duration
}

// NewTranslator builds a Translator. A nil model always uses the fallback.
func NewTranslator(model Model, timeout time.Duration) *Translator {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Translator{model: model, timeout: timeout}
}

// Translate humanizes a message, or falls back with its provenance recorded.
func (t *Translator) Translate(ctx context.Context, req Request) Result {
	if t.model == nil {
		return Fallback(req, "no model configured")
	}
	ctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	type outcome struct {
		body string
		err  error
	}
	ch := make(chan outcome, 1)
	go func() {
		body, err := t.model.Humanize(ctx, req)
		ch <- outcome{body: body, err: err}
	}()

	select {
	case o := <-ch:
		if o.err != nil {
			return Fallback(req, o.err.Error())
		}
		if strings.TrimSpace(o.body) == "" {
			return Fallback(req, "model returned an empty body")
		}
		return Result{Body: o.body, Source: SourceModel}
	case <-ctx.Done():
		return Fallback(req, "model deadline exceeded")
	}
}

// Fallback builds the unhumanized message: the raw content alone. The subject
// already travels as the message's subject line, so repeating it here would
// make the reader read it twice.
func Fallback(req Request, reason string) Result {
	return Result{Body: strings.TrimSpace(req.RawContent), Source: SourceFallback, Reason: reason}
}
