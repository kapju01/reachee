// Package ses is the email Messenger over Amazon SES v2, the one provisioned
// channel in this build. It sends once and reports what SES said. Retry,
// backoff, and failover belong to the dispatcher and the circuit breaker, so a
// failure here is returned unchanged in substance and nothing is attempted
// twice.
package ses

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"

	"github.com/aisolutions/reachee/internal/heartmap"
	"github.com/aisolutions/reachee/internal/providers"
)

// Name is the provider identifier written into every receipt.
const Name = "ses-v2"

// enforcementHealthy is the only GetAccount enforcement status under which
// SES will send without restriction. PROBATION and SHUTDOWN both fail the
// health check, because a provider on probation may stop accepting mid-run.
const enforcementHealthy = "HEALTHY"

// Client is the slice of the SES v2 API this provider depends on. It exists
// so tests can substitute a fake without a network, and so the execution role
// can be scoped to exactly ses:SendEmail and ses:GetAccount.
type Client interface {
	SendEmail(ctx context.Context, in *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
	GetAccount(ctx context.Context, in *sesv2.GetAccountInput, optFns ...func(*sesv2.Options)) (*sesv2.GetAccountOutput, error)
}

// ErrNoMessageID means SES reported success without a MessageId. The receipt
// cannot be written truthfully without one, so the send is reported as failed
// rather than given a synthesized id.
var ErrNoMessageID = errors.New("ses: SendEmail returned no MessageId")

// Provider is a providers.Messenger that delivers over SES v2.
type Provider struct {
	client Client
	sender string
	now    func() time.Time
}

// New builds a Provider that sends from sender, which must be an identity
// verified in SES. The sender is passed in rather than read from the
// environment so the caller decides where configuration comes from and tests
// never depend on process state.
func New(client Client, sender string) (*Provider, error) {
	if client == nil {
		return nil, errors.New("ses: nil client")
	}
	if sender == "" {
		return nil, errors.New("ses: empty sender address")
	}
	return &Provider{client: client, sender: sender, now: time.Now}, nil
}

// Name implements providers.Messenger.
func (p *Provider) Name() string { return Name }

// Channel implements providers.Messenger.
func (p *Provider) Channel() string { return heartmap.ChannelEmail }

// Send implements providers.Messenger. A nil error means SES accepted the
// message, which is SENT and nothing more: acceptance is not delivery, and
// delivery is not attention. Errors never carry the address or body, so a
// logged failure discloses nothing about the recipient or what was said.
func (p *Provider) Send(ctx context.Context, msg providers.Message) (providers.Receipt, error) {
	if msg.Address == "" {
		return providers.Receipt{}, errors.New("ses: message has no address")
	}
	out, err := p.client.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(p.sender),
		Destination:      &types.Destination{ToAddresses: []string{msg.Address}},
		Content: &types.EmailContent{
			Simple: &types.Message{
				Subject: &types.Content{Data: aws.String(msg.Subject), Charset: aws.String("UTF-8")},
				Body: &types.Body{
					Text: &types.Content{Data: aws.String(msg.Body), Charset: aws.String("UTF-8")},
				},
			},
		},
	})
	if err != nil {
		return providers.Receipt{}, fmt.Errorf("ses: send %s: %w", msg.RequestID, err)
	}
	if out == nil || aws.ToString(out.MessageId) == "" {
		return providers.Receipt{}, fmt.Errorf("%w (request %s)", ErrNoMessageID, msg.RequestID)
	}
	return providers.Receipt{
		ProviderName:  Name,
		ProviderMsgID: *out.MessageId,
		AcceptedAt:    p.now(),
	}, nil
}

// HealthCheck implements providers.Messenger. It passes only when SES reports
// the account's enforcement status as HEALTHY and sending as enabled, because
// a healthy account with sending paused rejects every message. An unreadable
// or missing status is a failure, not a presumption of health.
//
// A pass says the account can send, not that any particular recipient will
// accept: in the SES sandbox verification is per identity, and a health check
// takes no recipient, so it cannot know whether the address is verified.
func (p *Provider) HealthCheck(ctx context.Context) error {
	out, err := p.client.GetAccount(ctx, &sesv2.GetAccountInput{})
	if err != nil {
		return fmt.Errorf("ses: get account: %w", err)
	}
	if out == nil || out.EnforcementStatus == nil {
		return errors.New("ses: account enforcement status not reported")
	}
	if status := *out.EnforcementStatus; status != enforcementHealthy {
		return fmt.Errorf("ses: account enforcement status is %s, not %s", status, enforcementHealthy)
	}
	if !out.SendingEnabled {
		return errors.New("ses: account sending is paused")
	}
	return nil
}
