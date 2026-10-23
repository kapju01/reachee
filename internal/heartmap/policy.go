// Package heartmap holds per-contact escalation policy: the failover ladder,
// quiet hours, rate limits, and channel addresses for each human Reachee may
// need to reach.
package heartmap

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Channel names are stable identifiers shared with the provider registry.
const (
	ChannelWhatsApp = "whatsapp"
	ChannelEmail    = "email"
	ChannelSMS      = "sms"
)

// Channel is one reachable address for a contact.
type Channel struct {
	Name      string
	Address   string
	Preferred bool
	Lifeline  bool
}

// Rung is one tier of the failover ladder.
type Rung struct {
	Tier    int
	Channel string
	Timeout time.Duration
}

// QuietHours declares when a contact should not be disturbed below P0.
type QuietHours struct {
	Location *time.Location
	Start    string // "22:00"
	End      string // "07:00"
	P0Bypass bool
}

// RateLimit bounds how much of a human's attention Reachee may spend.
type RateLimit struct {
	MaxPerHour     int
	Burst          int
	CoalesceWindow time.Duration
}

// Policy is the resolved Heart-Map entry for a single contact.
type Policy struct {
	ID          string
	DisplayName string
	Channels    map[string]Channel
	Ladder      []Rung
	QuietHours  QuietHours
	RateLimit   RateLimit
}

// ErrNotFound is returned when no policy exists for a contact.
var ErrNotFound = errors.New("heartmap: no policy for contact")

// Address returns the contact address for a channel on the ladder.
func (p Policy) Address(channel string) (string, bool) {
	c, ok := p.Channels[channel]
	if !ok || c.Address == "" {
		return "", false
	}
	return c.Address, true
}

// InQuietHours reports whether at is inside the contact's quiet window.
func (p Policy) InQuietHours(at time.Time) bool {
	if p.QuietHours.Start == "" || p.QuietHours.End == "" {
		return false
	}
	loc := p.QuietHours.Location
	if loc == nil {
		loc = time.UTC
	}
	local := at.In(loc)
	mins := local.Hour()*60 + local.Minute()
	start, err1 := minutesOfDay(p.QuietHours.Start)
	end, err2 := minutesOfDay(p.QuietHours.End)
	if err1 != nil || err2 != nil {
		return false
	}
	if start == end {
		return false
	}
	if start < end {
		return mins >= start && mins < end
	}
	// Window wraps midnight.
	return mins >= start || mins < end
}

// NextAllowed returns the first instant at or after "at" outside quiet hours.
func (p Policy) NextAllowed(at time.Time) time.Time {
	if !p.InQuietHours(at) {
		return at
	}
	loc := p.QuietHours.Location
	if loc == nil {
		loc = time.UTC
	}
	end, err := minutesOfDay(p.QuietHours.End)
	if err != nil {
		return at
	}
	local := at.In(loc)
	candidate := time.Date(local.Year(), local.Month(), local.Day(), end/60, end%60, 0, 0, loc)
	if !candidate.After(local) {
		candidate = candidate.AddDate(0, 0, 1)
	}
	return candidate
}

func minutesOfDay(hhmm string) (int, error) {
	parts := strings.Split(strings.TrimSpace(hhmm), ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("heartmap: bad time %q", hhmm)
	}
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Store resolves policies by contact id. The in-memory implementation backs
// local development and tests; DynamoDB backs the deployed stack.
type Store interface {
	Resolve(contactID string) (Policy, error)
}

// MemoryStore is an in-memory Store.
type MemoryStore struct {
	policies map[string]Policy
}

// NewMemoryStore builds an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{policies: map[string]Policy{}}
}

// Put inserts or replaces a policy.
func (s *MemoryStore) Put(p Policy) { s.policies[p.ID] = p }

// Resolve implements Store.
func (s *MemoryStore) Resolve(contactID string) (Policy, error) {
	p, ok := s.policies[contactID]
	if !ok {
		return Policy{}, fmt.Errorf("%w: %s", ErrNotFound, contactID)
	}
	return p, nil
}

// LoadFile reads a Heart-Map YAML file into a Policy.
func LoadFile(path string) (Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Policy{}, err
	}
	return Load(string(b))
}

// Load parses a Heart-Map YAML document into a Policy.
func Load(src string) (Policy, error) {
	root := asMap(parse(scan(src), 0))
	c := asMap(root["contact"])
	if c == nil {
		return Policy{}, errors.New("heartmap: missing top-level 'contact' key")
	}

	p := Policy{
		ID:          str(c, "id"),
		DisplayName: str(c, "display_name"),
		Channels:    map[string]Channel{},
	}
	if p.ID == "" {
		return Policy{}, errors.New("heartmap: contact.id is required")
	}

	for name, raw := range asMap(c["channels"]) {
		cm := asMap(raw)
		if cm == nil {
			continue
		}
		addr := str(cm, "address")
		if addr == "" {
			addr = str(cm, "number")
		}
		p.Channels[name] = Channel{
			Name:      name,
			Address:   addr,
			Preferred: boolean(cm, "preferred"),
			Lifeline:  boolean(cm, "lifeline"),
		}
	}

	for _, raw := range asList(c["failover_ladder"]) {
		rm := asMap(raw)
		if rm == nil {
			continue
		}
		p.Ladder = append(p.Ladder, Rung{
			Tier:    num(rm, "tier", len(p.Ladder)+1),
			Channel: str(rm, "channel"),
			Timeout: time.Duration(num(rm, "timeout_seconds", 300)) * time.Second,
		})
	}
	if len(p.Ladder) == 0 {
		return Policy{}, errors.New("heartmap: failover_ladder must declare at least one rung")
	}

	if qh := asMap(c["quiet_hours"]); qh != nil {
		loc := time.UTC
		if tz := str(qh, "timezone"); tz != "" {
			if l, err := time.LoadLocation(tz); err == nil {
				loc = l
			}
		}
		p.QuietHours = QuietHours{
			Location: loc,
			Start:    str(qh, "start"),
			End:      str(qh, "end"),
			P0Bypass: boolean(qh, "p0_bypass"),
		}
	}

	rl := asMap(c["rate_limit"])
	p.RateLimit = RateLimit{MaxPerHour: 6, Burst: 3, CoalesceWindow: 90 * time.Second}
	if rl != nil {
		p.RateLimit = RateLimit{
			MaxPerHour:     num(rl, "max_per_hour", 6),
			Burst:          num(rl, "burst", 3),
			CoalesceWindow: time.Duration(num(rl, "coalesce_window_seconds", 90)) * time.Second,
		}
	}

	for _, r := range p.Ladder {
		if _, ok := p.Channels[r.Channel]; !ok {
			return Policy{}, fmt.Errorf("heartmap: ladder tier %d references undeclared channel %q", r.Tier, r.Channel)
		}
	}
	return p, nil
}
