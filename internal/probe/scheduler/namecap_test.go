package scheduler

import (
	"net/netip"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/probe"
	"github.com/otfabric/lan-tracker/internal/store"
)

// A name pass carries max_queries_per_pass to the engine, which counts
// every query sent against it; a pass stopped there is cut short, not
// failed.
func TestNamePassStopsAtItsQueryCap(t *testing.T) {
	cfg := baseConfig()
	cfg.Active.ARP.Enabled = false
	cfg.Active.StartupDelay = 0
	cfg.Active.Names = config.NamesConfig{Enabled: true, Interval: config.Duration(30 * time.Minute), MaxQueriesPerPass: 3,
		Resolvers: []netip.Addr{ip("192.168.110.2")}}
	r := newRig(t, cfg)
	r.s.o.Names = nameTargets{store.NameTargets{DNS: []string{"hmi.plant.example"}}}
	r.eng[probe.Name].result = func(p probe.Pass) ([]probe.Result, error) {
		return []probe.Result{{Target: ip("192.168.110.2"), State: probe.Reply}}, probe.ErrPassCap
	}
	r.start()
	r.waiters(1)
	r.sim.Advance(10 * time.Minute)
	if p := r.eng[probe.Name].wait(t); p.MaxQueries != 3 {
		t.Errorf("pass cap = %d, want 3", p.MaxQueries)
	}
	var sum PassSummary
	for deadline := time.Now().Add(5 * time.Second); sum.At.IsZero(); time.Sleep(time.Millisecond) {
		sum = r.s.LastPasses()["eth1"][platform.CollectorNames]
		if time.Now().After(deadline) {
			t.Fatalf("passes = %+v", r.s.LastPasses())
		}
	}
	if sum.Complete || sum.Probed != 1 || sum.Replied != 1 {
		t.Errorf("capped pass = %+v", sum)
	}
	r.waitState(probe.Name, platform.StateRunning)
}
