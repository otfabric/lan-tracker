package neighbor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/clock"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/platform/fake"
)

// flakySource fails its first Snapshot calls.
type flakySource struct {
	*fake.Neighbors
	failures atomic.Int32
}

func (f *flakySource) Snapshot(ctx context.Context) ([]platform.Neighbor, error) {
	if f.failures.Add(-1) >= 0 {
		return nil, errors.New("netlink: gone")
	}
	return f.Neighbors.Snapshot(ctx)
}

func next(t *testing.T, bus *observation.Bus) observation.Observation {
	t.Helper()
	select {
	case m := <-bus.C():
		return m.Observation
	case <-time.After(5 * time.Second):
		t.Fatal("no observation")
		return observation.Observation{}
	}
}

func state(reg *platform.Registry, want platform.State) bool {
	for _, s := range reg.List() {
		if s.State == want {
			return true
		}
	}
	return false
}

func TestRetriesAfterSnapshotFailure(t *testing.T) {
	sim := clock.NewSim(t0)
	bus := observation.NewBus(16)
	src := &flakySource{Neighbors: &fake.Neighbors{}}
	src.failures.Store(1)
	src.SetTable([]platform.Neighbor{nb("10.0.0.1", "00:1b:1b:00:00:01", "REACHABLE", 0)})
	reg := platform.NewRegistry(sim.Now)
	c := New(Options{Source: src, Bus: bus, Clock: sim, Interfaces: []string{"eth1"}, Resync: time.Hour, Registry: reg})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()

	waitFor(t, func() bool { return state(reg, platform.StateFailed) })
	if got := drain(bus); len(got) != 0 {
		t.Fatalf("emitted during the failure: %+v", got)
	}
	waitFor(t, func() bool { return sim.Waiters() > 1 }) // the resync ticker and the retry timer
	sim.Advance(retryDelay)
	if got := next(t, bus); got.IP.String() != "10.0.0.1" {
		t.Errorf("after the retry: %+v", got)
	}
	waitFor(t, func() bool { return state(reg, platform.StateRunning) })
	cancel()
	<-done
}

func TestCancelDuringRetryEndsRun(t *testing.T) {
	sim := clock.NewSim(t0)
	src := &flakySource{Neighbors: &fake.Neighbors{}}
	src.failures.Store(1000)
	c := New(Options{Source: src, Bus: observation.NewBus(1), Clock: sim, Interfaces: []string{"eth1"}, Resync: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	waitFor(t, func() bool { return sim.Waiters() > 1 })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestResyncTickerAndEventsPickUpChanges(t *testing.T) {
	sim := clock.NewSim(t0)
	bus := observation.NewBus(16)
	src := &fake.Neighbors{}
	src.SetTable([]platform.Neighbor{nb("10.0.0.1", "00:1b:1b:00:00:01", "REACHABLE", 0)})
	c := New(Options{Source: src, Bus: bus, Clock: sim, Interfaces: []string{"eth1"}, Resync: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = c.Run(ctx); close(done) }()
	next(t, bus)

	// Periodic resync.
	src.SetTable([]platform.Neighbor{nb("10.0.0.2", "00:1b:1b:00:00:02", "REACHABLE", 0)})
	sim.Advance(time.Minute)
	if got := next(t, bus); got.IP.String() != "10.0.0.2" {
		t.Errorf("ticker resync: %+v", got)
	}

	// Notification loss asks for a resync.
	src.SetTable([]platform.Neighbor{nb("10.0.0.3", "00:1b:1b:00:00:03", "REACHABLE", 0)})
	src.Emit(platform.NeighborEvent{Resync: true})
	if got := next(t, bus); got.IP.String() != "10.0.0.3" {
		t.Errorf("event resync: %+v", got)
	}

	// After a deletion the same entry is new evidence, not a duplicate.
	same := nb("10.0.0.3", "00:1b:1b:00:00:03", "REACHABLE", 0)
	src.Emit(platform.NeighborEvent{Neighbor: same, Deleted: true})
	src.Emit(platform.NeighborEvent{Neighbor: same})
	if got := next(t, bus); got.IP.String() != "10.0.0.3" {
		t.Errorf("after deletion: %+v", got)
	}
	cancel()
	<-done
}

func TestNewDefaults(t *testing.T) {
	c := New(Options{Source: &fake.Neighbors{}, Bus: observation.NewBus(1)})
	if c.o.Clock == nil || c.o.Logger == nil {
		t.Fatalf("defaults missing: %+v", c.o)
	}
	c.report(platform.StateRunning, nil) // no registry: nothing to do
}
