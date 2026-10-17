package cli

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/otfabric/lan-tracker/internal/api"
	"github.com/otfabric/lan-tracker/internal/events"
	"github.com/otfabric/lan-tracker/internal/store"
)

func (a *app) observationsCmd() *cobra.Command {
	c := &cobra.Command{Use: "observations", Short: "Raw evidence and its hourly roll-ups"}
	var f store.ObservationFilter
	var since, until string
	var rollups bool
	list := &cobra.Command{
		Use:     "list",
		Short:   "Raw observations within retention; hourly roll-ups with --rollups",
		Example: "  sudo lan-tracker observations list --since 1h",
		Long: "List the latest observations (default 1000) in time order. Observations older\n" +
			"than raw retention exist only as hourly roll-ups (--rollups): one row per host,\n" +
			"source, MAC, IP and hour, with a count.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tf := &timeFlags{a: a}
			f.Since, f.Until = tf.ago("since", since), tf.at("until", until)
			if tf.err != nil {
				return tf.err
			}
			return a.run(cmd, false, func(ctx context.Context, b backend) error {
				if rollups {
					rs, err := b.Rollups(ctx, f)
					if err != nil {
						return err
					}
					return a.rollupsTable(rs)
				}
				obs, err := b.Observations(ctx, f)
				if err != nil {
					return err
				}
				return a.observationsTable(obs)
			})
		},
	}
	fl := list.Flags()
	fl.StringVar(&f.HostID, "host", "", "only this host ID")
	fl.StringVar(&f.Interface, "interface", "", "only this interface")
	fl.StringVar(&f.MAC, "mac", "", "only this MAC")
	fl.StringVar(&f.IP, "ip", "", "only this IP")
	fl.StringVar(&f.Source, "source", "", "only this source (e.g. passive_arp)")
	fl.BoolVar(&f.Unbound, "unbound", false, "only observations that matched no host")
	fl.StringVar(&since, "since", "", "from this time or duration ago (24h, 7d)")
	fl.StringVar(&until, "until", "", "up to this time")
	fl.IntVar(&f.Limit, "limit", store.DefaultObservationLimit, "at most this many rows (the latest)")
	fl.BoolVar(&rollups, "rollups", false, "hourly roll-ups instead of raw observations")
	c.AddCommand(list)
	return c
}

func (a *app) listOut(header []string, rows [][]string, v any, jsonl func() error) error {
	w := a.output()
	switch a.g.output {
	case "json":
		return writeJSON(w, v)
	case "jsonl":
		return jsonl()
	case "csv":
		return writeCSV(w, header, rows)
	}
	return writeTable(w, header, rows)
}

func (a *app) ts(t time.Time) string {
	if a.g.output == "csv" {
		return t.UTC().Format(time.RFC3339Nano)
	}
	return a.stamp(t)
}

func (a *app) observationsTable(obs []store.Observation) error {
	header := []string{"TIME", "IFACE", "SOURCE", "MAC", "IP", "HOSTNAME", "SERVICE", "HOST"}
	rows := make([][]string, 0, len(obs))
	for _, o := range obs {
		svc := ""
		if len(o.Service) > 0 {
			svc = compact(o.Service)
		}
		rows = append(rows, []string{a.ts(o.TS), o.Interface, o.Source, dash(o.MAC), dash(o.IP), dash(o.Hostname), dash(svc), dash(o.HostID)})
	}
	return a.listOut(header, rows, obs, func() error { return writeJSONL(a.output(), obs) })
}

func (a *app) rollupsTable(rs []store.Rollup) error {
	header := []string{"HOUR", "IFACE", "SOURCE", "MAC", "IP", "COUNT", "HOST"}
	rows := make([][]string, 0, len(rs))
	for _, r := range rs {
		rows = append(rows, []string{a.ts(r.Hour), r.Interface, r.Source, dash(r.MAC), dash(r.IP), strconv.FormatInt(r.Count, 10), dash(r.HostID)})
	}
	return a.listOut(header, rows, rs, func() error { return writeJSONL(a.output(), rs) })
}

// eventNames are the CLI names of every event type, sorted, for errors:
// operators have no docs on the box.
func eventNames() []string {
	var names []string
	for _, s := range events.All() {
		names = append(names, s.CLIName)
	}
	slices.Sort(names)
	return names
}

// eventTypes resolves --type values (CLI or spec names) to spec names.
func eventTypes(names []string) ([]string, error) {
	var out []string
	for _, v := range names {
		for _, n := range strings.Split(v, ",") {
			t, ok := api.EventType(n)
			if !ok {
				return nil, failf(ExitUsage, "--type: unknown event type %q; one of: %s", n, strings.Join(eventNames(), ", "))
			}
			out = append(out, string(t))
		}
	}
	return out, nil
}

func (a *app) eventsCmd() *cobra.Command {
	c := &cobra.Command{Use: "events", Short: "State transitions with their evidence"}
	var f store.EventFilter
	var since, until string
	var types []string
	list := &cobra.Command{
		Use:     "list",
		Short:   "All events on this box",
		Example: "  sudo lan-tracker events list --type ip-added --since 24h",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tf := &timeFlags{a: a}
			f.Since, f.Until = tf.ago("since", since), tf.at("until", until)
			if tf.err != nil {
				return tf.err
			}
			var err error
			if f.Types, err = eventTypes(types); err != nil {
				return err
			}
			return a.run(cmd, false, func(ctx context.Context, b backend) error {
				evs, err := b.Events(ctx, f)
				if err != nil {
					return err
				}
				return a.eventsTable(evs)
			})
		},
	}
	fl := list.Flags()
	fl.StringVar(&since, "since", "", "from this time or duration ago (24h, 7d)")
	fl.StringVar(&until, "until", "", "up to this time")
	fl.StringSliceVar(&types, "type", nil, "event types by CLI name, e.g. ip-added, duplicate-ip (repeatable)")
	fl.StringVar(&f.Interface, "interface", "", "only this interface")
	fl.StringVar(&f.MAC, "mac", "", "events of hosts with this MAC")
	fl.StringVar(&f.IP, "ip", "", "events about this IP")
	fl.IntVar(&f.Limit, "limit", 1000, "at most this many events (the latest)")
	c.AddCommand(list)
	return c
}

func (a *app) servicesCmd() *cobra.Command {
	c := &cobra.Command{Use: "services", Short: "TCP endpoint results"}
	var f store.ServiceFilter
	var at string
	list := &cobra.Command{
		Use:     "list",
		Short:   "TCP endpoint results per address and port, with the last definite answer",
		Example: "  sudo lan-tracker services list --interface eth0 --ip 192.168.1.10",
		Long: "Lists the TCP endpoints checked, per interface, address and port: the last result\n" +
			"(OPEN, REFUSED, TIMEOUT, UNREACHABLE or LOCAL_ERROR, which is a failure here, not an\n" +
			"answer), the last definite answer (OPEN or REFUSED) and the MAC that alone held the\n" +
			"address then (- when none or several did). A timeout is not a closed port, and OPEN\n" +
			"is a completed connect, not proof of any protocol: tcp/502 OPEN is not Modbus.\n" +
			"--at shows the results in effect at a time.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tf := &timeFlags{a: a}
			if t := tf.at("at", at); !t.IsZero() {
				f.At = &t
			}
			if tf.err != nil {
				return tf.err
			}
			return a.run(cmd, false, func(ctx context.Context, b backend) error {
				eps, err := b.Services(ctx, f)
				if err != nil {
					return err
				}
				header := []string{"IP", "PORT", "LAST RESULT", "CHECKED", "LAST DEFINITE", "MAC", "IFACE"}
				rows := make([][]string, 0, len(eps))
				for _, e := range eps {
					last, definite := a.ago(e.LastResultAt), "-"
					if e.LastDefiniteAt != nil {
						definite = e.LastDefiniteResult + " " + a.ago(*e.LastDefiniteAt)
					}
					if a.g.output == "csv" {
						last = e.LastResultAt.UTC().Format(time.RFC3339)
						if e.LastDefiniteAt != nil {
							definite = e.LastDefiniteResult + " " + e.LastDefiniteAt.UTC().Format(time.RFC3339)
						}
					}
					result := e.LastResult
					if e.LastDetail != "" {
						result += " (" + e.LastDetail + ")"
					}
					rows = append(rows, []string{e.IP, strconv.Itoa(e.Port) + "/" + e.Proto, result, last, definite, dash(e.MAC), e.Interface})
				}
				return a.listOut(header, rows, eps, func() error { return writeJSONL(a.output(), eps) })
			})
		},
	}
	fl := list.Flags()
	fl.IntVar(&f.Port, "port", 0, "only this port")
	fl.StringVar(&f.State, "state", "", "only this last result (OPEN, REFUSED, TIMEOUT, UNREACHABLE, LOCAL_ERROR)")
	fl.StringVar(&f.Interface, "interface", "", "only this interface")
	fl.StringVar(&f.IP, "ip", "", "only this address")
	fl.StringVar(&at, "at", "", "the results in effect at this time")
	c.AddCommand(list)
	return c
}

func (a *app) dhcpCmd() *cobra.Command {
	c := &cobra.Command{Use: "dhcp", Short: "DHCP servers, lease history and exchanges seen on the interfaces"}
	var f store.DHCPServerFilter
	servers := &cobra.Command{
		Use:     "servers",
		Short:   "DHCP servers: identity, relay, sender, allowlist verdict and what they advertise",
		Example: "  sudo lan-tracker dhcp servers",
		Long: "Lists every DHCP server identity (option 54) seen in a reply, per interface, relay\n" +
			"and sender MAC (the server's, or the relay's). STATUS is the allowlist verdict at the\n" +
			"last reply: allowed, unexpected, or unchecked when the interface has no allowlist\n" +
			"(interfaces[].dhcp.servers). The allowlist matches the advertised identifier and\n" +
			"does not authenticate a server. On a switched port many replies are unicast to\n" +
			"the client and never seen, so an empty list does not prove there is no server.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch strings.ToLower(f.Status) {
			case "", "allowed", "unexpected", "unchecked":
			default:
				return failf(ExitUsage, "--status: want allowed, unexpected or unchecked, got %q", f.Status)
			}
			return a.run(cmd, false, func(ctx context.Context, b backend) error {
				servers, err := b.DHCPServers(ctx, f)
				if err != nil {
					return err
				}
				header := []string{"SERVER ID", "RELAY", "MAC", "IP", "IFACE", "STATUS", "ROUTER", "DNS", "MASK", "LAST SEEN"}
				rows := make([][]string, 0, len(servers))
				for _, s := range servers {
					id := s.ServerID
					if id == "" {
						id = "unknown"
					}
					last := a.ago(s.LastSeen)
					if a.g.output == "csv" {
						last = s.LastSeen.UTC().Format(time.RFC3339)
					}
					rows = append(rows, []string{id, dash(s.Relay), s.MAC, dash(s.IP), s.Interface, s.Status,
						dash(s.Config["router"]), dash(s.Config["dns"]), dash(s.Config["subnet_mask"]), last})
				}
				return a.listOut(header, rows, servers, func() error { return writeJSONL(a.output(), servers) })
			})
		},
	}
	fl := servers.Flags()
	fl.StringVar(&f.Interface, "interface", "", "only this interface")
	fl.StringVar(&f.Status, "status", "", "only this verdict (allowed, unexpected, unchecked)")
	c.AddCommand(servers, a.dhcpLeasesCmd(), a.dhcpExchangesCmd())
	return c
}

func (a *app) dhcpLeasesCmd() *cobra.Command {
	var f store.DHCPLeaseFilter
	c := &cobra.Command{
		Use:     "leases",
		Short:   "Lease history: the addresses and configuration servers acknowledged per client",
		Example: "  sudo lan-tracker dhcp leases --open\n  sudo lan-tracker dhcp leases --mac 00:1b:1b:aa:bb:01",
		Long: "Lists one row per address and acknowledged configuration a server asserted for a client\n" +
			"(an ACK with an address), newest first. A renewal with the same address, server and\n" +
			"configuration extends its row (ACKS); anything else starts a new one and ends the old\n" +
			"(superseded). A lease is the server's statement: it creates no host, does not make the\n" +
			"client present and is not proof the client holds the address. EXPIRES is the expiry per\n" +
			"the last ACK seen. PARTIAL marks a lease whose REQUEST was not seen (unicast on a switched\n" +
			"port).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, false, func(ctx context.Context, b backend) error {
				leases, err := b.DHCPLeases(ctx, f)
				if err != nil {
					return err
				}
				header := []string{"CLIENT", "IP", "SERVER ID", "RELAY", "IFACE", "FIRST ACK", "LAST ACK", "ACKS", "LEASE", "EXPIRES", "GATEWAY", "DNS", "STATE"}
				rows := make([][]string, 0, len(leases))
				for _, l := range leases {
					rows = append(rows, []string{l.CHAddr, l.IP, dash(l.ServerID), dash(l.Relay), l.Interface, a.when(l.FirstACK), a.when(l.LastACK),
						strconv.Itoa(l.ACKs), leaseText(l.LeaseSeconds), a.whenPtr(l.ExpiresAt), dash(configText(l.Config, "gateway")),
						dash(configText(l.Config, "dns")), leaseState(l)})
				}
				return a.listOut(header, rows, leases, func() error { return writeJSONL(a.output(), leases) })
			})
		},
	}
	fl := c.Flags()
	fl.StringVar(&f.Interface, "interface", "", "only this interface")
	fl.StringVar(&f.MAC, "mac", "", "only this client")
	fl.StringVar(&f.IP, "ip", "", "only this address")
	fl.BoolVar(&f.Open, "open", false, "only the leases in effect")
	fl.IntVar(&f.Limit, "limit", 0, "at most this many rows (default 1000)")
	return c
}

func (a *app) dhcpExchangesCmd() *cobra.Command {
	var f store.DHCPExchangeFilter
	var since string
	c := &cobra.Command{
		Use:     "exchanges",
		Short:   "DHCP transactions: each client's messages, the offers, its choice and the outcome",
		Example: "  sudo lan-tracker dhcp exchanges --since 1h",
		Long: "Lists the DHCP exchanges seen, newest first: one per client transaction (interface, xid,\n" +
			"client hardware address), its messages in order with retransmissions counted, the servers\n" +
			"that offered, the one the client selected and the outcome (ack, inform_ack, nak, offered,\n" +
			"decline, release, or incomplete when it closed unanswered). VISIBILITY is partial when\n" +
			"only one side was seen: on a switched port unicast replies and renewals are not.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tf := &timeFlags{a: a}
			if f.Since = tf.ago("since", since); tf.err != nil {
				return tf.err
			}
			return a.run(cmd, false, func(ctx context.Context, b backend) error {
				exchanges, err := b.DHCPExchanges(ctx, f)
				if err != nil {
					return err
				}
				header := []string{"CLIENT", "XID", "IFACE", "FIRST SEEN", "MESSAGES", "OFFERS", "SELECTED", "OUTCOME", "VISIBILITY"}
				rows := make([][]string, 0, len(exchanges))
				for _, x := range exchanges {
					rows = append(rows, []string{x.CHAddr, fmt.Sprintf("%#08x", x.XID), x.Interface, a.when(x.FirstSeen), x.Messages,
						strconv.Itoa(len(x.Offers)), dash(x.SelectedServer), x.Outcome, x.Visibility})
				}
				return a.listOut(header, rows, exchanges, func() error { return writeJSONL(a.output(), exchanges) })
			})
		},
	}
	fl := c.Flags()
	fl.StringVar(&f.Interface, "interface", "", "only this interface")
	fl.StringVar(&f.MAC, "mac", "", "only this client")
	fl.StringVar(&since, "since", "", "only exchanges seen from this time or duration ago (24h, 7d)")
	fl.IntVar(&f.Limit, "limit", 0, "at most this many rows (default 1000)")
	return c
}

// when renders a time for a table: its age, or the time itself in CSV.
func (a *app) when(t time.Time) string {
	if a.g.output == "csv" {
		return t.UTC().Format(time.RFC3339)
	}
	return a.stamp(t)
}

func (a *app) whenPtr(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return a.when(*t)
}

// configText is a configuration snapshot's value for key, "" when the ACK
// did not carry it.
func configText(cfg map[string]any, key string) string {
	v, ok := cfg[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(v)
}

// leaseText renders a lease time: seconds, infinite, or a dash.
func leaseText(v *int64) string {
	switch {
	case v == nil:
		return "-"
	case *v < 0:
		return "infinite"
	}
	return (time.Duration(*v) * time.Second).String()
}

// leaseState is open, or how the lease row ended, with PARTIAL appended.
func leaseState(l store.DHCPLease) string {
	s := "open"
	if l.EndedAt != nil {
		s = l.EndReason
	}
	if l.Partial {
		s += ",partial"
	}
	return s
}

func (a *app) interfacesCmd() *cobra.Command {
	c := &cobra.Command{Use: "interfaces", Short: "Monitored interfaces"}
	c.AddCommand(&cobra.Command{
		Use:     "list",
		Short:   "State, MAC, current prefixes, passive/active per interface",
		Example: "  sudo lan-tracker interfaces list",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.run(cmd, false, func(ctx context.Context, b backend) error {
				ifs, err := b.Interfaces(ctx)
				if err != nil {
					return err
				}
				header := []string{"NAME", "STATE", "MAC", "PREFIXES", "PASSIVE", "ACTIVE", "HOSTS"}
				rows := make([][]string, 0, len(ifs))
				for _, i := range ifs {
					passive := onOff(i.Passive)
					if i.Replay {
						passive = "replay"
					}
					rows = append(rows, []string{i.Name, i.State, dash(i.MAC), dash(strings.Join(i.Prefixes, " ")), passive, onOff(i.Active), strconv.Itoa(i.Hosts)})
				}
				return a.listOut(header, rows, ifs, func() error { return writeJSONL(a.output(), ifs) })
			})
		},
	})
	return c
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
