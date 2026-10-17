// Package correlate is the identity engine: a single goroutine that turns
// observations into host state, address bindings and events following
// docs/DATA_MODEL.md §5–§6. It is the only writer of state and events.
//
// Time is data-driven: every rule is evaluated at the observation's time, and
// timer-driven transitions (presence, binding expiry) are stamped with the
// moment their threshold was crossed, so results do not depend on when the
// evaluation runs. A replay therefore produces the same database every time.
package correlate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/otfabric/lan-tracker/internal/clock"
	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/identify"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/store"
)

// DefaultTick is how often live mode evaluates presence and expiry.
const DefaultTick = 15 * time.Second

// Internal event causes (docs/DATA_MODEL.md §2).
const (
	causePresence  = "presence"
	causeExpiry    = "expiry"
	causeIface     = "iface_monitor"
	causeIntegrity = "integrity_check"
)

// Options configures a Correlator.
type Options struct {
	Store   *store.Store
	Events  *events.Engine
	Vendors *identify.Vendors
	Clock   clock.Clock
	Logger  *slog.Logger
	Config  *config.Config
	// DataDriven disables the ticker: timers advance only with observation
	// times (replay).
	DataDriven bool
	Tick       time.Duration
	// NewID returns host ids; default UUIDv7.
	NewID func() string
	// Recovery, if set, is the corrupt database the store quarantined at
	// start-up; New records it as the new database's first event.
	Recovery *store.Recovery
}

// Correlator is the identity engine.
type Correlator struct {
	store   *store.Store
	events  *events.Engine
	vendors *identify.Vendors
	clock   clock.Clock
	log     *slog.Logger
	cfg     atomic.Pointer[config.Config]
	data    bool
	tick    time.Duration
	newID   func() string

	st         *state
	advancedTo time.Time
	gaps       map[gapKey]openGap // passive collectors not running, since when

	unbound atomic.Uint64
	ignored atomic.Uint64
	// Per source: observations processed and those left unbound
	// (lan_tracker_observations_total, lan_tracker_observations_unbound_total).
	bySource        map[observation.Source]*atomic.Uint64
	unboundBySource map[observation.Source]*atomic.Uint64
}

// New loads the current state from the store and makes sure every
// configured interface has a network context.
func New(ctx context.Context, o Options) (*Correlator, error) {
	if o.Store == nil || o.Events == nil || o.Config == nil {
		return nil, errors.New("correlate: store, events and config are required")
	}
	if o.Clock == nil {
		o.Clock = clock.Real()
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Tick <= 0 {
		o.Tick = DefaultTick
	}
	if o.NewID == nil {
		o.NewID = func() string { return uuid.Must(uuid.NewV7()).String() }
	}
	c := &Correlator{
		store: o.Store, events: o.Events, vendors: o.Vendors, clock: o.Clock, log: o.Logger,
		data: o.DataDriven, tick: o.Tick, newID: o.NewID, st: newState(), gaps: map[gapKey]openGap{},
		bySource: map[observation.Source]*atomic.Uint64{}, unboundBySource: map[observation.Source]*atomic.Uint64{},
	}
	for _, src := range observation.Sources {
		c.bySource[src], c.unboundBySource[src] = new(atomic.Uint64), new(atomic.Uint64)
	}
	c.SetConfig(o.Config)
	if err := o.Store.View(ctx, func(ctx context.Context, tx *sql.Tx) error { return c.st.load(ctx, tx) }); err != nil {
		return nil, err
	}
	now := c.clock.Now()
	if r := o.Recovery; r != nil {
		c.emit(ctx, events.Event{TS: now, Type: events.DatabaseRecreated, New: r.QuarantinedTo, Cause: causeIntegrity,
			Evidence: events.Evidence{TS: now, Reason: r.Reason}})
	}
	c.closeOpenScans(ctx, now)
	// Exchanges the previous run left open were never answered as far as
	// anyone saw; the in-memory exchange state did not survive.
	c.exec(ctx, "close dhcp exchanges", `UPDATE dhcp_exchanges SET outcome = 'incomplete'
		WHERE outcome IN ('discover', 'request', 'inform')`)
	for _, ic := range o.Config.Interfaces {
		if _, ok := c.st.contexts[ic.Name]; ok {
			continue
		}
		c.st.nextContextID++
		n := &netContext{id: c.st.nextContextID, iface: ic.Name, prefixes: map[netip.Prefix]*prefixRow{}, lastSeen: now}
		c.st.contexts[n.iface], c.st.contextsByID[n.id] = n, n
		c.dbInsertContext(ctx, n, now)
	}
	if !c.data { // replay has no downtime of its own
		if err := c.downtime(ctx, now); err != nil {
			return nil, fmt.Errorf("correlate: last heartbeat: %w", err)
		}
		c.heartbeat(ctx, now)
	}
	return c, nil
}

// SetConfig applies a reloaded configuration: thresholds, and DHCP
// allowlists at each server's next reply (the set of interfaces is
// restart-only).
func (c *Correlator) SetConfig(cfg *config.Config) { c.cfg.Store(cfg) }

// Unbound is how many observations matched no host
// (lan_tracker_observations_unbound_total).
func (c *Correlator) Unbound() uint64 { return c.unbound.Load() }

// Ignored is how many observations were dropped as unusable (unknown
// interface, no usable MAC or IP).
func (c *Correlator) Ignored() uint64 { return c.ignored.Load() }

// SourceCounts returns, per source, how many observations were processed
// and how many of them matched no host.
func (c *Correlator) SourceCounts() (processed, unbound map[observation.Source]uint64) {
	processed, unbound = map[observation.Source]uint64{}, map[observation.Source]uint64{}
	for src, n := range c.bySource {
		processed[src] = n.Load()
		unbound[src] = c.unboundBySource[src].Load()
	}
	return processed, unbound
}

func (c *Correlator) count(src observation.Source, counters map[observation.Source]*atomic.Uint64) {
	if n := counters[src]; n != nil {
		n.Add(1)
	}
}

// Run consumes the bus until ctx is cancelled.
func (c *Correlator) Run(ctx context.Context, bus *observation.Bus) error {
	var tick <-chan time.Time
	if !c.data {
		t := c.clock.NewTicker(c.tick)
		defer t.Stop()
		tick = t.C()
	}
	for {
		select {
		case <-ctx.Done():
			bg := context.WithoutCancel(ctx)
			if !c.data {
				now := c.clock.Now()
				c.closeGaps(bg, now)
				c.heartbeat(bg, now)
			}
			c.flushContexts(bg)
			return nil
		case m := <-bus.C():
			c.Handle(ctx, m)
		case <-tick:
			now := c.clock.Now()
			c.advance(ctx, now)
			c.flushContexts(ctx)
			c.heartbeat(ctx, now)
		}
	}
}

// Handle processes one bus message. Run calls it; tests may call it
// directly.
func (c *Correlator) Handle(ctx context.Context, m observation.Message) {
	switch {
	case m.Barrier != nil:
		c.flushContexts(ctx)
		close(m.Barrier)
	case m.Link != nil:
		c.link(ctx, *m.Link)
	case m.Operator != nil:
		c.operator(ctx, *m.Operator)
	case m.Collector != nil:
		c.collector(ctx, *m.Collector)
	default:
		c.observe(ctx, m.Observation)
	}
}

func (c *Correlator) flushContexts(ctx context.Context) {
	for _, n := range c.st.contextsByID {
		if n.dirty {
			c.dbContextSeen(ctx, n)
			n.dirty = false
		}
	}
}

func (c *Correlator) emit(ctx context.Context, ev events.Event) {
	if err := c.events.Emit(ctx, ev); err != nil && ctx.Err() == nil {
		c.log.Error("correlator: emitting event failed", "type", ev.Type, "err", err)
	}
}

// hostEvent fills the host-related fields of an event.
func hostEvent(t events.Type, ts time.Time, h *host, cause string, ev events.Evidence) events.Event {
	return events.Event{
		TS: ts, Type: t, ContextID: h.ctx.id, Interface: h.ctx.iface, HostID: h.id, MAC: h.mac.String(),
		Cause: cause, Evidence: ev,
	}
}

// presenceAt computes presence from the time since the last observation.
func (c *Correlator) presenceAt(lastSeen, now time.Time) Presence {
	p := c.cfg.Load().Presence
	age := now.Sub(lastSeen)
	switch {
	case age < p.Active.D():
		return Active
	case age < p.Recent.D():
		return Recent
	case age < p.Stale.D():
		return Stale
	default:
		return Missing
	}
}

func (c *Correlator) refTime(t time.Time) time.Time {
	if c.advancedTo.After(t) {
		return c.advancedTo
	}
	return t
}

func describe(p netip.Prefix) string { return fmt.Sprint(p) }
