package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/lan-tracker/internal/store"
)

func sum(m map[string][]int) int {
	n := 0
	for _, counts := range m {
		for _, c := range counts {
			n += c
		}
	}
	return n
}

// TestActivity checks that the buckets add up to what the list queries
// return over the same range, overall, per interface and per host.
func TestActivity(t *testing.T) {
	st, _, exp := seed(t)
	r := reader(t, st)
	ctx := context.Background()
	since, until := t0, t0.Add(5*time.Hour)

	a, err := r.Activity(ctx, store.ActivityFilter{Since: since, Until: until, Buckets: 5})
	if err != nil {
		t.Fatal(err)
	}
	if a.StepSeconds != 3600 || a.Buckets != 5 || !a.Since.Equal(since) || !a.Until.Equal(until) {
		t.Fatalf("range: %+v", a)
	}
	evs, err := r.Events(ctx, store.EventFilter{Since: since, Until: until.Add(-time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	if got := sum(a.Events); got != len(evs) || got == 0 {
		t.Errorf("events: %d in buckets, %d listed", got, len(evs))
	}
	for typ, counts := range a.Events {
		if len(counts) != 5 {
			t.Errorf("%s: %d buckets", typ, len(counts))
		}
	}
	obs, err := r.Observations(ctx, store.ObservationFilter{Since: since, Until: until, Limit: store.MaxListLimit})
	if err != nil {
		t.Fatal(err)
	}
	if got := sum(a.Observations); got != len(obs) || got == 0 {
		t.Errorf("observations: %d in buckets, %d listed", got, len(obs))
	}
	// The first observation (10:00) is in the first bucket.
	if a.Observations["passive_arp"][0] == 0 {
		t.Errorf("passive_arp: %v", a.Observations["passive_arp"])
	}
	if len(a.TopHosts) == 0 || a.TopHosts[0].Observations == 0 {
		t.Fatalf("top hosts: %+v", a.TopHosts)
	}
	for i := 1; i < len(a.TopHosts); i++ {
		if a.TopHosts[i].Observations > a.TopHosts[i-1].Observations {
			t.Errorf("top hosts not ranked: %+v", a.TopHosts)
		}
	}

	// One host: its events and observations, no ranking.
	top := a.TopHosts[0]
	h, err := r.Activity(ctx, store.ActivityFilter{HostID: top.HostID, Since: since, Until: until})
	if err != nil {
		t.Fatal(err)
	}
	if got := sum(h.Observations); got != top.Observations {
		t.Errorf("host %s: %d observations, ranked with %d", label(exp, top.MAC), got, top.Observations)
	}
	hevs, err := r.Events(ctx, store.EventFilter{HostID: top.HostID, Since: since, Until: until.Add(-time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	if got := sum(h.Events); got != len(hevs) || len(h.TopHosts) != 0 {
		t.Errorf("host events: %d in buckets, %d listed; top %+v", got, len(hevs), h.TopHosts)
	}
	if h.Buckets != store.DefaultActivityBuckets {
		t.Errorf("default buckets: %d", h.Buckets)
	}

	// An interface without data counts nothing.
	none, err := r.Activity(ctx, store.ActivityFilter{Interface: "eth9", Since: since, Until: until})
	if err != nil || sum(none.Events)+sum(none.Observations)+len(none.TopHosts) != 0 {
		t.Errorf("eth9: %v %+v", err, none)
	}
}

func TestActivityBadQuery(t *testing.T) {
	st, _, _ := seed(t)
	r := reader(t, st)
	for name, f := range map[string]store.ActivityFilter{
		"no range":      {},
		"until first":   {Since: t0, Until: t0.Add(-time.Hour)},
		"empty range":   {Since: t0, Until: t0},
		"too many":      {Since: t0, Until: t0.Add(time.Hour), Buckets: store.MaxActivityBuckets + 1},
		"no since only": {Until: t0},
	} {
		if _, err := r.Activity(context.Background(), f); !errors.Is(err, store.ErrBadQuery) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A range shorter than the buckets gets one-second buckets.
	a, err := r.Activity(context.Background(), store.ActivityFilter{Since: t0, Until: t0.Add(10 * time.Second), Buckets: 100})
	if err != nil || a.StepSeconds != 1 || a.Buckets != 10 {
		t.Errorf("short range: %v %+v", err, a)
	}
}
