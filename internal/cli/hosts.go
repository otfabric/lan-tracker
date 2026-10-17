package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/otfabric/lan-tracker/internal/store"
)

func (a *app) hostsCmd() *cobra.Command {
	c := &cobra.Command{Use: "hosts", Short: "Query the host inventory and its history"}
	c.AddCommand(a.hostsListCmd(), a.hostsShowCmd(), a.hostsFindCmd(), a.hostsHistoryCmd(), a.hostsEvidenceCmd(),
		a.hostsSetCmd(), a.hostsUnsetCmd())
	return c
}

// queryFlags are the explicit query kinds that override auto-detection.
type queryFlags struct{ ip, mac, hostname, id string }

func (q *queryFlags) add(c *cobra.Command) {
	f := c.Flags()
	f.StringVar(&q.ip, "ip", "", "query an IP address")
	f.StringVar(&q.mac, "mac", "", "query a MAC address")
	f.StringVar(&q.hostname, "hostname", "", "query a hostname (even one that looks like an address)")
	f.StringVar(&q.id, "id", "", "query a host ID")
}

// resolve returns the query from the argument or exactly one kind flag.
func (q queryFlags) resolve(args []string) (string, store.QueryKind, error) {
	var text string
	var kind store.QueryKind
	n := 0
	for _, f := range []struct {
		v string
		k store.QueryKind
	}{{q.ip, store.KindIP}, {q.mac, store.KindMAC}, {q.hostname, store.KindName}, {q.id, store.KindHost}} {
		if f.v != "" {
			text, kind = f.v, f.k
			n++
		}
	}
	if len(args) == 1 {
		text = args[0]
		n++
	}
	if n != 1 {
		return "", "", failf(ExitUsage, "give one query: an argument (host ID, MAC, IP or hostname) or one of --ip, --mac, --hostname, --id")
	}
	return text, kind, nil
}

func (a *app) hostsListCmd() *cobra.Command {
	var iface, vendor, description, seen string
	var active, stale bool
	var port int
	c := &cobra.Command{
		Use:     "list",
		Short:   "Inventory: MAC, IP, hostname, OUI registrant, interface, presence, last seen, description",
		Example: "  sudo lan-tracker hosts list --active",
		Long: "List hosts. --active shows live hosts (ACTIVE or RECENT), --stale the others\n" +
			"(STALE or MISSING); --port hosts with that TCP port last answering OPEN;\n" +
			"--vendor hosts whose OUI registrant (the MAC prefix's IEEE registrant, a hint,\n" +
			"not the equipment's maker) contains the text; --description hosts whose\n" +
			"description contains it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tf := &timeFlags{a: a}
			f := store.HostFilter{Interface: iface, Live: active, NotLive: stale, Vendor: vendor, Description: description, Port: port, SeenSince: tf.ago("seen-within", seen)}
			if tf.err != nil {
				return tf.err
			}
			return a.run(cmd, false, func(ctx context.Context, b backend) error {
				hosts, err := b.Hosts(ctx, f)
				if err != nil {
					return err
				}
				return a.hostsTable(hosts)
			})
		},
	}
	fl := c.Flags()
	fl.StringVar(&iface, "interface", "", "only this interface")
	fl.BoolVar(&active, "active", false, "only live hosts (ACTIVE or RECENT)")
	fl.BoolVar(&stale, "stale", false, "only hosts that are not live (STALE or MISSING)")
	fl.StringVar(&vendor, "vendor", "", "the OUI registrant contains this text")
	fl.StringVar(&description, "description", "", "the description contains this text")
	fl.IntVar(&port, "port", 0, "an OPEN service on this port")
	fl.StringVar(&seen, "seen-within", "", "last seen within this duration (24h, 7d) or since this time")
	return c
}

func (a *app) hostsTable(hosts []store.HostSummary) error {
	w := a.output()
	switch a.g.output {
	case "json":
		return writeJSON(w, hosts)
	case "jsonl":
		return writeJSONL(w, hosts)
	}
	header := []string{"MAC", "IP", "HOSTNAME", "OUI REGISTRANT", "IFACE", "STATE", "LAST SEEN", "DESCRIPTION"}
	rows := make([][]string, 0, len(hosts))
	for _, h := range hosts {
		if a.g.output == "csv" {
			rows = append(rows, []string{h.MAC, strings.Join(h.IPs, " "), h.PreferredName, h.Vendor, h.Interface, h.Presence,
				h.LastSeen.UTC().Format(time.RFC3339), h.Description})
			continue
		}
		ip := "-"
		if len(h.IPs) > 0 {
			ip = h.IPs[0]
			if len(h.IPs) > 1 {
				ip += fmt.Sprintf(" (+%d)", len(h.IPs)-1)
			}
		}
		rows = append(rows, []string{h.MAC, ip, dash(h.PreferredName), dash(h.Vendor), h.Interface, h.Presence, a.ago(h.LastSeen),
			h.Description})
	}
	if a.g.output == "csv" {
		return writeCSV(w, header, rows)
	}
	return writeTable(w, header, rows)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (a *app) hostsShowCmd() *cobra.Command {
	var q queryFlags
	var iface string
	c := &cobra.Command{
		Use:     "show <query>",
		Short:   "Full record of one current host, with every address and name over time",
		Example: "  sudo lan-tracker hosts show 192.168.0.99",
		Long: "Shows one current host by host ID, MAC, IP or hostname (detected in that\n" +
			"order, as hosts find). When several match (the same MAC on two interfaces),\n" +
			"it lists them with their host IDs and exits 64; --interface or --id picks one.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := onlyFormats(a.g.output, "table", "json"); err != nil {
				return err
			}
			text, kind, err := q.resolve(args)
			if err != nil {
				return err
			}
			return a.run(cmd, false, func(ctx context.Context, b backend) error {
				cur, err := oneCurrent(ctx, b, text, kind, iface)
				if err != nil {
					return err
				}
				h, err := b.Host(ctx, cur.HostID)
				if err != nil {
					return err
				}
				if a.g.output == "json" {
					return writeJSON(a.output(), h)
				}
				a.hostRecord(a.output(), h, nil)
				return nil
			})
		},
	}
	q.add(c)
	c.Flags().StringVar(&iface, "interface", "", "the host's interface, when the query matches hosts on several")
	return c
}

// finder is what oneCurrent needs: a backend, or the API client of a
// command that only runs online (hosts set).
type finder interface {
	Find(ctx context.Context, q store.FindQuery) (store.FindResult, error)
}

// oneCurrent resolves a find query to exactly one current host (hosts show,
// hosts set). Several matches are a usage error so the operator can pick
// --interface or a host ID.
func oneCurrent(ctx context.Context, b finder, query string, kind store.QueryKind, iface string) (store.Host, error) {
	res, err := b.Find(ctx, store.FindQuery{Query: query, Kind: kind, Interface: iface})
	if err != nil {
		return store.Host{}, err
	}
	switch len(res.Hosts) {
	case 0:
		return store.Host{}, failf(ExitDegraded, "no current host matches %s", query)
	case 1:
		return res.Hosts[0].Host, nil
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "%s matches %d hosts; name one with --interface or its host ID:", query, len(res.Hosts))
	for _, h := range res.Hosts {
		fmt.Fprintf(&buf, "\n  %s  %s  %s", h.HostID, h.Interface, h.MAC)
	}
	return store.Host{}, failf(ExitUsage, "%s", buf.String())
}

// hostRecord prints a host as in docs/CLI.md §5; at marks the bindings
// unconfirmed at that time.
func (a *app) hostRecord(w io.Writer, h store.Host, at *time.Time) {
	mac := h.MAC
	if h.Vendor != "" {
		mac += "  (OUI registrant: " + h.Vendor + ")"
	}
	if h.LocallyAdministered {
		mac += "  (locally administered)"
	}
	fmt.Fprintf(w, "Host ID:     %s\nInterface:   %s\nMAC:         %s\nState:       %s\n", h.HostID, h.Interface, mac, h.Presence)
	if h.PreferredName != "" {
		fmt.Fprintf(w, "Name:        %s\n", h.PreferredName)
	}
	if h.Description != "" {
		fmt.Fprintf(w, "Description: %s\n", h.Description)
	}
	if h.ProxyARP {
		fmt.Fprintln(w, "Proxy ARP:   answers ARP for addresses other hosts hold; its ARP replies do not prove ownership")
	}
	fmt.Fprintf(w, "First seen:  %s\nLast seen:   %s%s\n", a.stamp(h.FirstSeen), a.stamp(h.LastSeen), a.evidenceKinds(h.HostSummary))
	if len(h.Addresses) > 0 {
		fmt.Fprintln(w, "\nAddresses:")
		for _, b := range h.Addresses {
			fmt.Fprintf(w, "  %s\n", a.bindingLine(b, at))
		}
	}
	a.printNames(w, h.Names, false)
	a.printEndpoints(w, h.Endpoints)
	a.printLeases(w, h.DHCPLeases)
	a.printLegacyServices(w, h.LegacyServices)
}

// printLeases lists the leases servers acknowledged for the host's MAC.
func (a *app) printLeases(w io.Writer, leases []store.DHCPLease) {
	if len(leases) == 0 {
		return
	}
	fmt.Fprintln(w, "\nDHCP leases (the servers' statements, not proof the host holds the address):")
	for _, l := range leases {
		end := "open"
		if l.EndedAt != nil {
			end = l.EndReason + " " + a.stamp(*l.EndedAt)
		}
		line := fmt.Sprintf("  %-16s from %-15s %s → %s  acks %d  lease %s", l.IP, dash(l.ServerID), a.stamp(l.FirstACK), end, l.ACKs,
			leaseText(l.LeaseSeconds))
		if l.ExpiresAt != nil && l.EndedAt == nil {
			line += "  expires " + a.stamp(*l.ExpiresAt)
		}
		if gw := configText(l.Config, "gateway"); gw != "" {
			line += "  gateway " + gw
		}
		if l.Relay != "" {
			line += "  via relay " + l.Relay
		}
		if l.Backfilled {
			line += "  (LAN Sentinel: configuration unknown)"
		}
		fmt.Fprintln(w, line)
	}
}

// evidenceKinds says which kind of evidence was last when ("  (frame 2m
// ago, address 1h ago, endpoint answer 3m ago)"); empty for a host whose
// kinds are unknown (recorded by LAN Sentinel).
func (a *app) evidenceKinds(h store.HostSummary) string {
	var parts []string
	for _, k := range []struct {
		name string
		t    *time.Time
	}{{"frame", h.LastFrameSeen}, {"address confirmed", h.LastAddressConfirmed}, {"endpoint answer", h.LastEndpointAnswer}} {
		if k.t != nil {
			parts = append(parts, k.name+" "+a.ago(*k.t))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "  (" + strings.Join(parts, ", ") + ")"
}

// printNames lists name records: the name, its type and record type, what
// it describes, its freshness and, with detail, its advertiser and TTL.
func (a *app) printNames(w io.Writer, names []store.Name, detail bool) {
	if len(names) == 0 {
		return
	}
	fmt.Fprintln(w, "\nNames:")
	for _, n := range names {
		what := n.Address
		if what == "" {
			what = n.Value
		}
		line := fmt.Sprintf("  %-24s %-7s %-5s %-16s %s %s", n.Name, n.Type, n.RecordType, dash(what), n.Freshness, a.ago(n.LastSeen))
		if n.Legacy {
			line += " (legacy: type, advertiser and TTL unknown)"
		}
		if n.EndedAt != nil {
			line += "  ended " + a.stamp(*n.EndedAt) + " (" + n.EndReason + ")"
		}
		if detail {
			line += "  " + n.Source
			if n.AdvertiserMAC != "" && n.Link != "sender" {
				line += " advertised by " + n.AdvertiserMAC
			}
			if n.TTL != nil {
				line += fmt.Sprintf(" ttl %ds", *n.TTL)
			}
			line += " link " + n.Link
		}
		fmt.Fprintln(w, line)
	}
}

// printEndpoints lists TCP endpoints: the last result and, apart from it,
// the last definite answer.
func (a *app) printEndpoints(w io.Writer, eps []store.Endpoint) {
	if len(eps) == 0 {
		return
	}
	fmt.Fprintln(w, "\nTCP endpoints:")
	for _, e := range eps {
		line := fmt.Sprintf("  %-16s %-9s %-11s %s", e.IP, e.Proto+"/"+strconv.Itoa(e.Port), e.LastResult, a.ago(e.LastResultAt))
		if e.LastDetail != "" {
			line += " (" + e.LastDetail + ")"
		}
		if e.LastDefiniteAt != nil && (e.LastDefiniteResult != e.LastResult || !e.LastDefiniteAt.Equal(e.LastResultAt)) {
			line += "  last definite: " + e.LastDefiniteResult + " " + a.ago(*e.LastDefiniteAt)
		}
		fmt.Fprintln(w, line)
	}
}

// printLegacyServices lists LAN Sentinel's probe results, which have no
// address.
func (a *app) printLegacyServices(w io.Writer, svc []store.Service) {
	if len(svc) == 0 {
		return
	}
	fmt.Fprintln(w, "\nLegacy services (recorded by LAN Sentinel, address unknown):")
	for _, s := range svc {
		fmt.Fprintf(w, "  %-9s %-11s %s\n", s.Proto+"/"+strconv.Itoa(s.Port), s.State, a.stamp(s.LastResultAt))
	}
}

// bindingLine renders "IP  first → end|open  sources: …  CONFLICT".
func (a *app) bindingLine(b store.Binding, at *time.Time) string {
	end := "open"
	if b.EndedAt != nil {
		end = a.stamp(*b.EndedAt)
	}
	line := fmt.Sprintf("%-16s %s → %s", b.IP, a.stamp(b.FirstSeen), end)
	if len(b.Sources) > 0 {
		var src []string
		for _, s := range b.Sources {
			src = append(src, s.Source)
		}
		line += "   sources: " + strings.Join(src, ", ")
	}
	if b.Conflict {
		line += "   CONFLICT"
	}
	if at != nil && at.After(b.LastSeen) {
		line += fmt.Sprintf("   (unconfirmed since %s)", a.stamp(b.LastSeen))
	}
	return line
}

// printLegacy lists the identifications LAN Sentinel recorded. They are
// history: nothing updates them since the migration (ADR 0012).
func (a *app) printLegacy(w io.Writer, ids []store.Identification) {
	if len(ids) == 0 {
		return
	}
	fmt.Fprintln(w, "\nLegacy identification (recorded by LAN Sentinel, no longer maintained):")
	for _, i := range ids {
		fmt.Fprintf(w, "  %-12s %s=%s  first %s  last %s\n", i.Source, i.Field, i.Value, a.stamp(i.FirstSeen), a.stamp(i.LastSeen))
	}
}

func (a *app) hostsFindCmd() *cobra.Command {
	var q queryFlags
	var iface, atFlag string
	c := &cobra.Command{
		Use:     "find <query>",
		Short:   "Current or point-in-time holder(s) with addresses, names and TCP endpoints",
		Example: "  sudo lan-tracker hosts find 192.168.0.99",
		Long: "Find hosts by host ID, MAC, IP or hostname (detected in that order). With --at,\n" +
			"answer for that time: the holder of an IP with its binding and what replaced it,\n" +
			"every holder of a duplicate IP, or the bindings before and after when nobody\n" +
			"held it. Exit 1 when nothing matches.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := onlyFormats(a.g.output, "table", "json"); err != nil {
				return err
			}
			text, kind, err := q.resolve(args)
			if err != nil {
				return err
			}
			tf := &timeFlags{a: a}
			fq := store.FindQuery{Query: text, Kind: kind, Interface: iface}
			if t := tf.at("at", atFlag); !t.IsZero() {
				fq.At = &t
			}
			if tf.err != nil {
				return tf.err
			}
			return a.run(cmd, false, func(ctx context.Context, b backend) error {
				res, err := b.Find(ctx, fq)
				if err != nil {
					return err
				}
				if a.g.output == "json" {
					err = writeJSON(a.output(), res)
				} else {
					a.findResult(a.output(), res)
				}
				if err == nil && len(res.Hosts) == 0 {
					return silent(ExitDegraded)
				}
				return err
			})
		},
	}
	q.add(c)
	c.Flags().StringVar(&iface, "interface", "", "only this interface")
	c.Flags().StringVar(&atFlag, "at", "", "answer for this time (YYYY-MM-DD[ HH:MM[:SS]] local, or RFC 3339)")
	return c
}

func (a *app) findResult(w io.Writer, res store.FindResult) {
	defer func() {
		if len(res.Advertised) == 0 {
			return
		}
		fmt.Fprintln(w, "\nAdvertised or resolved, but held by no single MAC (a name record, not a host):")
		for _, n := range res.Advertised {
			what := n.Address
			if what == "" {
				what = n.Value
			}
			line := fmt.Sprintf("  %-24s %-7s %-5s %-16s %s %s  %s", n.Name, n.Type, n.RecordType, dash(what), n.Freshness, a.ago(n.LastSeen), n.Source)
			if n.AdvertiserMAC != "" {
				line += " from " + n.AdvertiserMAC
			}
			fmt.Fprintln(w, line)
		}
	}()
	if len(res.Hosts) == 0 {
		when := "now"
		if res.At != nil {
			when = "at " + a.stamp(*res.At)
		}
		fmt.Fprintf(w, "No holder of %s %s.\n", res.Query, when)
		for _, p := range res.Previous {
			fmt.Fprintf(w, "Previous:    %s held by %s on %s until %s\n", p.Binding.IP, p.MAC, p.Interface, a.stamp(*p.Binding.EndedAt))
		}
		for _, n := range res.Next {
			fmt.Fprintf(w, "Next:        %s held by %s on %s from %s\n", n.Binding.IP, n.MAC, n.Interface, a.stamp(n.Binding.FirstSeen))
		}
		return
	}
	for i, h := range res.Hosts {
		if i > 0 {
			fmt.Fprintln(w)
		}
		if h.Binding == nil || res.At == nil {
			a.hostRecord(w, h.Host, res.At)
			continue
		}
		// An IP at a point in time: the holder and its binding.
		mac := h.MAC
		if h.Vendor != "" {
			mac += "  (OUI registrant: " + h.Vendor + ")"
		}
		fmt.Fprintf(w, "Host ID:     %s\nInterface:   %s\nMAC:         %s\n", h.HostID, h.Interface, mac)
		if h.PreferredName != "" {
			fmt.Fprintf(w, "Name:        %s\n", h.PreferredName)
		}
		if h.Description != "" {
			fmt.Fprintf(w, "Description: %s\n", h.Description)
		}
		b := *h.Binding
		b.Conflict = h.Conflict // in conflict at the time asked about
		fmt.Fprintf(w, "Binding:     %s\n", a.bindingLine(b, res.At))
		if h.ReplacedBy != nil {
			fmt.Fprintf(w, "Replaced by: %s → %s from %s\n", h.ReplacedBy.Binding.IP, h.ReplacedBy.MAC, a.stamp(h.ReplacedBy.Binding.FirstSeen))
		}
		if h.NextAddress != nil {
			fmt.Fprintf(w, "Next new address of this MAC: %s from %s (its addresses are independent; not proof it moved)\n",
				h.NextAddress.Binding.IP, a.stamp(h.NextAddress.Binding.FirstSeen))
		}
		if h.ConflictEvent != nil {
			e := h.ConflictEvent
			fmt.Fprintf(w, "Conflict:    %s %s %s between %s and %s\n", a.stamp(e.TS), e.Type, e.New, e.MAC, e.RelatedMAC)
		}
	}
}

func (a *app) hostsHistoryCmd() *cobra.Command {
	var q queryFlags
	var iface, since, until string
	c := &cobra.Command{
		Use:     "history <query>",
		Short:   "Event timeline for a host, MAC, IP or name",
		Example: "  sudo lan-tracker hosts history 192.168.0.99",
		Long: "For a host ID or MAC, every event of the host; for an IP, every event whose\n" +
			"old value, new value or evidence IP is that IP, across all its holders; for a\n" +
			"name, every event of the hosts that ever had it.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			text, kind, err := q.resolve(args)
			if err != nil {
				return err
			}
			tf := &timeFlags{a: a}
			hq := store.HistoryQuery{Query: text, Kind: kind, Interface: iface, Since: tf.ago("since", since), Until: tf.at("until", until)}
			if tf.err != nil {
				return tf.err
			}
			return a.run(cmd, false, func(ctx context.Context, b backend) error {
				evs, err := b.History(ctx, hq)
				if err != nil {
					return err
				}
				return a.eventsTable(evs)
			})
		},
	}
	q.add(c)
	fl := c.Flags()
	fl.StringVar(&iface, "interface", "", "only this interface")
	fl.StringVar(&since, "since", "", "from this time or duration ago (24h, 7d)")
	fl.StringVar(&until, "until", "", "up to this time")
	return c
}

// eventValue is the readable change of an event.
func eventValue(e store.Event) string {
	switch {
	case e.New != "" && e.New == e.MAC: // HOST_DISCOVERED: show the address
		return dash(e.EvidenceIP())
	case e.Old != "" && e.New != "":
		return e.Old + " -> " + e.New
	case e.New != "":
		return e.New
	}
	return dash(e.Old)
}

func (a *app) eventsTable(evs []store.Event) error {
	w := a.output()
	switch a.g.output {
	case "json":
		return writeJSON(w, evs)
	case "jsonl":
		return writeJSONL(w, evs)
	}
	header := []string{"TIME", "TYPE", "IFACE", "MAC", "VALUE", "RELATED", "CAUSE"}
	rows := make([][]string, 0, len(evs))
	marked := map[string]bool{}
	for _, e := range evs {
		row := []string{"", e.Type, dash(e.Interface), dash(e.MAC), eventValue(e), dash(e.RelatedMAC), e.Cause}
		if a.g.output == "csv" {
			row[0] = e.TS.UTC().Format(time.RFC3339Nano)
			row = append(row, e.ClockSync)
		} else {
			mark := clockMarks[e.ClockSync]
			row[0] = a.stamp(e.TS) + mark
			marked[mark] = marked[mark] || mark != ""
		}
		rows = append(rows, row)
	}
	if a.g.output == "csv" {
		return writeCSV(w, append(header, "CLOCK_SYNC"), rows)
	}
	if err := writeTable(w, header, rows); err != nil {
		return err
	}
	// Events written before the clock was synchronised (NFR-REL-2).
	if marked["*"] {
		fmt.Fprintln(w, "* written before the system clock was synchronised: the time may be off")
	}
	if marked["?"] {
		fmt.Fprintln(w, "? written while the clock's synchronisation was unknown")
	}
	return nil
}

// clockMarks flag event times by the clock's state when they were written.
var clockMarks = map[string]string{"unsynced": "*", "unknown": "?"}

func (a *app) hostsEvidenceCmd() *cobra.Command {
	var q queryFlags
	var iface, since string
	c := &cobra.Command{
		Use:     "evidence <query>",
		Short:   "Why we believe what we believe about a host",
		Example: "  sudo lan-tracker hosts evidence 192.168.0.99",
		Long: "For every host that ever matched the query: per address, name and TCP endpoint\n" +
			"the sources that confirmed it with first and last seen, the\n" +
			"observation counts per source within roll-up retention, and the host's events\n" +
			"with their evidence snapshots.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := onlyFormats(a.g.output, "table", "json", "jsonl"); err != nil {
				return err
			}
			text, kind, err := q.resolve(args)
			if err != nil {
				return err
			}
			tf := &timeFlags{a: a}
			from := tf.ago("since", since)
			if tf.err != nil {
				return tf.err
			}
			return a.run(cmd, false, func(ctx context.Context, b backend) error {
				all, err := b.EvidenceFor(ctx, store.HostFilter{Query: text, QueryKind: kind, Interface: iface}, from)
				if err != nil {
					return err
				}
				switch a.g.output {
				case "json":
					err = writeJSON(a.output(), nonNilSlice(all))
				case "jsonl":
					err = writeJSONL(a.output(), all)
				default:
					a.evidenceTable(a.output(), all)
				}
				if err == nil && len(all) == 0 {
					return silent(ExitDegraded)
				}
				return err
			})
		},
	}
	q.add(c)
	c.Flags().StringVar(&iface, "interface", "", "only this interface")
	c.Flags().StringVar(&since, "since", "", "events and counts from this time or duration ago")
	return c
}

func nonNilSlice[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

func (a *app) evidenceTable(w io.Writer, all []store.Evidence) {
	for i, ev := range all {
		if i > 0 {
			fmt.Fprintln(w, strings.Repeat("-", 72))
		}
		h := ev.Host
		fmt.Fprintf(w, "Host %s  %s  on %s\n", h.HostID, h.MAC, h.Interface)
		if len(h.Addresses) > 0 {
			fmt.Fprintln(w, "\nAddresses:")
			for _, b := range h.Addresses {
				fmt.Fprintf(w, "  %s\n", a.bindingLine(store.Binding{IP: b.IP, FirstSeen: b.FirstSeen, LastSeen: b.LastSeen, EndedAt: b.EndedAt, Conflict: b.Conflict}, nil))
				for _, s := range b.Sources {
					fmt.Fprintf(w, "      %-16s first %s  last %s\n", s.Source, a.stamp(s.FirstSeen), a.stamp(s.LastSeen))
				}
			}
		}
		a.printNames(w, h.Names, true)
		a.printEndpoints(w, h.Endpoints)
		a.printLeases(w, h.DHCPLeases)
		a.printLegacyServices(w, h.LegacyServices)
		a.printLegacy(w, h.LegacyIdentifications)
		if len(ev.Counts) > 0 {
			fmt.Fprintln(w, "\nObservations (raw and roll-ups):")
			for _, c := range ev.Counts {
				fmt.Fprintf(w, "  %-16s %-16s %6d  first %s  last %s\n", c.Source, dash(c.IP), c.Count, a.stamp(c.FirstSeen), a.stamp(c.LastSeen))
			}
		}
		if len(ev.Events) > 0 {
			fmt.Fprintln(w, "\nEvents:")
			for _, e := range ev.Events {
				fmt.Fprintf(w, "  %s  %-22s %s  (%s)\n      evidence %s\n", a.stamp(e.TS), e.Type, eventValue(e), e.Cause, compact(e.Evidence))
			}
		}
	}
}

// compact renders JSON on one line.
func compact(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
