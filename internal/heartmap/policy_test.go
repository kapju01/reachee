package heartmap

import (
	"strings"
	"testing"
	"time"
)

const sample = `
contact:
  id: justin-kaplan
  display_name: Justin Kaplan
  channels:
    whatsapp:
      number: "+10000000001"
      preferred: true
    email:
      address: justin@example.invalid
      preferred: true
    sms:
      number: "+10000000002"
      lifeline: true
  failover_ladder:
    - tier: 1
      channel: whatsapp
      timeout_seconds: 300
    - tier: 2
      channel: email
      timeout_seconds: 900
    - tier: 3
      channel: sms
      timeout_seconds: 60
  quiet_hours:
    timezone: America/Chicago
    start: "22:00"
    end: "07:00"
    p0_bypass: true
  rate_limit:
    max_per_hour: 6
    burst: 3
    coalesce_window_seconds: 90
`

func TestLoadParsesTheFullPolicy(t *testing.T) {
	p, err := Load(sample)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "justin-kaplan" || p.DisplayName != "Justin Kaplan" {
		t.Fatalf("unexpected identity: %+v", p)
	}
	if len(p.Ladder) != 3 {
		t.Fatalf("expected three rungs, got %d", len(p.Ladder))
	}
	if p.Ladder[0].Channel != ChannelWhatsApp || p.Ladder[0].Timeout != 300*time.Second {
		t.Fatalf("unexpected tier 1: %+v", p.Ladder[0])
	}
	if p.Ladder[2].Timeout != 60*time.Second {
		t.Fatalf("unexpected tier 3 timeout: %v", p.Ladder[2].Timeout)
	}
	if addr, ok := p.Address(ChannelEmail); !ok || addr != "justin@example.invalid" {
		t.Fatalf("expected the email address, got %q", addr)
	}
	if addr, ok := p.Address(ChannelSMS); !ok || addr != "+10000000002" {
		t.Fatalf("expected the sms number, got %q", addr)
	}
	if !p.Channels[ChannelSMS].Lifeline {
		t.Fatal("sms should be marked lifeline")
	}
	if p.RateLimit.MaxPerHour != 6 || p.RateLimit.Burst != 3 || p.RateLimit.CoalesceWindow != 90*time.Second {
		t.Fatalf("unexpected rate limit: %+v", p.RateLimit)
	}
	if !p.QuietHours.P0Bypass {
		t.Fatal("p0_bypass should be true")
	}
}

func TestLadderMustReferenceDeclaredChannels(t *testing.T) {
	bad := strings.Replace(sample, "      channel: email", "      channel: pigeon", 1)
	if _, err := Load(bad); err == nil {
		t.Fatal("expected a validation error for an undeclared channel")
	}
}

func TestLoadRejectsAnEmptyLadder(t *testing.T) {
	src := "contact:\n  id: someone\n  channels:\n    email:\n      address: a@b.invalid\n"
	if _, err := Load(src); err == nil {
		t.Fatal("expected an error when no ladder is declared")
	}
}

func TestQuietHoursWrapMidnight(t *testing.T) {
	p, err := Load(sample)
	if err != nil {
		t.Fatal(err)
	}
	chicago := p.QuietHours.Location

	cases := []struct {
		hour int
		want bool
	}{
		{23, true},
		{2, true},
		{6, true},
		{7, false},
		{12, false},
		{21, false},
		{22, true},
	}
	for _, c := range cases {
		at := time.Date(2026, 10, 14, c.hour, 0, 0, 0, chicago)
		if got := p.InQuietHours(at); got != c.want {
			t.Fatalf("hour %02d: expected quiet=%v, got %v", c.hour, c.want, got)
		}
	}
}

func TestQuietHoursRespectTheContactTimezone(t *testing.T) {
	p, err := Load(sample)
	if err != nil {
		t.Fatal(err)
	}
	// 04:00 UTC is 23:00 the previous day in Chicago, which is quiet.
	at := time.Date(2026, 10, 15, 4, 0, 0, 0, time.UTC)
	if !p.InQuietHours(at) {
		t.Fatal("expected the contact timezone to govern quiet hours")
	}
}

func TestNextAllowedLandsAtTheWindowEnd(t *testing.T) {
	p, err := Load(sample)
	if err != nil {
		t.Fatal(err)
	}
	chicago := p.QuietHours.Location
	at := time.Date(2026, 10, 14, 23, 30, 0, 0, chicago)
	next := p.NextAllowed(at).In(chicago)
	if next.Hour() != 7 || next.Day() != 15 {
		t.Fatalf("expected 07:00 the next morning, got %s", next)
	}
}

func TestNextAllowedAcrossDST(t *testing.T) {
	p, err := Load(sample)
	if err != nil {
		t.Fatal(err)
	}
	chicago := p.QuietHours.Location
	// US DST ends on 2026-11-01; the quiet window spans the transition.
	at := time.Date(2026, 10, 31, 23, 0, 0, 0, chicago)
	next := p.NextAllowed(at).In(chicago)
	if next.Hour() != 7 || next.Day() != 1 || next.Month() != time.November {
		t.Fatalf("expected 07:00 on Nov 1 local, got %s", next)
	}
	if p.InQuietHours(next) {
		t.Fatal("the next allowed instant must be outside quiet hours")
	}
}

func TestResolveUnknownContact(t *testing.T) {
	s := NewMemoryStore()
	if _, err := s.Resolve("nobody"); err == nil {
		t.Fatal("expected ErrNotFound for an unknown contact")
	}
}
