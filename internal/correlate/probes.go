package correlate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/observation"
)

// causeOperator is the cause of kill-switch and scan events.
const causeOperator = "operator"

// operator records an operator action (§7, §8): the kill switch in
// runtime_state, operator scans in scans, and their events.
func (c *Correlator) operator(ctx context.Context, op observation.Operator) {
	c.advance(ctx, op.Time)
	ev := events.Evidence{TS: op.Time.UTC(), Actor: op.Actor, Reason: op.Reason}
	switch op.Kind {
	case observation.OpActiveDisabled:
		if op.Persist {
			v, _ := json.Marshal(map[string]any{"disabled": true, "reason": op.Reason, "by": op.Actor, "at": op.Time.UTC()})
			c.exec(ctx, "kill switch", `INSERT INTO runtime_state (key, value, updated_at) VALUES ('active_disabled', ?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, string(v), ms(op.Time))
		}
		c.emit(ctx, events.Event{TS: op.Time, Type: events.ActiveDisabled, New: switchText("disabled", op), Cause: causeOperator, Evidence: ev})
	case observation.OpActiveEnabled:
		if op.Persist {
			c.exec(ctx, "kill switch", `DELETE FROM runtime_state WHERE key = 'active_disabled'`)
		}
		c.emit(ctx, events.Event{TS: op.Time, Type: events.ActiveEnabled, New: switchText("enabled", op), Cause: causeOperator, Evidence: ev})
	case observation.OpScanStarted, observation.OpScanCompleted:
		c.scan(ctx, op, ev)
	case observation.OpHostDescribed:
		c.describe(ctx, op, ev)
	}
}

// describe sets or removes a host's description (§5.8). A change emits
// HOST_DESCRIBED with the old and new text and the operator; the same text
// again changes nothing. An unknown host is ignored: the API checks that it
// exists before asking.
func (c *Correlator) describe(ctx context.Context, op observation.Operator, ev events.Evidence) {
	h := c.hostByID(op.HostID)
	if h == nil || h.description == op.Description {
		return
	}
	old := h.description
	h.description = op.Description
	c.exec(ctx, "host description", `UPDATE hosts SET description = ? WHERE host_id = ?`, nullString(op.Description), h.id)
	ev.Interface, ev.MAC = h.ctx.iface, h.mac.String()
	e := hostEvent(events.HostDescribed, op.Time, h, causeOperator, ev)
	e.Old, e.New = old, op.Description
	c.emit(ctx, e)
}

func switchText(what string, op observation.Operator) string {
	s := "active discovery " + what
	if op.Actor != "" {
		s += " by " + op.Actor
	}
	if op.Reason != "" {
		s += ": " + op.Reason
	}
	return s
}

func (c *Correlator) scan(ctx context.Context, op observation.Operator, ev events.Evidence) {
	sc := op.Scan
	if sc == nil {
		return
	}
	n := c.st.contexts[sc.Interface]
	if n == nil {
		return
	}
	if sc.Handle == nil {
		return
	}
	ev.Interface = n.iface
	e := events.Event{TS: op.Time, ContextID: n.id, Interface: n.iface, New: sc.Summary, Cause: causeOperator, Evidence: ev}
	if op.Kind == observation.OpScanStarted {
		c.st.nextScanID++
		sc.Handle.ID = c.st.nextScanID
		c.exec(ctx, "insert scan", `INSERT INTO scans (id, context_id, kind, trigger, started_at, targets) VALUES (?, ?, ?, 'operator', ?, ?)`,
			sc.Handle.ID, n.id, sc.Kind, ms(op.Time), sc.Targets)
		e.Type = events.ScanStarted
	} else {
		if sc.Handle.ID == 0 {
			return // its start was never recorded
		}
		results, err := json.Marshal(sc.Results)
		if err != nil {
			results = fmt.Appendf(nil, `{"error": %q}`, err.Error())
		}
		c.exec(ctx, "finish scan", `UPDATE scans SET finished_at = ?, results_json = ? WHERE id = ?`, ms(op.Time), string(results), sc.Handle.ID)
		e.Type = events.ScanCompleted
	}
	c.emit(ctx, e)
}

// abortedScan is the result of a scan the daemon stopped in the middle of.
const abortedScan = "the daemon stopped during the scan"

// closeOpenScans finishes the scans a previous run left open (a crash or
// power loss mid-scan): finished at their start, aborted, with
// SCAN_COMPLETED so every start in the history has an end.
func (c *Correlator) closeOpenScans(ctx context.Context, now time.Time) {
	for _, sc := range c.st.openScans {
		n := c.st.contextsByID[sc.ctx]
		results, _ := json.Marshal(map[string]any{"aborted": abortedScan})
		c.exec(ctx, "close scan", `UPDATE scans SET finished_at = started_at, results_json = ? WHERE id = ?`, string(results), sc.id)
		if n == nil {
			continue
		}
		c.emit(ctx, events.Event{TS: now, Type: events.ScanCompleted, ContextID: n.id, Interface: n.iface,
			New: "operator scan " + sc.kind + " aborted: " + abortedScan, Cause: causeOperator,
			Evidence: events.Evidence{TS: now, Interface: n.iface, Reason: abortedScan}})
	}
	c.st.openScans = nil
}
