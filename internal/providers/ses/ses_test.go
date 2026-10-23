package ses

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"

	"github.com/aisolutions/reachee/internal/heartmap"
	"github.com/aisolutions/reachee/internal/providers"
)

var _ providers.Messenger = (*Provider)(nil)

type fakeClient struct {
	sendIn  *sesv2.SendEmailInput
	sendOut *sesv2.SendEmailOutput
	sendErr error
	sends   int
	account *sesv2.GetAccountOutput
	accErr  error
}

func (f *fakeClient) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.sends++
	f.sendIn = in
	return f.sendOut, f.sendErr
}

func (f *fakeClient) GetAccount(context.Context, *sesv2.GetAccountInput, ...func(*sesv2.Options)) (*sesv2.GetAccountOutput, error) {
	return f.account, f.accErr
}

func newProvider(t *testing.T, c Client) *Provider {
	t.Helper()
	p, err := New(c, "sender@example.org")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func testMessage() providers.Message {
	return providers.Message{
		RequestID: "req-1",
		Address:   "clinician@example.org",
		Subject:   "Decision needed",
		Body:      "body-text",
		Priority:  providers.P1,
	}
}

func TestIdentity(t *testing.T) {
	p := newProvider(t, &fakeClient{})
	if p.Name() != "ses-v2" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.Channel() != heartmap.ChannelEmail {
		t.Errorf("Channel = %q", p.Channel())
	}
}

func TestNewRejectsMissingArguments(t *testing.T) {
	if _, err := New(nil, "sender@example.org"); err == nil {
		t.Error("nil client accepted")
	}
	if _, err := New(&fakeClient{}, ""); err == nil {
		t.Error("empty sender accepted")
	}
}

// Criterion 06 asserts on ProviderMsgID, so it must be SES's own id verbatim.
func TestSendReturnsSESMessageID(t *testing.T) {
	f := &fakeClient{sendOut: &sesv2.SendEmailOutput{MessageId: aws.String("0101-ses-id")}}
	p := newProvider(t, f)
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return at }

	r, err := p.Send(context.Background(), testMessage())
	if err != nil {
		t.Fatal(err)
	}
	if r.ProviderMsgID != "0101-ses-id" || r.ProviderName != "ses-v2" || !r.AcceptedAt.Equal(at) {
		t.Errorf("receipt = %+v", r)
	}

	in := f.sendIn
	if aws.ToString(in.FromEmailAddress) != "sender@example.org" {
		t.Errorf("from = %q", aws.ToString(in.FromEmailAddress))
	}
	if got := in.Destination.ToAddresses; len(got) != 1 || got[0] != "clinician@example.org" {
		t.Errorf("to = %v", got)
	}
	s := in.Content.Simple
	if s == nil {
		t.Fatal("content is not Simple")
	}
	if aws.ToString(s.Subject.Data) != "Decision needed" || aws.ToString(s.Body.Text.Data) != "body-text" {
		t.Errorf("simple content = %q / %q", aws.ToString(s.Subject.Data), aws.ToString(s.Body.Text.Data))
	}
}

func TestSendRejectsMissingMessageID(t *testing.T) {
	for name, out := range map[string]*sesv2.SendEmailOutput{
		"nil output": nil,
		"nil id":     {},
		"empty id":   {MessageId: aws.String("")},
	} {
		t.Run(name, func(t *testing.T) {
			p := newProvider(t, &fakeClient{sendOut: out})
			r, err := p.Send(context.Background(), testMessage())
			if !errors.Is(err, ErrNoMessageID) {
				t.Errorf("err = %v, want ErrNoMessageID", err)
			}
			if r.ProviderMsgID != "" {
				t.Errorf("synthesized id %q", r.ProviderMsgID)
			}
		})
	}
}

// A provider reports its failure and stops; retry is the dispatcher's job.
func TestSendFailureIsReportedOnceAndDisclosesNothing(t *testing.T) {
	cause := errors.New("throttled")
	f := &fakeClient{sendErr: cause}
	p := newProvider(t, f)
	msg := testMessage()

	_, err := p.Send(context.Background(), msg)
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want wrapped cause", err)
	}
	if f.sends != 1 {
		t.Errorf("SendEmail called %d times, want 1", f.sends)
	}
	if s := err.Error(); strings.Contains(s, msg.Address) || strings.Contains(s, msg.Body) {
		t.Errorf("error discloses message content: %q", s)
	}
}

func TestSendRejectsEmptyAddressWithoutCalling(t *testing.T) {
	f := &fakeClient{}
	p := newProvider(t, f)
	msg := testMessage()
	msg.Address = ""
	if _, err := p.Send(context.Background(), msg); err == nil {
		t.Error("empty address accepted")
	}
	if f.sends != 0 {
		t.Errorf("SendEmail called %d times", f.sends)
	}
}

func TestHealthCheck(t *testing.T) {
	cases := map[string]struct {
		out    *sesv2.GetAccountOutput
		err    error
		wantOK bool
	}{
		"healthy":            {out: &sesv2.GetAccountOutput{EnforcementStatus: aws.String("HEALTHY"), SendingEnabled: true}, wantOK: true},
		"healthy but paused": {out: &sesv2.GetAccountOutput{EnforcementStatus: aws.String("HEALTHY"), SendingEnabled: false}},
		"probation":          {out: &sesv2.GetAccountOutput{EnforcementStatus: aws.String("PROBATION")}},
		"shutdown":           {out: &sesv2.GetAccountOutput{EnforcementStatus: aws.String("SHUTDOWN")}},
		"missing":            {out: &sesv2.GetAccountOutput{}},
		"nil":                {},
		"api error":          {err: errors.New("access denied")},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			p := newProvider(t, &fakeClient{account: c.out, accErr: c.err})
			err := p.HealthCheck(context.Background())
			if (err == nil) != c.wantOK {
				t.Errorf("err = %v, wantOK %v", err, c.wantOK)
			}
		})
	}
}
