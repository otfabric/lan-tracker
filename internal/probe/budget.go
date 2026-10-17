// Package probe holds what every active probe engine shares
// (docs/ARCHITECTURE.md §5): the safety budget, the kill switch, target
// enumeration, back-off and the scan planner. Engines (arp, icmp, tcp)
// build packets themselves, send through platform.Transmitter and only emit
// observations; the scheduler runs them.
//
// The budget counts probe starts (ADR 0012): one ARP request, ICMP echo,
// name query or TCP connect initiation is one start. The kernel's own
// packets (TCP handshake ACKs, retransmissions, the close, ACKs of data a
// server sends) are bounded by timeouts and concurrency, not paced.
package probe

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/otfabric/lan-tracker/internal/clock"
	"github.com/otfabric/lan-tracker/internal/config"
)

// Protocol names a probe engine.
type Protocol string

// Probe protocols. IPv6 NDP is deferred (v1 is IPv4-first). Name is a
// DNS or mDNS query (FR-AC-12): paced by its own rate and the global one,
// but not by the target spacing, which is for probes of one device.
const (
	ARP  Protocol = "arp"
	ICMP Protocol = "icmp"
	TCP  Protocol = "tcp"
	Name Protocol = "name"
)

// paceMargin paces sends slightly below the budget, and window is the
// sliding window a budget is enforced over: slightly longer than a second,
// so a send that leaves a few milliseconds late cannot put one probe too
// many into any one-second window.
const paceMargin = 1.02

const window = time.Duration(paceMargin * float64(time.Second))

// Errors returned by Budget.Acquire.
var (
	ErrDisabled = errors.New("active discovery is disabled")
	ErrRefused  = errors.New("probe refused")
)

// Policy decides whether a probe may be sent at all; Budget asks it before
// reserving a slot and again right before the send (NFR-SAFE-4).
type Policy interface {
	Check(iface string, p Protocol, target netip.Addr) error
}

// Limits are the safety limits of docs/ARCHITECTURE.md §5.
type Limits struct {
	GlobalStarts    float64              // probe starts/s across every engine and interface
	Rates           map[Protocol]float64 // starts/s; TCP in connects/s
	MaxConcurrent   int
	TCPPerInterface int
	TCPPerHost      int
	TargetSpacing   time.Duration
}

// LimitsFrom reads the limits from the configuration.
func LimitsFrom(a config.ActiveConfig) Limits {
	return Limits{
		GlobalStarts: a.MaxStartsPerSecond,
		Rates: map[Protocol]float64{
			ARP: a.Budgets.ARP.StartsPerSecond, ICMP: a.Budgets.ICMP.StartsPerSecond, TCP: a.Budgets.TCP.ConnectsPerSecond,
			Name: a.Budgets.Names.StartsPerSecond,
		},
		MaxConcurrent: a.MaxConcurrentJobs, TCPPerInterface: a.Budgets.TCP.MaxConcurrentPerInterface,
		TCPPerHost: a.Budgets.TCP.MaxConcurrentPerHost, TargetSpacing: a.MinTargetInterval.D(),
	}
}

type hostKey struct {
	iface string
	ip    netip.Addr
}

// bucket is one rate budget: sends are paced (the next is due cost/rate
// after the previous, so there are no bursts) and at most rate tokens are
// spent in any window.
type bucket struct {
	next time.Time
	log  []spent // sends within the last window, oldest first
}

type spent struct {
	at   time.Time
	cost float64
}

// earliest returns the first time from t on at which cost fits.
func (k *bucket) earliest(t time.Time, cost, rate float64) time.Time {
	if rate <= 0 {
		return t
	}
	if k.next.After(t) {
		t = k.next
	}
	for {
		sum, oldest := 0.0, time.Time{}
		for _, s := range k.log {
			if s.at.After(t.Add(-window)) {
				if oldest.IsZero() || s.at.Before(oldest) {
					oldest = s.at
				}
				sum += s.cost
			}
		}
		if oldest.IsZero() || sum+cost <= rate {
			return t
		}
		t = oldest.Add(window) // the oldest send leaves the window
	}
}

func (k *bucket) book(at time.Time, cost, rate float64) {
	if rate <= 0 {
		return
	}
	k.next = at.Add(interval(cost, rate))
	kept := k.log[:0]
	for _, s := range k.log {
		if s.at.After(at.Add(-window)) {
			kept = append(kept, s)
		}
	}
	k.log = append(kept, spent{at, cost})
}

// move shifts the send booked at from to to, when it left late.
func (k *bucket) move(from, to time.Time, cost, rate float64) {
	if rate <= 0 {
		return
	}
	for i := len(k.log) - 1; i >= 0; i-- {
		if k.log[i].at.Equal(from) && k.log[i].cost == cost {
			k.log[i].at = to
			break
		}
	}
	if next := to.Add(interval(cost, rate)); next.After(k.next) {
		k.next = next
	}
}

// Budget enforces the limits across every engine and interface: one global
// start budget, one per protocol, a global concurrency cap, the TCP caps
// per interface and per host, and the minimum spacing between two probes to
// one target.
type Budget struct {
	clock  clock.Clock
	policy Policy

	mu        sync.Mutex
	limits    Limits
	global    bucket
	proto     map[Protocol]*bucket
	target    map[hostKey]time.Time
	slots     chan struct{}
	tcpIface  map[string]chan struct{}
	tcpHost   map[hostKey]chan struct{}
	throttled map[Throttle]uint64
}

// Throttle is a reason a probe waited, for
// lan_tracker_probe_throttled_total{protocol,reason}.
type Throttle struct {
	Protocol Protocol
	Reason   string // global, protocol, target_spacing, concurrency, tcp_interface, tcp_host
}

// NewBudget returns a budget enforcing limits, asking policy before every
// send.
func NewBudget(c clock.Clock, limits Limits, policy Policy) *Budget {
	if c == nil {
		c = clock.Real()
	}
	return &Budget{
		clock: c, policy: policy, limits: limits, proto: map[Protocol]*bucket{}, target: map[hostKey]time.Time{},
		slots: make(chan struct{}, max(limits.MaxConcurrent, 1)), tcpIface: map[string]chan struct{}{},
		tcpHost: map[hostKey]chan struct{}{}, throttled: map[Throttle]uint64{},
	}
}

// Throttled returns how often probes waited, per protocol and reason.
func (b *Budget) Throttled() map[Throttle]uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[Throttle]uint64, len(b.throttled))
	for k, v := range b.throttled {
		out[k] = v
	}
	return out
}

func (b *Budget) count(p Protocol, reason string) {
	b.mu.Lock()
	b.throttled[Throttle{p, reason}]++
	b.mu.Unlock()
}

func (b *Budget) ifaceSem(iface string) chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := b.tcpIface[iface]
	if ch == nil {
		ch = make(chan struct{}, max(b.limits.TCPPerInterface, 1))
		b.tcpIface[iface] = ch
	}
	return ch
}

func (b *Budget) hostSem(k hostKey) chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := b.tcpHost[k]
	if ch == nil {
		ch = make(chan struct{}, max(b.limits.TCPPerHost, 1))
		b.tcpHost[k] = ch
	}
	return ch
}

// take acquires one slot of a semaphore, counting a wait.
func (b *Budget) take(ctx context.Context, ch chan struct{}, p Protocol, reason string) error {
	select {
	case ch <- struct{}{}:
		return nil
	default:
	}
	b.count(p, reason)
	select {
	case ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Acquire waits until a probe of protocol p to target on iface may be sent
// and returns a release function, to call once the probe is done (reply,
// timeout or connection closed). It holds a concurrency slot (and for TCP
// the interface and host slots) until then. The policy is checked before
// the wait and again just before returning.
func (b *Budget) Acquire(ctx context.Context, p Protocol, iface string, target netip.Addr) (func(), error) {
	return b.acquire(ctx, p, iface, target)
}

func (b *Budget) acquire(ctx context.Context, p Protocol, iface string, target netip.Addr) (func(), error) {
	if err := b.check(iface, p, target); err != nil {
		return nil, err
	}
	var held []chan struct{}
	release := func() {
		for i := len(held) - 1; i >= 0; i-- {
			<-held[i]
		}
	}
	if err := b.take(ctx, b.slots, p, "concurrency"); err != nil {
		return nil, err
	}
	held = append(held, b.slots)
	if p == TCP {
		ifs := b.ifaceSem(iface)
		if err := b.take(ctx, ifs, p, "tcp_interface"); err != nil {
			release()
			return nil, err
		}
		held = append(held, ifs)
		hs := b.hostSem(hostKey{iface, target})
		if err := b.take(ctx, hs, p, "tcp_host"); err != nil {
			release()
			return nil, err
		}
		held = append(held, hs)
	}
	// Wait for the target's spacing before booking the budgets, so a probe
	// that must wait for its target does not hold up probes to others.
	if due := b.spacing(iface, target); p != Name && due.After(b.clock.Now()) {
		b.count(p, "target_spacing")
		if err := b.sleep(ctx, due.Sub(b.clock.Now())); err != nil {
			release()
			return nil, err
		}
	}
	at, reason := b.reserve(p, iface, target)
	if wait := at.Sub(b.clock.Now()); wait > 0 {
		b.count(p, reason)
		if err := b.sleep(ctx, wait); err != nil {
			release()
			return nil, err
		}
	}
	if err := b.check(iface, p, target); err != nil {
		release()
		return nil, err
	}
	b.settle(p, iface, target, at)
	return release, nil
}

// settle moves the booking to the actual send time when the probe leaves
// late (a timer that fired late, a goroutine that was not scheduled at
// once): the budgets then count when probes really leave, so a late send
// cannot crowd the next ones into one window.
func (b *Budget) settle(p Protocol, iface string, target netip.Addr, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()
	if !now.After(at) {
		return
	}
	if pb := b.proto[p]; pb != nil {
		pb.move(at, now, 1, b.limits.Rates[p])
	}
	b.global.move(at, now, 1, b.limits.GlobalStarts)
	k := hostKey{iface, target}
	if t := now.Add(b.limits.TargetSpacing); p != Name && t.After(b.target[k]) {
		b.target[k] = t
	}
}

// sleep waits d on the budget's clock or until ctx ends.
func (b *Budget) sleep(ctx context.Context, d time.Duration) error {
	t := b.clock.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// spacing returns when the next probe to target may leave.
func (b *Budget) spacing(iface string, target netip.Addr) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.target[hostKey{iface, target}]
}

func (b *Budget) check(iface string, p Protocol, target netip.Addr) error {
	if b.policy == nil {
		return nil
	}
	return b.policy.Check(iface, p, target)
}

// reserve books the send time: the earliest moment that respects the
// target's spacing and the protocol and global budgets, booked in all of
// them in one step so the time booked everywhere is the time of the send.
// Every probe is one start of each. It returns the binding constraint.
func (b *Budget) reserve(p Protocol, iface string, target netip.Addr) (time.Time, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()
	pb := b.proto[p]
	if pb == nil {
		pb = &bucket{}
		b.proto[p] = pb
	}
	k := hostKey{iface, target}
	at, reason := now, ""
	spacing := b.target[k]
	if p == Name {
		spacing = time.Time{}
	}
	for _, c := range []struct {
		t      time.Time
		reason string
	}{
		{spacing, "target_spacing"},
		{pb.earliest(now, 1, b.limits.Rates[p]), "protocol"},
		{b.global.earliest(now, 1, b.limits.GlobalStarts), "global"},
	} {
		if c.t.After(at) {
			at, reason = c.t, c.reason
		}
	}
	if p != Name {
		b.target[k] = at.Add(b.limits.TargetSpacing)
	}
	pb.book(at, 1, b.limits.Rates[p])
	b.global.book(at, 1, b.limits.GlobalStarts)
	b.prune(now)
	return at, reason
}

// prune drops spacing entries long past, so the map stays small.
func (b *Budget) prune(now time.Time) {
	if len(b.target) < 4096 {
		return
	}
	for k, t := range b.target {
		if t.Before(now) {
			delete(b.target, k)
		}
	}
}

func interval(cost, rate float64) time.Duration {
	return time.Duration(cost / rate * paceMargin * float64(time.Second))
}

// SetLimits applies reloaded limits to future reservations. The
// concurrency caps keep their size until restart; the reload answer lists
// them as needing one (config.RestartKeys).
func (b *Budget) SetLimits(l Limits) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l.MaxConcurrent, l.TCPPerInterface, l.TCPPerHost = b.limits.MaxConcurrent, b.limits.TCPPerInterface, b.limits.TCPPerHost
	b.limits = l
}

// refusal wraps a policy refusal with its reason.
func refusal(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrRefused}, args...)...)
}

// Simulate returns how long the probes take under the limits if the first
// could start at start, as Acquire paces them: each waits for its target's
// spacing, the budgets and a free concurrency slot, and holds that slot
// for its Hold (its reply wait). Phases run one after the other, each
// starting once the previous one's last probe is done, as a scan's phases
// do. It returns when the last probe is done. The planner uses it to
// estimate scan durations.
func Simulate(limits Limits, start time.Time, phases ...[]SimProbe) time.Duration {
	sim := clock.NewSim(start)
	b := NewBudget(sim, limits, nil)
	slots := max(limits.MaxConcurrent, 1)
	end := start
	for _, probes := range phases {
		sim.Set(end)
		var busy []time.Time // when held slots free up, earliest first
		for _, p := range probes {
			t := sim.Now()
			if len(busy) >= slots {
				if busy[0].After(t) {
					t = busy[0]
				}
				busy = busy[1:]
			}
			if due := b.spacing(p.Interface, p.Target); due.After(t) && p.Protocol != Name {
				t = due
			}
			sim.Set(t)
			at, _ := b.reserve(p.Protocol, p.Interface, p.Target)
			sim.Set(at)
			done := at.Add(p.Hold)
			i := sort.Search(len(busy), func(i int) bool { return busy[i].After(done) })
			busy = slices.Insert(busy, i, done)
			if done.After(end) {
				end = done
			}
		}
	}
	return end.Sub(start)
}

// SimProbe is one probe of a simulated run.
type SimProbe struct {
	Protocol  Protocol
	Interface string
	Target    netip.Addr
	Hold      time.Duration // how long the probe holds its concurrency slot
}
