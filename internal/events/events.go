// Package events defines the event catalogue (docs/DATA_MODEL.md §7) and the
// engine that writes each event to the events table and, as one structured
// entry, to the log (journald). Only the correlator emits events.
package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/service"
	"github.com/otfabric/lan-tracker/internal/store"
)

// Type is an event type.
type Type string

// Event types. Legacy types (Spec.Legacy) are LAN Sentinel's: kept so its
// history stays readable and filterable, never emitted by LAN Tracker
// (ADR 0012, docs/DATA_MODEL.md §7).
const (
	HostDiscovered      Type = "HOST_DISCOVERED"
	HostDisappeared     Type = "HOST_DISAPPEARED"
	HostReappeared      Type = "HOST_REAPPEARED"
	IPAdded             Type = "IP_ADDED"
	IPRemoved           Type = "IP_REMOVED"
	IPChanged           Type = "IP_CHANGED"
	MACMoved            Type = "MAC_MOVED"
	HostnameAdded       Type = "HOSTNAME_ADDED"
	HostnameChanged     Type = "HOSTNAME_CHANGED"
	HostnameRemoved     Type = "HOSTNAME_REMOVED"
	ServiceOpened       Type = "SERVICE_OPENED"
	ServiceClosed       Type = "SERVICE_CLOSED"
	VendorIdentified    Type = "VENDOR_IDENTIFIED"
	DuplicateIPDetected Type = "DUPLICATE_IP_DETECTED"
	DuplicateIPResolved Type = "DUPLICATE_IP_RESOLVED"
	ScanStarted         Type = "SCAN_STARTED"
	ScanCompleted       Type = "SCAN_COMPLETED"
	ActiveDisabled      Type = "ACTIVE_DISABLED"
	ActiveEnabled       Type = "ACTIVE_ENABLED"
	InterfaceUp         Type = "INTERFACE_UP"
	InterfaceDown       Type = "INTERFACE_DOWN"
	SubnetChanged       Type = "SUBNET_CHANGED"
	DatabaseRecreated   Type = "DATABASE_RECREATED"

	DHCPServerDiscovered Type = "DHCP_SERVER_DISCOVERED"
	DHCPServerUnexpected Type = "DHCP_SERVER_UNEXPECTED"
	DHCPServerMACChanged Type = "DHCP_SERVER_MAC_CHANGED"
	// DHCPConfigChanged compared one server's configuration across clients;
	// LAN Tracker compares it per client (DHCPClientConfigChanged) and no
	// longer emits it. Kept for LAN Sentinel's events.
	DHCPConfigChanged Type = "DHCP_CONFIG_CHANGED"

	HostDescribed Type = "HOST_DESCRIBED"
	IdentifyRan   Type = "IDENTIFY_RAN"

	// LAN Tracker (migration 0010).
	ProxyARPDetected        Type = "PROXY_ARP_DETECTED"
	ServiceStateChanged     Type = "SERVICE_STATE_CHANGED"
	MACAlsoObserved         Type = "MAC_ALSO_OBSERVED"
	ObservationGap          Type = "OBSERVATION_GAP"
	DHCPLeaseAssigned       Type = "DHCP_LEASE_ASSIGNED"
	DHCPClientConfigChanged Type = "DHCP_CLIENT_CONFIG_CHANGED"
	DHCPLeaseNAK            Type = "DHCP_LEASE_NAK"
	DHCPLeaseDeclined       Type = "DHCP_LEASE_DECLINED"
	DHCPLeaseReleased       Type = "DHCP_LEASE_RELEASED"
)

// Severity is an event's severity.
type Severity string

// Severities.
const (
	Info    Severity = "info"
	Notice  Severity = "notice"
	Warning Severity = "warning"
)

// Spec describes one event type.
type Spec struct {
	Type     Type
	CLIName  string
	Severity Severity
	// Legacy types are only in LAN Sentinel's history.
	Legacy bool
}

var catalogue = map[Type]Spec{}

func init() {
	for _, s := range []Spec{
		{HostDiscovered, "discovered", Notice, false},
		{HostDisappeared, "disappeared", Notice, false},
		{HostReappeared, "reappeared", Notice, false},
		{IPAdded, "ip-added", Notice, false},
		{IPRemoved, "ip-removed", Notice, false},
		{IPChanged, "ip-changed", Notice, true},
		{MACMoved, "mac-moved", Warning, true},
		{HostnameAdded, "name-added", Notice, false},
		{HostnameChanged, "name-changed", Notice, true},
		{HostnameRemoved, "name-removed", Notice, false},
		{ServiceOpened, "service-opened", Notice, true},
		{ServiceClosed, "service-closed", Notice, true},
		{VendorIdentified, "vendor-identified", Info, true},
		{DuplicateIPDetected, "duplicate-ip", Warning, false},
		{DuplicateIPResolved, "duplicate-ip-resolved", Notice, false},
		{ScanStarted, "scan-started", Info, false},
		{ScanCompleted, "scan-completed", Info, false},
		{ActiveDisabled, "active-disabled", Warning, false},
		{ActiveEnabled, "active-enabled", Notice, false},
		{InterfaceUp, "interface-up", Notice, false},
		{InterfaceDown, "interface-down", Warning, false},
		{SubnetChanged, "subnet-changed", Notice, false},
		{DatabaseRecreated, "db-recreated", Warning, false},
		{DHCPServerDiscovered, "dhcp-server-discovered", Notice, false},
		{DHCPServerUnexpected, "dhcp-server-unexpected", Warning, false},
		{DHCPServerMACChanged, "dhcp-server-mac-changed", Warning, false},
		{DHCPConfigChanged, "dhcp-config-changed", Notice, true},
		{HostDescribed, "described", Notice, false},
		{IdentifyRan, "identify-ran", Info, true},
		{ProxyARPDetected, "proxy-arp", Warning, false},
		{ServiceStateChanged, "service-changed", Notice, false},
		{MACAlsoObserved, "mac-also-observed", Notice, false},
		{ObservationGap, "observation-gap", Warning, false},
		{DHCPLeaseAssigned, "dhcp-lease", Notice, false},
		{DHCPClientConfigChanged, "dhcp-client-config-changed", Notice, false},
		{DHCPLeaseNAK, "dhcp-nak", Warning, false},
		{DHCPLeaseDeclined, "dhcp-decline", Warning, false},
		{DHCPLeaseReleased, "dhcp-release", Notice, false},
	} {
		catalogue[s.Type] = s
	}
}

// Lookup returns the spec of t.
func Lookup(t Type) (Spec, bool) {
	s, ok := catalogue[t]
	return s, ok
}

// ByCLIName resolves a kebab-case CLI name such as "ip-changed".
func ByCLIName(name string) (Type, bool) {
	for _, s := range catalogue {
		if s.CLIName == name {
			return s.Type, true
		}
	}
	return "", false
}

// All returns the catalogue sorted by type.
func All() []Spec {
	out := make([]Spec, 0, len(catalogue))
	for _, s := range catalogue {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// Evidence is the self-contained snapshot stored with every event
// (evidence_json), so provenance survives compaction of observations.
type Evidence struct {
	TS            time.Time                  `json:"ts"`
	Source        string                     `json:"source,omitempty"`
	Interface     string                     `json:"interface,omitempty"`
	MAC           string                     `json:"mac,omitempty"`
	IP            string                     `json:"ip,omitempty"`
	Hostname      string                     `json:"hostname,omitempty"`
	NameType      string                     `json:"name_type,omitempty"`
	Service       *observation.ServiceResult `json:"service,omitempty"`
	NeighborState string                     `json:"neighbor_state,omitempty"`
	Reason        string                     `json:"reason,omitempty"`
	Actor         string                     `json:"actor,omitempty"`
	// Contested lists the addresses a proxy-ARP host answered for that
	// other live hosts held.
	Contested []string `json:"contested,omitempty"`
}

// EvidenceFrom snapshots an observation.
func EvidenceFrom(o observation.Observation) Evidence {
	e := Evidence{
		TS: o.Time.UTC(), Source: string(o.Source), Interface: o.Interface, Hostname: o.Hostname,
		NameType: string(o.NameType), Service: o.Service, NeighborState: o.NeighborState,
	}
	if len(o.MAC) > 0 {
		e.MAC = o.MAC.String()
	}
	if o.IP.IsValid() {
		e.IP = o.IP.String()
	}
	return e
}

// Event is one state transition.
type Event struct {
	TS            time.Time
	Type          Type
	ContextID     int64  // 0: not tied to a context
	Interface     string // for the log entry
	HostID        string
	RelatedHostID string
	RelatedMAC    string // for the live stream; the table derives it from the host
	MAC           string // for the log entry
	Old, New      string
	Cause         string // observation source or internal cause (presence, expiry, iface_monitor, ...)
	ObservationID int64  // 0: none
	Evidence      Evidence
}

// Engine writes events.
type Engine struct {
	store *store.Store
	log   *slog.Logger
	clock func() platform.ClockState

	mu     sync.Mutex
	counts map[Type]uint64
	subs   map[*subscriber]struct{}
	missed uint64 // events slow subscribers did not receive
}

type subscriber struct{ ch chan store.Event }

// NewEngine returns an engine writing to st and log. clock reports the
// kernel clock's synchronisation state, recorded with every event
// (NFR-REL-2); nil records unknown (replay, where events carry recorded
// times).
func NewEngine(st *store.Store, log *slog.Logger, clock func() platform.ClockState) *Engine {
	if clock == nil {
		clock = func() platform.ClockState { return platform.ClockUnknown }
	}
	return &Engine{store: st, log: log, clock: clock, counts: map[Type]uint64{}, subs: map[*subscriber]struct{}{}}
}

// Emit queues the event for the next store batch and logs it.
func (e *Engine) Emit(ctx context.Context, ev Event) error {
	spec, ok := catalogue[ev.Type]
	if !ok {
		return fmt.Errorf("unknown event type %q", ev.Type)
	}
	evidence, err := json.Marshal(ev.Evidence)
	if err != nil {
		return fmt.Errorf("event evidence: %w", err)
	}
	clock := e.clock()
	err = e.store.Submit(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO events
			(ts, type, severity, context_id, host_id, related_host_id, old_value, new_value, cause, observation_id, evidence_json, clock_sync)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ev.TS.UnixMilli(), string(ev.Type), string(spec.Severity), nullInt(ev.ContextID), nullString(ev.HostID),
			nullString(ev.RelatedHostID), nullString(ev.Old), nullString(ev.New), ev.Cause, nullInt(ev.ObservationID),
			string(evidence), string(clock))
		if err != nil {
			return fmt.Errorf("insert event %s: %w", ev.Type, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.counts[ev.Type]++
	if len(e.subs) > 0 {
		se := store.Event{
			TS: ev.TS.UTC(), Type: string(ev.Type), Severity: string(spec.Severity), Interface: ev.Interface, HostID: ev.HostID,
			MAC: ev.MAC, RelatedHostID: ev.RelatedHostID, RelatedMAC: ev.RelatedMAC, Old: ev.Old, New: ev.New, Cause: ev.Cause,
			ObservationID: ev.ObservationID, Evidence: evidence, ClockSync: string(clock),
		}
		for s := range e.subs {
			select {
			case s.ch <- se:
			default:
				e.missed++
			}
		}
	}
	e.mu.Unlock()
	e.logEvent(ctx, spec, ev, clock)
	return nil
}

// Subscribe returns a channel that receives every event emitted from now on
// (the live stream behind `watch`), and a function that ends the
// subscription and closes the channel. A subscriber more than buffer events
// behind misses events instead of blocking the correlator.
func (e *Engine) Subscribe(buffer int) (<-chan store.Event, func()) {
	s := &subscriber{ch: make(chan store.Event, max(buffer, 1))}
	e.mu.Lock()
	e.subs[s] = struct{}{}
	e.mu.Unlock()
	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			e.mu.Lock()
			delete(e.subs, s)
			e.mu.Unlock()
			close(s.ch)
		})
	}
}

// Subscribers returns the number of live subscriptions.
func (e *Engine) Subscribers() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.subs)
}

// Missed returns how many events slow subscribers did not receive.
func (e *Engine) Missed() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.missed
}

// Counts returns how many events of each type have been emitted.
func (e *Engine) Counts() map[Type]uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[Type]uint64, len(e.counts))
	for k, v := range e.counts {
		out[k] = v
	}
	return out
}

// logEvent writes one structured entry; the journald handler turns the
// attributes into fields such as EVENT=ip_changed and IFACE=eth1.
func (e *Engine) logEvent(ctx context.Context, spec Spec, ev Event, clock platform.ClockState) {
	level := slog.LevelInfo
	switch spec.Severity {
	case Notice:
		level = service.LevelNotice
	case Warning:
		level = slog.LevelWarn
	}
	attrs := []slog.Attr{
		slog.String("event", strings.ToLower(string(ev.Type))),
		slog.Time("ts", ev.TS),
	}
	add := func(k, v string) {
		if v != "" {
			attrs = append(attrs, slog.String(k, v))
		}
	}
	add("iface", ev.Interface)
	add("host_id", ev.HostID)
	add("mac", ev.MAC)
	add("ip", ev.Evidence.IP)
	add("old_value", ev.Old)
	add("new_value", ev.New)
	add("related_host_id", ev.RelatedHostID)
	add("source", ev.Cause)
	if clock != platform.ClockSynced {
		attrs = append(attrs, slog.String("clock_sync", string(clock)))
	}
	e.log.LogAttrs(ctx, level, message(ev), attrs...)
}

func message(ev Event) string {
	parts := []string{string(ev.Type)}
	for _, p := range []string{ev.Interface, ev.MAC} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	old, nw := ev.Old, ev.New
	if nw == ev.MAC { // HOST_DISCOVERED: the MAC is already in the message
		nw = ""
	}
	switch {
	case old != "" && nw != "":
		parts = append(parts, old+" -> "+nw)
	case nw != "":
		parts = append(parts, nw)
	case old != "":
		parts = append(parts, old)
	}
	if ev.Evidence.IP != "" && ev.Evidence.IP != ev.New && ev.Evidence.IP != ev.Old {
		parts = append(parts, ev.Evidence.IP)
	}
	return strings.Join(parts, " ")
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
