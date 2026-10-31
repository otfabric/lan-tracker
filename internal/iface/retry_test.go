package iface

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

// flakyMonitor fails its first List calls.
type flakyMonitor struct {
	*fake.Interfaces
	failures atomic.Int32
}

func (f *flakyMonitor) List(ctx context.Context) ([]platform.Link, error) {
	if f.failures.Add(-1) >= 0 {
		return nil, errors.New("netlink: gone")
	}
	return f.Interfaces.List(ctx)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func failed(reg *platform.Registry) bool {
	for _, s := range reg.List() {
		if s.State == platform.StateFailed {
			return true
		}
	}
	return false
}

func TestRetriesAfterBackendFailure(t *testing.T) {
	sim := clock.NewSim(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	bus := observation.NewBus(16)
	mon := &flakyMonitor{Interfaces: &fake.Interfaces{}}
	mon.failures.Store(1)
	mon.SetLinks([]platform.Link{{Name: "eth1", Index: 2, Up: true}})
	reg := platform.NewRegistry(sim.Now)
	m := New(Options{Monitor: mon, Bus: bus, Clock: sim, Interfaces: []string{"eth1"}, Registry: reg})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = m.Run(ctx); close(done) }()

	// The failure is visible in the registry, and nothing is published.
	waitFor(t, func() bool { return failed(reg) && sim.Waiters() > 0 })
	select {
	case msg := <-bus.C():
		t.Fatalf("published during the failure: %+v", msg)
	default:
	}
	sim.Advance(retryDelay)
	if ls := next(t, bus); ls.Interface != "eth1" || !ls.Present {
		t.Errorf("after the retry: %+v", ls)
	}
	waitFor(t, func() bool { return !failed(reg) })
	cancel()
	<-done
}

func TestCancelDuringRetryEndsRun(t *testing.T) {
	sim := clock.NewSim(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC))
	mon := &flakyMonitor{Interfaces: &fake.Interfaces{}}
	mon.failures.Store(1000)
	m := New(Options{Monitor: mon, Bus: observation.NewBus(1), Clock: sim, Interfaces: []string{"eth1"}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Run(ctx) }()
	waitFor(t, func() bool { return sim.Waiters() > 0 })
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

// A lost notification makes the monitor ask for a resync, which must pick
// up a change no event reported.
func TestResyncEventRepublishes(t *testing.T) {
	bus := observation.NewBus(16)
	mon := &fake.Interfaces{}
	mon.SetLinks([]platform.Link{{Name: "eth1", Index: 2, Up: true}})
	m := New(Options{Monitor: mon, Bus: bus, Interfaces: []string{"eth1"}}) // default clock and logger
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = m.Run(ctx); close(done) }()
	if ls := next(t, bus); !ls.Up {
		t.Fatalf("start-up: %+v", ls)
	}
	mon.SetLinks(nil)
	mon.Emit(platform.LinkEvent{Resync: true})
	if ls := next(t, bus); ls.Interface != "eth1" || ls.Present {
		t.Errorf("resync: %+v", ls)
	}
	cancel()
	<-done
}

// A full bus at shutdown is not an error worth logging, and must not hang.
func TestPublishOnCancelledContextReturns(t *testing.T) {
	m := New(Options{Monitor: &fake.Interfaces{}, Bus: observation.NewBus(0), Interfaces: []string{"eth1"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m.publish(ctx, observation.LinkState{Interface: "eth1"})
}
