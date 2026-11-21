package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Activity bounds (docs/API.md, /v1/activity).
const (
	DefaultActivityBuckets = 96
	MaxActivityBuckets     = 720
	// TopHostsLimit is how many hosts Activity ranks.
	TopHostsLimit = 10
)

// ActivityFilter selects the range and scope of Activity. Since and Until
// are required; Buckets defaults to DefaultActivityBuckets.
type ActivityFilter struct {
	Interface string
	HostID    string
	Since     time.Time
	Until     time.Time
	Buckets   int
}

// Activity is how much happened over a time range, in equal buckets: the
// events per type, the observations per source (raw observations, and the
// hourly roll-ups older ones were folded into, counted in the bucket of
// their hour) and the hosts with the most observations. It counts what was
// recorded; an empty bucket is silence, never proof that nothing was there.
type Activity struct {
	Since        time.Time        `json:"since"`
	Until        time.Time        `json:"until"`
	StepSeconds  int64            `json:"step_seconds"`
	Buckets      int              `json:"buckets"`
	Events       map[string][]int `json:"events"`       // event type → count per bucket
	Observations map[string][]int `json:"observations"` // observation source → count per bucket
	TopHosts     []HostActivity   `json:"top_hosts"`
}

// HostActivity is a host's observation count over an Activity range.
type HostActivity struct {
	HostID        string `json:"host_id"`
	Interface     string `json:"interface"`
	MAC           string `json:"mac"`
	PreferredName string `json:"preferred_name,omitempty"`
	Description   string `json:"description,omitempty"`
	Observations  int    `json:"observations"`
}

// Activity counts events and observations in f's range, per bucket.
func (r *Reader) Activity(ctx context.Context, f ActivityFilter) (Activity, error) {
	if f.Since.IsZero() || f.Until.IsZero() || !f.Until.After(f.Since) {
		return Activity{}, fmt.Errorf("%w: since and until are required, until after since", ErrBadQuery)
	}
	if f.Buckets <= 0 {
		f.Buckets = DefaultActivityBuckets
	}
	if f.Buckets > MaxActivityBuckets {
		return Activity{}, fmt.Errorf("%w: at most %d buckets", ErrBadQuery, MaxActivityBuckets)
	}
	since, until := msOf(f.Since), msOf(f.Until)
	// Whole seconds per bucket, at least one; the last bucket ends at or
	// after until.
	step := (until - since + int64(f.Buckets) - 1) / int64(f.Buckets)
	step = max((step+999)/1000*1000, 1000)
	n := int((until - since + step - 1) / step)
	a := Activity{
		Since: timeOf(since), Until: timeOf(until), StepSeconds: step / 1000, Buckets: n,
		Events: map[string][]int{}, Observations: map[string][]int{}, TopHosts: []HostActivity{},
	}
	bucket := func(ts int64) int { return min(max(int((ts-since)/step), 0), n-1) }

	scope := func(w *where, ctxCol, hostCond string, hostArgs ...any) {
		if f.Interface != "" {
			w.add(ctxCol+` IN (SELECT id FROM network_contexts WHERE interface = ?)`, f.Interface)
		}
		if f.HostID != "" {
			w.add(hostCond, hostArgs...)
		}
	}
	count := func(tx *sql.Tx, into map[string][]int, query string, args ...any) error {
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ts int64
			var key string
			var c int
			if err := rows.Scan(&ts, &key, &c); err != nil {
				return err
			}
			if into[key] == nil {
				into[key] = make([]int, n)
			}
			into[key][bucket(ts)] += c
		}
		return rows.Err()
	}

	err := r.read(ctx, func(tx *sql.Tx) error {
		// Grouped by bucket in SQL; the bucket's start stands for its rows.
		group := fmt.Sprintf(`(%%[1]s - %d) / %d * %d + %d`, since, step, step, since)

		var ew where
		ew.add(`e.ts >= ? AND e.ts < ?`, since, until)
		scope(&ew, "e.context_id", `(e.host_id = ? OR e.related_host_id = ?)`, f.HostID, f.HostID)
		b := fmt.Sprintf(group, "e.ts")
		if err := count(tx, a.Events, `SELECT `+b+`, e.type, count(*) FROM events e`+ew.sql()+` GROUP BY 1, 2`, ew.args...); err != nil {
			return err
		}

		var ow where
		ow.add(`o.ts >= ? AND o.ts < ?`, since, until)
		scope(&ow, "o.context_id", `o.host_id = ?`, f.HostID)
		b = fmt.Sprintf(group, "o.ts")
		if err := count(tx, a.Observations, `SELECT `+b+`, o.source, count(*) FROM observations o`+ow.sql()+` GROUP BY 1, 2`, ow.args...); err != nil {
			return err
		}
		// Roll-ups hold only observations already deleted from the raw
		// table, so the two never count the same observation.
		var rw where
		rw.add(`o.hour >= ? AND o.hour < ?`, since, until)
		scope(&rw, "o.context_id", `o.host_id = ?`, f.HostID)
		b = fmt.Sprintf(group, "o.hour")
		if err := count(tx, a.Observations, `SELECT `+b+`, o.source, sum(o.count) FROM observation_rollups o`+rw.sql()+` GROUP BY 1, 2`, rw.args...); err != nil {
			return err
		}
		if f.HostID != "" {
			return nil
		}
		return r.topHosts(ctx, tx, &a, f.Interface, since, until)
	})
	return a, err
}

// topHosts ranks the hosts by their observations in [since, until).
func (r *Reader) topHosts(ctx context.Context, tx *sql.Tx, a *Activity, iface string, since, until int64) error {
	var w where
	if iface != "" {
		w.add(`c.interface = ?`, iface)
	}
	rows, err := tx.QueryContext(ctx, `SELECT h.host_id, c.interface, h.mac, coalesce(h.preferred_name, ''), coalesce(h.description, ''), sum(x.n)
		FROM (SELECT host_id, count(*) AS n FROM observations WHERE host_id IS NOT NULL AND ts >= ? AND ts < ? GROUP BY host_id
			UNION ALL
			SELECT host_id, sum(count) FROM observation_rollups WHERE host_id IS NOT NULL AND hour >= ? AND hour < ? GROUP BY host_id) x
		JOIN hosts h ON h.host_id = x.host_id JOIN network_contexts c ON c.id = h.context_id`+w.sql()+
		fmt.Sprintf(` GROUP BY h.host_id ORDER BY 6 DESC, h.mac LIMIT %d`, TopHostsLimit),
		append([]any{since, until, since, until}, w.args...)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var h HostActivity
		if err := rows.Scan(&h.HostID, &h.Interface, &h.MAC, &h.PreferredName, &h.Description, &h.Observations); err != nil {
			return err
		}
		a.TopHosts = append(a.TopHosts, h)
	}
	return rows.Err()
}
