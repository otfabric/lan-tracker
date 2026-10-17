package correlate

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/store"
)

func (h *harness) collector(d time.Duration, iface, collector string, state platform.State, err string) {
	h.t.Helper()
	h.sim.Set(h.at(d))
	h.c.Handle(h.ctx, observation.Message{Collector: &observation.CollectorState{Time: h.at(d), Interface: iface,
		Collector: collector, State: string(state), Error: err}})
}

// gapEvents lists "ts interface cause old>new reason" per OBSERVATION_GAP.
func (h *harness) gapEvents() []string {
	return h.query(`SELECT e.ts, coalesce(c.interface, '-'), e.cause, e.old_value, e.new_value, coalesce(json_extract(e.evidence_json, '$.reason'), '')
		FROM events e LEFT JOIN network_contexts c ON c.id = e.context_id WHERE e.type = 'OBSERVATION_GAP' ORDER BY e.id`,
		func(r *sql.Rows) string {
			var ts int64
			var iface, cause, old, nw, reason string
			if err := r.Scan(&ts, &iface, &cause, &old, &nw, &reason); err != nil {
				h.t.Fatal(err)
			}
			return fmt.Sprintf("%s %s %s %s>%s %s", time.UnixMilli(ts).UTC().Sub(t0), iface, cause, old, nw, reason)
		})
}

func at(d time.Duration) string { return t0.Add(d).UTC().Format(time.RFC3339) }

// A passive collector that stops running opens a gap on its interface;
// running again records it, with the last reason. Disabled or unsupported
// collectors, and the probes' collectors, are no gap.
func TestCollectorGaps(t *testing.T) {
	h := newHarness(t)
	h.collector(0, "eth1", platform.CollectorCapture, platform.StateRunning, "")
	h.collector(time.Minute, "eth1", platform.CollectorCapture, platform.StateFailed, "socket closed")
	h.collector(2*time.Minute, "eth1", platform.CollectorCapture, platform.StateFailed, "no such device")
	h.collector(5*time.Minute, "eth1", platform.CollectorCapture, platform.StateRunning, "")
	h.collector(6*time.Minute, "eth0", platform.CollectorNeighbor, platform.StateFailed, "netlink")
	h.collector(7*time.Minute, "eth0", platform.CollectorNeighbor, platform.StateDisabled, "")
	h.collector(8*time.Minute, "eth0", platform.CollectorNeighbor, platform.StateRunning, "")
	h.collector(9*time.Minute, "eth0", platform.CollectorARP, platform.StateFailed, "x")
	h.collector(10*time.Minute, "eth0", platform.CollectorARP, platform.StateRunning, "")
	h.collector(11*time.Minute, "eth9", platform.CollectorCapture, platform.StateFailed, "gone") // not configured
	h.collector(12*time.Minute, "eth0", platform.CollectorCapture, platform.StateFailed, "down")
	h.c.closeGaps(h.ctx, h.at(15*time.Minute)) // shutdown
	expect(t, "gaps", h.gapEvents(), []string{
		"5m0s eth1 capture " + at(time.Minute) + ">" + at(5*time.Minute) + " no such device",
		"15m0s eth0 capture " + at(12*time.Minute) + ">" + at(15*time.Minute) + " down",
		"15m0s - capture " + at(11*time.Minute) + ">" + at(15*time.Minute) + " gone",
	})
	if spec, ok := events.Lookup(events.ObservationGap); !ok || spec.Legacy {
		t.Error("OBSERVATION_GAP is not a current event type")
	}
}

// The daemon's own downtime, from the last heartbeat to the next start, is
// a gap on every configured interface; a quick restart and replay are not.
func TestDowntimeGap(t *testing.T) {
	h := newHarness(t)
	h.c.heartbeat(h.ctx, h.at(0))
	h.flush()
	live := func(d time.Duration) *Correlator {
		t.Helper()
		if err := h.st.Close(); err != nil {
			t.Fatal(err)
		}
		h.sim.Set(h.at(d))
		st, err := store.Open(h.ctx, store.Options{Path: h.path, Clock: h.sim})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = st.Close() })
		log := slog.New(slog.DiscardHandler)
		c, err := New(context.Background(), Options{Store: st, Events: events.NewEngine(st, log, nil), Clock: h.sim, Logger: log,
			Config: testConfig()})
		if err != nil {
			t.Fatal(err)
		}
		h.st, h.c = st, c
		return c
	}
	live(30 * time.Second) // within minGap: no gap; the heartbeat moves on
	if g := h.gapEvents(); len(g) != 0 {
		t.Errorf("quick restart = %v", g)
	}
	live(20 * time.Minute)
	expect(t, "gaps", h.gapEvents(), []string{
		"20m0s eth0 daemon " + at(30*time.Second) + ">" + at(20*time.Minute) + " the daemon was not running",
		"20m0s eth1 daemon " + at(30*time.Second) + ">" + at(20*time.Minute) + " the daemon was not running",
	})
	// Run's tick and shutdown write the heartbeat.
	bus := observation.NewBus(1)
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan struct{})
	go func() { _ = h.c.Run(ctx, bus); close(done) }()
	h.sim.Advance(DefaultTick)
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done
	got := h.query(`SELECT value FROM runtime_state WHERE key = 'observed_until'`, scanText)
	if want := fmt.Sprint(h.sim.Now().UnixMilli()); len(got) != 1 || got[0] != want {
		t.Errorf("heartbeat = %v, want %s", got, want)
	}
	h.sim.Advance(time.Hour)
	h.restart() // replay (the harness is data-driven): no gap
	if n := len(h.gapEvents()); n != 2 {
		t.Errorf("%d gaps after a replay start", n)
	}
}
