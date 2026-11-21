package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/lan-tracker/internal/store"
)

// TestListLimitBound: every list read takes a limit of at most
// MaxListLimit and refuses a larger one as a bad query, so the API and
// `--offline` give the same answer; MaxListLimit itself is accepted.
func TestListLimitBound(t *testing.T) {
	st, _, _ := seed(t)
	r := reader(t, st)
	ctx := context.Background()
	reads := map[string]func(limit int) error{
		"observations": func(n int) error {
			_, err := r.Observations(ctx, store.ObservationFilter{Limit: n})
			return err
		},
		"rollups": func(n int) error {
			_, err := r.Rollups(ctx, store.ObservationFilter{Limit: n})
			return err
		},
		"events": func(n int) error {
			_, err := r.Events(ctx, store.EventFilter{Limit: n})
			return err
		},
		"leases": func(n int) error {
			_, err := r.DHCPLeases(ctx, store.DHCPLeaseFilter{Limit: n})
			return err
		},
		"exchanges": func(n int) error {
			_, err := r.DHCPExchanges(ctx, store.DHCPExchangeFilter{Limit: n})
			return err
		},
	}
	for name, read := range reads {
		for _, n := range []int{-1, 0, 1, store.MaxListLimit} {
			if err := read(n); err != nil {
				t.Errorf("%s limit %d: %v", name, n, err)
			}
		}
		for _, n := range []int{store.MaxListLimit + 1, 1_000_000_000} {
			err := read(n)
			if !errors.Is(err, store.ErrBadQuery) || !strings.Contains(err.Error(), "limit at most 10000") {
				t.Errorf("%s limit %d: %v", name, n, err)
			}
		}
	}
}
