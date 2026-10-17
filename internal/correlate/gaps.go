package correlate

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/platform"
)

// Collection gaps (docs/DATA_MODEL.md §7, OBSERVATION_GAP): a time during
// which nothing was observed on an interface, because a passive collector
// was not running or the daemon was not. A gap is recorded with its start
// and end once it is over; history inside it is unknown, not quiet.
const (
	// minGap is the shortest daemon downtime recorded as a gap: shorter
	// restarts lose less than the heartbeat's own resolution.
	minGap = time.Minute
	// heartbeatKey is the runtime_state row holding the last time the
	// correlator ran, written every tick and at shutdown.
	heartbeatKey = "observed_until"

	causeDaemon = "daemon"
)

type gapKey struct{ iface, collector string }

type openGap struct {
	since  time.Time
	reason string
}

// collector records a passive collector's state change: leaving running
// opens a gap on its interface, running again closes it. A collector
// disabled by configuration or unsupported is not a gap: nothing was
// meant to run.
func (c *Correlator) collector(ctx context.Context, cs observation.CollectorState) {
	if cs.Collector != platform.CollectorCapture && cs.Collector != platform.CollectorNeighbor {
		return
	}
	k := gapKey{cs.Interface, cs.Collector}
	switch platform.State(cs.State) {
	case platform.StateRunning:
		if g, ok := c.gaps[k]; ok {
			delete(c.gaps, k)
			c.gapEvent(ctx, cs.Interface, cs.Collector, g.since, cs.Time, g.reason)
		}
	case platform.StateFailed:
		g, ok := c.gaps[k]
		if !ok {
			g.since = cs.Time
		}
		g.reason = cs.Error
		c.gaps[k] = g
	default:
		delete(c.gaps, k)
	}
}

// closeGaps records the gaps still open at shutdown, ending now: the
// daemon's own downtime follows from the heartbeat at the next start.
func (c *Correlator) closeGaps(ctx context.Context, now time.Time) {
	keys := make([]gapKey, 0, len(c.gaps))
	for k := range c.gaps {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].iface < keys[j].iface || keys[i].iface == keys[j].iface && keys[i].collector < keys[j].collector
	})
	for _, k := range keys {
		g := c.gaps[k]
		c.gapEvent(ctx, k.iface, k.collector, g.since, now, g.reason)
	}
	clear(c.gaps)
}

// gapEvent emits OBSERVATION_GAP for one interface: old is when the gap
// began, new when it ended.
func (c *Correlator) gapEvent(ctx context.Context, iface, cause string, from, to time.Time, reason string) {
	if !to.After(from) {
		return
	}
	e := events.Event{TS: to, Type: events.ObservationGap, Interface: iface, Old: stamp(from), New: stamp(to), Cause: cause,
		Evidence: events.Evidence{TS: to, Interface: iface, Reason: reason}}
	if n := c.st.contexts[iface]; n != nil {
		e.ContextID = n.id
	}
	c.emit(ctx, e)
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// heartbeat writes the time the correlator last ran.
func (c *Correlator) heartbeat(ctx context.Context, now time.Time) {
	v := strconv.FormatInt(ms(now), 10)
	c.exec(ctx, "heartbeat", `INSERT INTO runtime_state (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, heartbeatKey, v, ms(now))
}

// downtime records the time since the previous run's last heartbeat as a
// gap on every configured interface, if it is at least minGap.
func (c *Correlator) downtime(ctx context.Context, now time.Time) error {
	var last int64
	err := c.store.View(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var v string
		err := tx.QueryRowContext(ctx, `SELECT value FROM runtime_state WHERE key = ?`, heartbeatKey).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		last, _ = strconv.ParseInt(v, 10, 64)
		return nil
	})
	if err != nil || last <= 0 {
		return err
	}
	from := fromMS(last)
	if now.Sub(from) < minGap {
		return nil
	}
	for _, ic := range c.cfg.Load().Interfaces {
		c.gapEvent(ctx, ic.Name, causeDaemon, from, now, "the daemon was not running")
	}
	return nil
}
