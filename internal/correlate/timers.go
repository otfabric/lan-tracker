package correlate

import (
	"cmp"
	"container/heap"
	"context"
	"slices"
	"time"

	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/events"
)

// pq is an indexed binary min-heap (container/heap). pos points at an
// item's position plus one, 0 while it is not queued, so an item can be
// moved or removed in O(log n) when its key changes.
type pq[T any] struct {
	items []T
	less  func(a, b T) bool
	pos   func(T) *int
}

func (q *pq[T]) Len() int           { return len(q.items) }
func (q *pq[T]) Less(i, j int) bool { return q.less(q.items[i], q.items[j]) }

func (q *pq[T]) Swap(i, j int) {
	q.items[i], q.items[j] = q.items[j], q.items[i]
	*q.pos(q.items[i]), *q.pos(q.items[j]) = i+1, j+1
}

func (q *pq[T]) Push(x any) {
	t := x.(T)
	q.items = append(q.items, t)
	*q.pos(t) = len(q.items)
}

func (q *pq[T]) Pop() any {
	n := len(q.items) - 1
	t := q.items[n]
	var zero T
	q.items[n] = zero
	q.items = q.items[:n]
	*q.pos(t) = 0
	return t
}

// set queues t, or moves it after its key changed.
func (q *pq[T]) set(t T) {
	if p := *q.pos(t); p > 0 {
		heap.Fix(q, p-1)
		return
	}
	heap.Push(q, t)
}

func (q *pq[T]) remove(t T) {
	if p := *q.pos(t); p > 0 {
		heap.Remove(q, p-1)
	}
}

func (q *pq[T]) peek() (T, bool) {
	if len(q.items) == 0 {
		var zero T
		return zero, false
	}
	return q.items[0], true
}

// hostTimers orders hosts by their next timer, then creation.
func (s *state) hostTimers() *pq[*host] {
	if s.timers.less == nil {
		s.timers.less = func(a, b *host) bool { return a.wake.Before(b.wake) || a.wake.Equal(b.wake) && a.seq < b.seq }
		s.timers.pos = func(h *host) *int { return &h.timerPos }
	}
	return &s.timers
}

// hostAges orders a context's hosts by last_seen, then creation: the first
// is the one missing the longest when any is missing.
func (n *netContext) hostAges() *pq[*host] {
	if n.ages.less == nil {
		n.ages.less = func(a, b *host) bool {
			return a.lastSeen.Before(b.lastSeen) || a.lastSeen.Equal(b.lastSeen) && a.seq < b.seq
		}
		n.ages.pos = func(h *host) *int { return &h.agePos }
	}
	return &n.ages
}

// timing is what the timers depend on; a reload that changes it
// recomputes every host's timers.
type timing struct {
	active, recent, stale, expiry time.Duration
}

func timingOf(cfg *config.Config) timing {
	return timing{cfg.Presence.Active.D(), cfg.Presence.Recent.D(), cfg.Presence.Stale.D(), cfg.Identity.AddressExpiry.D()}
}

func (t timing) presence(lastSeen, now time.Time) Presence {
	age := now.Sub(lastSeen)
	switch {
	case age < t.active:
		return Active
	case age < t.recent:
		return Recent
	case age < t.stale:
		return Stale
	default:
		return Missing
	}
}

// schedule computes the earliest moment after at when advance may change
// h: its next presence threshold (at itself when its state is behind),
// and the expiry of every binding that is not confirmed while the host was
// seen since (§5.3). Waking early is harmless, as a host with nothing due
// is left unchanged; this is never later than the first change.
func (c *Correlator) schedule(h *host, at time.Time) {
	t := c.timing
	var wake time.Time
	ok := false
	earlier := func(x time.Time) {
		if !ok || x.Before(wake) {
			wake, ok = x, true
		}
	}
	if t.presence(h.lastSeen, at) != h.presence {
		earlier(at)
	} else {
		for _, d := range [...]time.Duration{t.active, t.recent, t.stale} {
			if x := h.lastSeen.Add(d); x.After(at) {
				earlier(x)
				break
			}
		}
	}
	for _, b := range h.bindings {
		if h.lastSeen.After(b.lastSeen) {
			earlier(b.lastSeen.Add(t.expiry))
		}
	}
	q := c.st.hostTimers()
	if !ok {
		q.remove(h)
		return
	}
	h.wake = wake
	q.set(h)
}

// advance evaluates presence (§6) and binding expiry (§5.3) up to now.
// Transitions are stamped with the moment the threshold was crossed. Only
// the hosts whose timers are due are evaluated, in creation order, so the
// cost of an observation does not grow with the number of hosts (ADR 0013).
func (c *Correlator) advance(ctx context.Context, now time.Time) {
	if !now.After(c.advancedTo) {
		return
	}
	cfg := c.cfg.Load()
	if t := timingOf(cfg); t != c.timing {
		c.timing = t
		for _, h := range c.st.byID {
			c.st.touch(h)
		}
	}
	for _, h := range c.st.dirty {
		h.dirty = false
		if !h.evicted {
			c.schedule(h, c.advancedTo)
		}
	}
	clear(c.st.dirty)
	c.st.dirty = c.st.dirty[:0]
	c.advancedTo = now
	c.expireExchanges(ctx, now)

	q := c.st.hostTimers()
	due := c.due[:0]
	for {
		h, ok := q.peek()
		if !ok || h.wake.After(now) {
			break
		}
		heap.Pop(q)
		due = append(due, h)
	}
	slices.SortFunc(due, func(a, b *host) int { return cmp.Compare(a.seq, b.seq) })
	for _, h := range due {
		c.expire(ctx, h, now, cfg)
		c.schedule(h, now)
	}
	clear(due)
	c.due = due[:0]
}

// expire applies the due timers of one host.
func (c *Correlator) expire(ctx context.Context, h *host, now time.Time, cfg *config.Config) {
	stale, expiry := c.timing.stale, c.timing.expiry
	// Expiry: unconfirmed for address_expiry while the host was seen since.
	for _, b := range c.bindingsByIP(h) {
		if now.Sub(b.lastSeen) < expiry || !h.lastSeen.After(b.lastSeen) {
			continue
		}
		ended := b.lastSeen.Add(expiry)
		c.close(b, ended)
		ev := h.evidence
		ev.Reason = "address not confirmed for " + cfg.Identity.AddressExpiry.String()
		e := hostEvent(events.IPRemoved, ended, h, causeExpiry, ev)
		e.Old = b.ip.String()
		c.emit(ctx, e)
		c.resolve(ctx, ipKey{h.ctx.id, b.ip}, ended, causeExpiry, 0, ev, h)
	}

	p := c.timing.presence(h.lastSeen, now)
	if p == h.presence {
		return
	}
	h.presence = p
	c.dbHostSeen(ctx, h)
	if p == Missing {
		ev := h.evidence
		ev.Reason = "no observation for " + cfg.Presence.Stale.String()
		// The state just before MISSING is always STALE (thresholds are
		// ordered), however coarsely presence was evaluated.
		e := hostEvent(events.HostDisappeared, h.lastSeen.Add(stale), h, causePresence, ev)
		e.Old, e.New = string(Stale), string(Missing)
		c.emit(ctx, e)
	}
}

// bindingsByIP returns h's open bindings by IP in a buffer the correlator
// reuses (advance runs it for every due host).
func (c *Correlator) bindingsByIP(h *host) []*binding {
	out := c.bindingBuf[:0]
	for _, b := range h.bindings {
		out = append(out, b)
	}
	slices.SortFunc(out, func(a, b *binding) int { return a.ip.Compare(b.ip) })
	c.bindingBuf = out
	return out
}
