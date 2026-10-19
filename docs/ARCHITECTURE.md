# LAN Tracker — Architecture

Every collector emits normalised observations onto one bus; a single correlation goroutine owns hosts, bindings and events, so collectors never touch the database directly. LAN Tracker is a connectivity tracker with history, not an identification product ([ADR 0011](adr/0011-connectivity-tracker-scope.md)): it records which MACs, addresses, names, DHCP exchanges and TCP endpoints were seen on each interface, and never infers what a device is.

## 1. Data flow

```text
                      ┌──────────────────────────────┐
                      │      Interface manager       │
                      │ links, addresses, contexts   │
                      └──────────────┬───────────────┘
            ┌────────────────────────┼────────────────────────┐
            ▼                        ▼                        ▼
   ┌─────────────────┐     ┌──────────────────┐     ┌──────────────────┐
   │ Passive capture │     │ Kernel neighbour │     │  Active probes   │
   │ AF_PACKET / BPF │     │ netlink / route  │     │ scheduler, start │
   │ ARP IPv4 IPv6   │     │ socket, resync   │     │ budget, jitter   │
   │ NDP DHCP mDNS   │     │ link/addr events │     │ ARP ICMP TCP     │
   │ DNS LLDP        │     │                  │     │ name queries     │
   └────────┬────────┘     └────────┬─────────┘     └────────┬─────────┘
            └────────────────────────┼────────────────────────┘
                                     ▼
                      ┌──────────────────────────────┐
                      │ Observation bus (bounded,    │
                      │ drop counter)                │
                      └──────────────┬───────────────┘
                                     ▼
                      ┌──────────────────────────────┐
                      │ Correlator, single goroutine │
                      │ hosts, addresses, names,     │
                      │ endpoints, DHCP leases       │
                      └───────┬──────────────┬───────┘
                              ▼              ▼
                     ┌──────────────┐  ┌──────────────┐
                     │ Current state│  │ Event engine │────────┐
                     └──────┬───────┘  └──────┬───────┘        │
                            └───────┬─────────┘                │
                                    ▼                          ▼
                      ┌──────────────────────────────┐   ┌──────────┐
                      │ SQLite (WAL), single writer  │   │ log sink │
                      │ hosts.db (platform path)     │   └──────────┘
                      └───────┬──────────────┬───────┘
                              ▼              ▼
                     ┌──────────────┐  ┌──────────────┐
                     │ REST API     │  │ Prometheus   │
                     │ Unix socket  │  │ /metrics     │
                     │ → CLI, watch │  │              │
                     └──────────────┘  └──────────────┘
```

Only the correlator writes state and events. journald receives state transitions straight from the event engine, never raw observations.

LAN Tracker is Linux only, and so is the code base. The kernel-facing parts (interface manager, capture, neighbour monitoring, probe transmission) sit behind small interfaces (§3, Platform layer) so everything above them can be tested against fakes. A replay collector can stand in for live collection, e.g. in the dev container.

## 2. Key decisions

| Decision | Choice | Why |
| --- | --- | --- |
| Scope | Connectivity tracker with history; no device identification, classifiers or payload probes (ADR 0011) | What operators need after the fact is which MAC held an address, its names, DHCP and reachable ports; identification guesses are neither reliable nor safe to probe for |
| Host identity | Within a context, host = MAC (one-to-one); internal UUID `host_id` as record key; no merge/split | The MAC is the only reliable same-device evidence on L2; multi-NIC devices are several hosts, a NIC swap is a new host |
| Network scoping | Every host keyed by `network_context` = interface; subnets kept as history | Same IP or MAC on `eth0` and `eth1` is not the same host; readdressing does not split history |
| Data flow | Collectors → observation channel → correlator → state + events → SQLite | Collectors stay independent and testable; one writer avoids lock contention |
| Concurrency | One correlator goroutine; collectors and probes in their own goroutines under `errgroup` | Deterministic ordering of state changes |
| Storage | SQLite in WAL mode, single writer, batched transactions | Atomic, crash-safe, one portable file, queryable |
| SQLite driver | `modernc.org/sqlite` (pure Go) | No cgo; static cross-compile |
| Packet capture | `AF_PACKET` TPACKET_V3 via `gopacket/afpacket`, classic-BPF filter (`golang.org/x/net/bpf`) | No cgo, no libpcap; kernel-side filtering keeps CPU low |
| Neighbours and interfaces | `vishvananda/netlink` dump + `RTM_NEWNEIGH`/`RTM_DELNEIGH`, link and address subscriptions, resync after `ENOBUFS` and periodically | Event-driven, no `ip neigh` polling |
| ARP send | Request frames built with `gopacket/layers`, sent and received on an `AF_PACKET` socket bound to the interface behind the platform `Transmitter` (no `mdlayher` libraries); no NDP probing (v1 is IPv4-first) | Needs only `CAP_NET_RAW`; protocol logic stays out of the platform layer, so engines are tested against fakes |
| TCP probes | Bare connect: plain `net.Dialer` connect with timeout, bound to the interface with `SO_BINDTODEVICE`, SYN retransmissions capped (`TCP_SYNCNT` 2), closed at once with a RST (`SO_LINGER` 0); never a write. Dialled only when the kernel's route to the address is direct on that interface with a source address, never because a ping answered | Safe for OT: no payload, at most 3 packets per connect (§5), no connection left in `TIME_WAIT` or `CLOSE_WAIT` on either side; classifies OPEN / REFUSED / TIMEOUT / UNREACHABLE / LOCAL_ERROR; leaves via the probed interface |
| Active rates | One shared budget of probe starts per second: every ARP request, ICMP echo, name query send and TCP connect is one start (ADR 0011) | A contract the application can keep; the kernel's own packets (handshake ACKs, retransmissions, the close) are bounded by timeouts and concurrency, not paced |
| Presence | ACTIVE / RECENT / STALE / MISSING, configurable thresholds | Quiet PLCs are not offline |
| Logging | `log/slog` with a journald handler; text or JSON on a terminal or in a container; transitions only | No journal spam |
| Service manager | systemd, `Type=notify` with watchdog | Readiness, supervision and sandboxing in one place |
| CLI | One binary with cobra subcommands; client over Unix socket, `--offline` reads DB | Single static operational tool |
| Build | Go 1.26+, `CGO_ENABLED=0`, `linux/amd64` and `linux/arm64`; minimum kernel 5.10 | Fits target hardware; static binary, no runtime deps |
| Development | Linux-only code base; every build, lint and test runs in a Linux dev container via `make` (§9), on Linux or macOS hosts | One toolchain and one OS everywhere; no stubs or build tags |
| Replay | pcap/pcapng or JSONL replay through the real decoders and correlator, with a simulated clock | Reproduce site behaviour and run golden tests on any machine |
| Delivery | One tarball per target (static binary, `capcheck`, the reference files from `deploy/`) + SHA-256 checksums | No packaging requirement |

## 3. Components

### Interface manager (`internal/iface`)
Enumerates links and addresses through netlink, derives one network context per configured interface name, records the interface's own prefixes over time (`context_prefixes`), follows link and address changes at runtime, and publishes each configured interface's state (present, up, own prefixes) on the observation bus. The correlator turns changes into `context_prefixes` history and `INTERFACE_UP`, `INTERFACE_DOWN` and `SUBNET_CHANGED` events, in order with observations, so the on-link check (`DATA_MODEL.md` §5.2) always uses the prefixes in effect at the time.

### Observation bus (`internal/observation`)
A bounded Go channel (4,096 messages) carrying `Observation` values (see `DATA_MODEL.md` §2), interface states, collector state changes (for `OBSERVATION_GAP`), operator actions and barriers. When full, new observations from live collectors are dropped and `lan_tracker_bus_dropped_total` increments; collectors never block on the correlator. Interface and collector states, operator actions and replayed observations are never dropped (they wait); an operator action returns once the correlator has processed it. A barrier returns once everything before it has been processed; shutdown and replay use it to drain the bus.

### Passive capture (`internal/collect/capture`)
One receive-only `AF_PACKET` TPACKET_V3 ring per interface (`passive.ring_size`, default 2 MiB, in 128 KiB blocks handed over at least every 100 ms) with a classic-BPF filter built from `passive.protocols`: ARP, LLDP (EtherType 0x88cc), NDP (ICMPv6 133–137), DHCP (67/68), mDNS (from 5353) and DNS responses (from 53) are captured whole; other IPv4 and IPv6 frames are truncated to their first 96 bytes for source-address learning; everything else is dropped in the kernel. `ipv6: false` drops every IPv6 frame, NDP and mDNS or DNS over IPv6 included. Frames of a VLAN are dropped too (VLANs are out of scope in v1): the kernel strips 802.1Q tags before the filter runs, so the filter checks the tag through the BPF VLAN extensions and keeps only untagged and priority-tagged (VLAN ID 0, common with PROFINET devices) frames. The host's own frames are never evidence: the filter drops what the kernel marks `PACKET_OUTGOING`, and the capture source skips frames from the interface's own MAC, which a bridge port in hairpin mode reflects back, as well as frames that reached the ring before the filter was attached. Promiscuous mode is set with `PACKET_ADD_MEMBERSHIP` when enabled and dropped with the socket. An administratively down interface is not opened; when the interface goes down or disappears the ring is closed. Either way capture retries with back-off (1 s doubling to 30 s, starting over only once frames arrive), shows as failed meanwhile, and logs each distinct failure once. Kernel receive and drop counters are kept per interface across reopens (`lan_tracker_capture_drops_total`).

Decoders (`capture/decoders`) are pure functions from an Ethernet frame to zero or more observations, shared with pcap replay, each with a fuzz target; they never panic, never trust a length in the frame, and bound every list and string. The MAC evidence is the ARP sender hardware address, the NDP link-layer address option, or else the frame's source MAC; LLDP's attachment is the Ethernet sender (the switch port), and its chassis ID is metadata. Each observation is classified by the evidence it carries (`Observation.Evidence()`, `DATA_MODEL.md` §2): a frame from the MAC, an address confirmed at it, an endpoint that answered, a cached or asserted fact, or none; only the first three refresh presence.

- **DHCPv4.** The whole message is decoded into one observation (`observation.DHCPMessage`): the BOOTP header, options split across several instances joined (RFC 3396), `file` and `sname` when option 52 overloads them, and the options the tracker interprets (mask, routers, DNS servers, hostname, domain, requested address, lease time, message type, server identifier, request list, T1/T2, vendor class, client identifier, FQDN, relay agent information, search list, classless routes). Malformed options are named and the rest kept. The direction comes from the BOOTP op agreeing with the message type, not the ports, so relay traffic from 67 to 67 decodes too. A client message is `passive_dhcp`; its sender is the client, with its `ciaddr` claim and hostname, only when the frame came straight from it (Ethernet source is `chaddr`, no `giaddr`). A server reply is `passive_dhcp_server` from the server or the relay that sent it, with the server identifier (option 54) kept apart from the sender. The correlator turns these into `dhcp_servers`, `dhcp_exchanges` and `dhcp_leases` (`DATA_MODEL.md` §5.6); a lease is the server's statement and never makes its client present.
- **mDNS and DNS.** An mDNS response is one observation of its sender with its records (A, AAAA, PTR, SRV, bounded TXT, each with TTL and cache-flush bit); the records describe what the sender advertises and never bind an address to it, so a proxy or reflector cannot merge attachments. Unicast DNS answers (PTR, A, AAAA, CNAME) carry no MAC. Both become name records (`DATA_MODEL.md` §5.4).
- **LLDP, ARP, NDP, IPv4, IPv6.** LLDP management addresses and system name go into `meta` and name records; ARP and NDP give "IP is at MAC"; other IPv4 and IPv6 frames give source-address learning only.

Identical observations (same source, MAC, IP, name and metadata) are emitted at most once per 2 minutes per interface, which keeps presence fresh (ACTIVE is 5 min) while bounding the volume written to `observations`.

On a switched port, capture sees traffic to/from the box, broadcast, multicast, ARP, mDNS, IPv6 multicast and some switch control traffic, but not unicast between other devices unless the port is mirrored. That is expected and sufficient for discovery.

### Replay collector (`internal/collect/replay`)
Reads JSONL files of `Observation` values, or pcap/pcapng captures (Ethernet link type; `.pcap.gz` and `.pcapng.gz` are read compressed) decoded by the capture decoders with the same refresh suppression as live capture, skipping packets a pcapng file records as outbound (a classic pcap records no direction, so a capture taken on the box includes its own frames), merges every replay interface's file into one time-ordered stream, and emits them with their recorded source and timestamp. Paths are relative to the working directory. Replay and live interfaces cannot be mixed: a replay runs the whole daemon on recorded time. In `speed: 0` mode it advances a simulated clock (`internal/clock`) to each observation's timestamp before publishing it, never dropping it (it waits while the bus is full); the correlator has no ticker in replay, so presence and expiry timers fire exactly as they would have at the site; `speed: N` replays in real time × N. When every replay source is exhausted the daemon commits its last batch (the writer's flush ticker runs on the simulated clock, which stops), logs `replay data committed`, and keeps serving the API (or exits with `replay.exit_when_done: true`, used by tests).

### Clock (`internal/clock`)
All components that read time (correlator, presence ticker, expiry, scheduler, compaction) take a `clock.Clock`. Production uses the wall clock; replay and tests use a simulated clock.

### Platform layer (`internal/platform`)
Five small interfaces over the kernel facilities, each also naming its `Backend()`. Nothing outside `internal/platform` and `internal/service` uses AF_PACKET, netlink or raw sockets directly, so collectors, the scheduler and the daemon are tested against fakes.

| Interface | Methods (sketch) | Linux implementation |
| --- | --- | --- |
| `Capturer` | `Open(ctx, iface, CaptureOptions{Filter, Promiscuous, RingSize}) (FrameSource, error)`; the `FrameSource` has `ReadFrame`, `Stats()` and `Close` | `AF_PACKET` TPACKET_V3 ring |
| `NeighborSource` | `Snapshot(ctx) ([]Neighbor, error)`, `Watch(ctx) (<-chan NeighborEvent, error)`; entries carry the NUD state and the confirmation age | rtnetlink neighbour dump and subscription |
| `InterfaceMonitor` | `List(ctx)`, `Watch(ctx) (<-chan LinkEvent, error)` | rtnetlink link and address dump and subscription |
| `Transmitter` | `Frames(iface, etherType) FrameConn` (write and read frames; each read frame carries its interface), `DialTCP(iface, addr, timeout)` (bare connect, never writes), `DialUDP(iface, addr)` (unicast DNS to a configured resolver), `ListenUDP(iface)` (one-shot mDNS queries; responders answer by unicast), `ICMPConn(iface) EchoConn`, `Route(iface, dst)` (the kernel's route: interface, gateway, source address), `Backend(transport)` (`afpacket` for frames, `ping socket` or `raw socket` for ICMP, `socket` for TCP and UDP) | `AF_PACKET` socket bound to the interface and EtherType (the host's own and outgoing frames are not read back), `SO_BINDTODEVICE` TCP and UDP sockets, a ping socket or, where `net.ipv4.ping_group_range` forbids it, a raw ICMP socket bound to the interface, an unprivileged `RTM_GETROUTE` restricted to the interface |
| `ClockSource` | `State()`: `synced`, `unsynced` or `unknown` | read-only `adjtimex` of the kernel's NTP status (§8) |

Each backend reports availability per interface (`running`, `disabled`, `unsupported`, `failed` with error) to the collector registry, which feeds `daemon status` and `lan_tracker_collector_up`; the scheduler adds each probe's last periodic pass (sent, answered, blocked, duration) for `daemon status`, and never logs passes. Shared tests run against fake implementations of these interfaces; the Linux implementations are tested in Docker (§9).

### Neighbour collector (`internal/collect/neighbor`)
At startup subscribes to `RTM_NEWNEIGH`/`RTM_DELNEIGH` and then dumps the neighbour table, so no change between the two is missed, and takes a fresh dump after notification loss (`ENOBUFS`: the backend resubscribes and signals a resync) and every `neighbor.resync_interval` (default 10 min). Emits source `kernel_neighbor` with the NUD state as `neighbor_state` for resolved entries only (REACHABLE, STALE, DELAY, PROBE; not INCOMPLETE, FAILED, NOARP or PERMANENT). Each observation is stamped with the time the kernel last confirmed reachability (`ndm_confirmed`), and an entry is emitted again only when it is reconfirmed or its MAC changes. A REACHABLE entry confirms the address at the MAC; a STALE, DELAY or PROBE entry is cached evidence: it may record a MAC or address never seen before, but never makes a known host present again or confirms a binding. A STALE entry that lingers therefore never makes a switched-off device look present, and STALE is never read as offline either.

### Active probes (`internal/probe`)
Four engines, ARP (`probe/arp`), ICMP (`probe/icmp`), bare TCP connect (`probe/tcp`) and name queries (`probe/names`), build their packets themselves and send through the platform `Transmitter`; there is no IPv6 NDP probing (v1 is IPv4-first). Engines only emit observations (`arp_scan`, `icmp_scan`, `tcp_connect`, `dns_query`, `mdns_query`) on the bus; they never touch state. There are no identification, banner or payload probes and no other UDP (ADR 0011); a new probe type changes the safety contract and needs a recorded decision first.

- **ARP** sweeps the configured networks and is the primary discovery mechanism. The sender address is the interface's address in the target's subnet, or 0.0.0.0 without one (an RFC 5227 ARP probe), so candidates outside the interface's own subnet are still asked.
- **ICMP and TCP** probe known hosts only: the open IPv4 bindings inside those networks (plus, in an operator scan, the hosts that answered its ARP sweep). An ICMP echo reply is extra positive evidence; TCP never waits for it.
- **TCP** first asks the kernel for the route (`Transmitter.Route`): without a direct route on the interface and a source address on it, nothing is dialled, no start is spent, and the endpoint records `LOCAL_ERROR` with why (routed via a gateway, no source address, no route). `SO_BINDTODEVICE` picks the interface but does not make a destination directly connected; a `/32` field interface reaches its candidates only through direct routes the operator adds. A connect writes nothing and closes at once with a RST. Results are OPEN, REFUSED, TIMEOUT, UNREACHABLE or `LOCAL_ERROR`; only OPEN and REFUSED are answers. The correlator keeps them per interface, address and port (`endpoints`, `DATA_MODEL.md` §5.5).
- **Name queries** (FR-AC-12, `active.names`, off by default) ask only about what is already known: one-shot mDNS queries to 224.0.0.251:5353 for the interface's known `.local` names, the configured DNS-SD types and, if asked, one `_services._dns-sd._udp.local` enumeration whose answers are not chased; unicast DNS PTR for known addresses and A and AAAA for known DNS names through the first configured resolver only. Names and addresses a record's TTL still covers are not asked; negative answers are kept for their SOA TTL (5 min to 1 h); CNAMEs are followed at most twice; a pass asks at most `max_queries_per_pass`. Answers are name records only: an mDNS answer carries the responder's address, never a MAC, and makes nobody present; a failure or negative answer changes nothing.

Before every send an engine acquires the shared budget (`probe.Budget`). It checks the policy (kill switch, active discovery on the interface, target inside the configured networks, IPv4, not excluded; a name query only to the mDNS group or a configured resolver) before it waits and again right before it returns, waits for the target's spacing before it takes anything, so a probe waiting for its target holds up neither probes to other targets nor other interfaces (name queries are outside the spacing, which is for probes of one device), then takes a slot of the global concurrency semaphore (TCP also the per-interface and per-host ones) and books the send time: the earliest moment that respects the target's spacing, the protocol budget and the global start budget, booked in all three at once. The contract is probe starts, not packets (ADR 0011): every ARP request, ICMP echo, name query send and TCP connect is one start of its protocol budget and of the global one; the kernel's own packets (its ARP to resolve a probed host, handshake ACKs, SYN retransmissions, the RST) are bounded by timeouts and concurrency instead (§5). Budgets are paced (the next start is due 1.02/rate after the previous one, about 2% below the rate) and enforced over a sliding window of 1.02 s; a probe that leaves late (a timer that fired late, a goroutine not scheduled at once) is re-booked at the moment it actually leaves, so the budgets count real send times and no one-second window holds more starts than a budget allows. A probe holds its concurrency slot until its reply or timeout, so with 4 jobs in flight and a 1 s ARP reply timeout a sweep of mostly empty addresses runs at about 4 requests/s, not 5. Every wait is counted per protocol and reason (`lan_tracker_probe_throttled_total`). `probe.Simulate` replays the same pacing, spacing and slots on a simulated clock for the planner. A reload applies new rates and spacing at once; the concurrency caps keep their size until a restart (§6).

The scheduler (`probe/scheduler`) runs one loop per configured interface and protocol; a loop probes while its interface has active discovery enabled, which follows reloads. The first pass waits for the startup delay plus a random share, up to `jitter`, of the interval; later passes follow the interval ±`jitter`; targets go in random order. An ARP sweep is never listed: its addresses (the networks less excludes and the interface's own addresses, `internal/netrange`) are counted and walked in a random order by a keyed permutation, so a sweep needs constant memory however wide it is. ICMP and TCP passes skip targets in back-off. Back-off is kept per interface, address, probe type and port (TCP), so one probe that goes unanswered never slows another: after three unanswered probes in a row (no reply, TIMEOUT or UNREACHABLE) that probe goes to the address at four times its interval. An answer to it ends the back-off; fresh positive evidence for the address (the open binding confirmed after the last miss: passive traffic, the neighbour table, an answer to another probe) eases it, so the next probe goes at the normal interval and one more miss brings the back-off back at once. ARP sweeps and name queries are not backed off (name queries keep negative answers instead). TCP passes go port by port, so consecutive connects reach different hosts. A pass that takes longer than its interval is flagged `overran` in `daemon status`; missed passes are never made up. A reload applies new budgets at once and new intervals, enabled probes and each interface's `active` settings at each loop's next step; the policy checks every probe against the new configuration at once, so disabling an interface or narrowing its networks stops the probes it no longer allows immediately. A reload that enables an interface starts its passes after the startup delay and jitter, as at start-up. The kill switch cancels running passes and scans at once; the loops then wait for it to clear. Operator scans (`scan run`) go through the same budgets, one interface after another: the ARP sweep (its known hosts first, so the probes after it never wait out the target spacing behind their own ARP request), then ICMP and TCP port by port on the known hosts and the ARP responders, in one random order for every phase; phases are barriers. Operator scans send no name queries. The scheduler records a scan through the correlator (an operator message on the bus: a `scans` row, `SCAN_STARTED`, `SCAN_COMPLETED` with counts), as the daemon does the kill switch (`runtime_state`, `ACTIVE_DISABLED`/`ACTIVE_ENABLED`); once the correlator has stopped, the bus is closed and such requests fail rather than wait. The planner (`probe.Compute`) computes `scan plan` and gates `scan run` identically. It lists known hosts without a direct route, which a TCP check would not dial, and leaves them out of its estimate. The estimate replays the budget's pacing, target spacing and concurrency slots on a simulated clock (`probe.Simulate`; the first 20,000 addresses of a longer sweep, extrapolated at the pace reached), in the order a run uses, and gives two durations with the assumptions behind them: the estimated typical duration (known hosts answer at once; most swept addresses are empty, so only the sweep waits out its reply timeout) and the estimated no-response duration (every phase waits out its reply timeout). See §5.

### Correlator (`internal/correlate`)
Single goroutine, which also holds the in-memory current state. Loads the current state (contexts and their prefixes, hosts, open bindings, TCP endpoints, open name records, DHCP servers and open leases, the kill switch, open scans) at startup, applies the correlation rules (`DATA_MODEL.md` §5) to each bus message in order, and hands state changes to the store and events to the event engine. A host is one MAC per interface and is never merged or split; a MAC's addresses are independent (a new address never closes another; an address unconfirmed for `identity.address_expiry` while its host is seen is closed as an inference, and a takeover by another MAC closes it while the holder is not live). Only evidence that refreshes presence (§3, Passive capture) moves `last_seen`; hosts keep the times of the last frame, the last address confirmation and the last endpoint answer apart. Besides hosts and bindings it keeps:

- **TCP endpoints** (`endpoints.go`): results per interface, address and port, with the last result and the last definite answer (OPEN or REFUSED) kept apart, so a timeout never erases an earlier OPEN; the MAC is an attribute only when exactly one MAC held the address; each change is a row of `endpoint_results` and a `SERVICE_STATE_CHANGED` event when the endpoint opens or leaves OPEN.
- **Name records** (`names.go`): every advertised or resolved record with its type, value, source, advertiser and TTL, linked to a host only by evidence (a NAME to the device that sent it, an address record to the one MAC holding the address). Records end only on evidence (an mDNS goodbye, a cache-flush replacement, the address passing to another MAC), never by time; only when more than 16,384 records are open are the least recently seen ended (`evicted`, without an event); `preferred_name` is a display choice.
- **DHCP** (`dhcp.go`): servers with their allowlist verdict, one exchange per transaction (interface, xid, `chaddr`; at most 1,024 open per interface, closed as `incomplete` after a minute idle), and per-client lease history compared on the options both ACKs carried (`DATA_MODEL.md` §5.6).
- **Collection gaps** (`gaps.go`): a time during which nothing was observed on an interface, because a passive collector or the daemon was not running (at least a minute, from the `observed_until` heartbeat in `runtime_state`), recorded once it is over as `OBSERVATION_GAP`; history inside a gap is unknown, not quiet.
- **Proxy ARP** (`proxyarp.go`): a diagnostic flag, `hosts.proxy_arp`, announced by `PROXY_ARP_DETECTED`, for a MAC whose ARP replies claim two or more addresses held by other live hosts, or more than `identity.proxy_arp_threshold` addresses (`DATA_MODEL.md` §5.2); a flagged host's replies then only refresh addresses it holds on other evidence.

Time is data-driven: rules use the observation's time and timer transitions (presence, expiry) are stamped with the moment their threshold was crossed. A ticker re-evaluates them in live mode (every 15 s); replay has no ticker, so results are identical however fast it runs. The correlator assigns row ids itself, so it can refer to rows the writer has not committed yet. Observations that match no host are stored unbound and counted.

### Event engine (`internal/events`)
Builds events from state transitions, attaches the evidence snapshot (`DATA_MODEL.md` §7), writes them to the store and to journald.

### Store (`internal/store`)
Owns the single SQLite writer goroutine, migrations, repositories, batched commits (target 5 s), retention, roll-up and compaction (`DATA_MODEL.md` §9). Besides observations and roll-ups, compaction prunes `endpoint_results`, ended name records and ended DHCP leases with the events' retention and DHCP exchanges with the roll-ups'; open leases are kept however old. Compaction runs at start-up and hourly (not in replay mode, so a replay's database depends only on the recorded data); each step touches at most 2,000 rows and is one committed op in the writer's queue, so it never holds the writer for long. The size cap compares the data size (pages minus free pages) with `max_db_size`; new databases use `auto_vacuum=INCREMENTAL`, so compaction returns freed pages to the file system. At start-up the store runs `PRAGMA quick_check`; a database that fails it, or is not a database at all, is renamed with its `-wal` and `-shm` files to `<path>.corrupt-<UTC time>` and a new one is created, which the daemon logs as an error and the correlator records as `DATABASE_RECREATED` (FR-ST-4). Exposes read-only query functions used by the API and by `--offline`; the offline open procedure is specified in `CLI.md` §1. On clean shutdown the writer runs `PRAGMA wal_checkpoint(TRUNCATE)` and closes, so the WAL is removed.

### OUI lookup (`internal/identify`)
Despite the package name, nothing here identifies devices (ADR 0011): it holds the OUI lookup and MAC helpers (locally administered, unicast). The OUI answer is shown as the OUI registrant (`oui_registrant`): the IEEE registrant of the MAC prefix, a hint about the network interface, never the equipment's maker. The lookup takes the longest prefix (MA-S, MA-M, MA-L) from a precomputed table embedded in the binary (`data/oui/oui.bin`, about 54,000 assignments, 1.2 MB; format in `internal/ouitable`: sorted keys per prefix length, name offsets and each distinct name once), searched in place by binary search, so start-up parses nothing and the table takes no heap (parsing the text registry took about 0.6 s of CPU on a CM4, 6 s under the unit's `CPUQuota=20%`, ADR 0010). `make oui` downloads the IEEE registries each release, writes them readable as `data/oui/oui.tsv.gz` and derives `oui.bin` from them; a test keeps the two in step and checks that every lookup answers as the text registry does. An optional override file (`identity.oui_override`: lines `PREFIX[/bits] Name`, e.g. `00:1B:1B Siemens` or `02-42-AC-1F/24 Lab`) takes precedence at the same prefix length; it is read at start-up. The locally-administered bit is recorded per host.

### API (`internal/api`) and CLI (`internal/cli`)
REST over `/run/lan-tracker/api.sock` (mode 0660, owner root under the reference unit), optional `127.0.0.1` listener that also serves `/metrics`; specified in `API.md`. The API answers from `store.Reader` on a read-only connection pool, the same queries `--offline` runs on the database file, so online and offline answers are identical; it sees what the writer has committed (at most one 5 s batch behind), and `/v1/events/stream` is fed live by the event engine. The CLI (`CLI.md`) is an API client by default and a read-only DB reader with `--offline`; both behind one interface, so every read command works both ways. Changes go through the daemon only: the operator endpoints that change state (kill switch, scans, host descriptions) hand their action to the correlator and answer once it is committed, so the single writer holds. A configuration reload changes no stored state: the daemon swaps its running configuration atomically and logs the reload with its caller (§6).

### Metrics (`internal/metrics`)
`lan_tracker_hosts{interface,presence}`, `lan_tracker_events_total{type}`, `lan_tracker_probe_total{interface,protocol,port,result}`, `lan_tracker_scan_duration_seconds{interface,protocol}` (the last pass or operator scan), `lan_tracker_observations_total{source}`, `lan_tracker_bus_dropped_total`, `lan_tracker_capture_drops_total{interface}`, `lan_tracker_db_size_bytes`, `lan_tracker_observations_unbound_total{source}`, `lan_tracker_active_disabled` (0/1), `lan_tracker_probe_throttled_total{protocol,reason}`, `lan_tracker_collector_up{interface,collector}`, `lan_tracker_dhcp_servers{interface,status}` (`API.md` lists their meaning). The daemon gathers them at scrape time (`internal/daemon`, `server.go`); `internal/metrics` is a small writer for the Prometheus text format; it refuses any label outside a fixed low-cardinality list, so no MAC, IP, hostname or host-ID label can appear.

## 4. Repository layout

```text
lan-tracker/
├── cmd/lan-tracker/            main.go: cobra root, daemon + CLI subcommands
├── internal/
│   ├── config/                  YAML schema 1, env (LAN_TRACKER_*), flags, defaults, validation, reload diff
│   ├── platform/                Capturer, NeighborSource, InterfaceMonitor, Transmitter; fakes in platform/fake
│   ├── service/                 sd_notify notifier and journald slog handler
│   ├── iface/                   interface manager: contexts, prefixes, up/down (via platform.InterfaceMonitor)
│   ├── observation/             Observation type, sources, evidence kinds, DHCPMessage; bounded bus with drop counters
│   ├── clock/                   wall clock and simulated clock
│   ├── collect/
│   │   ├── capture/             capture loop over platform.Capturer, BPF filter; decoders/: arp, ip, ndp, dhcp, dns (mDNS, DNS), lldp
│   │   ├── neighbor/            snapshot + watch + resync over platform.NeighborSource
│   │   └── replay/              pcap/pcapng and JSONL replay
│   ├── netrange/                IPv4 range arithmetic: sweep sizes, excludes, a random walk without a list
│   ├── probe/                   start budget (rates, concurrency, spacing, Simulate), policy and kill switch, targets,
│   │                            back-off, planner
│   │   ├── scheduler/           periodic passes per interface and protocol, operator scans
│   │   ├── arp/  icmp/  tcp/    engines (transmit via platform.Transmitter); tcp: route check, bare connect
│   │   ├── names/               name queries: one-shot mDNS, unicast DNS via configured resolvers
│   │   └── probetest/           shared test helpers for the engines
│   ├── correlate/               the only writer of state: observation → host, bindings, conflicts, presence; TCP endpoints,
│   │                            name records, DHCP servers, exchanges and leases, proxy-ARP flag, operator actions
│   ├── events/                  event types, emitter, journald sink
│   ├── store/                   SQLite: migrations, writer, batching, retention/compaction, integrity; read model and
│   │                            queries (Reader) for the API and --offline; storetest/ seeds test databases
│   ├── identify/                OUI lookup (registrant hint only) and MAC helpers; no classifiers
│   ├── ouitable/                format of the precomputed OUI table (oui.bin), searched in place by identify
│   ├── api/                     REST over Unix socket + optional 127.0.0.1 TCP (server and the CLI's client)
│   ├── buildinfo/               version, commit, date stamped via -ldflags
│   ├── daemon/                  lifecycle: config, store, collectors, SIGHUP reload, watchdog, shutdown
│   ├── logging/                 slog handler selection (journald, text, json)
│   ├── metrics/                 Prometheus registry, low-cardinality collectors
│   └── cli/                     API client, offline DB reader, table/json/jsonl/csv output
├── migrations/                  numbered .sql files embedded with go:embed (0001_init.sql: the initial schema)
├── data/oui/                    OUI registry (oui.tsv.gz), the precomputed table it embeds (oui.bin) + generator tool
├── build/                       dev container (dev.Dockerfile) and the scripts make runs in it
├── deploy/                      systemd unit, default config, config.dev.yaml (replay), install guide
├── test/
│   ├── golden/                  the reconstruction scenario (JSONL stream + expected bindings, events, queries), the pcap
│   │                            fixture replayed through the daemon, the same queries through the CLI
│   ├── net/                     Docker network tests: run.sh + Go tests (build tag nettest); inject/ frame injector
│   ├── systemd/                 the reference unit under systemd on Debian 11, 12 and 13 (make test-systemd)
│   ├── crash/                   SIGKILL during writes, then the integrity check (runs in make check)
│   ├── soak/                    90 simulated days of a 50-host LAN (build tag soak, make soak)
│   ├── frames/                  frame builders (gopacket) for tests, fixtures and the injector
│   └── fixtures/                synthetic pcap fixture and its generator (`make fixtures`)
├── tools/capcheck/              privilege and feasibility check (Linux; shipped in the release tarballs)
├── docs/                        specifications, USER_GUIDE.md, ROADMAP.md (open work), TEST_PLAN.md, AI_REPO_MAP.md, adr/
├── .github/                     CI and release workflows; copilot-instructions.md
├── .cursor/rules/               Cursor rules (point to AGENTS.md)
├── Makefile
├── RELEASE.md                   release notes, one `## vX.Y.Z` section per release (the release workflow requires it)
├── AGENTS.md                    instructions for every coding agent (hard rules, map, tasks)
└── CLAUDE.md                    Claude Code notes; imports AGENTS.md
```

Tooling: a `Makefile` (`make` lists every target) whose Go commands all run in the Linux dev container (`build/dev.Dockerfile`: Go toolchain, pinned golangci-lint; caches on a named volume; runs as the calling user). Targets: `build` (`bin/lan-tracker` for the Docker host's architecture), `release` (static `linux/amd64` and `linux/arm64` binaries, version, commit and commit date via `-ldflags`, so a commit always builds the same bytes, static-linking check, `SHA256SUMS`); hygiene targets `fmt`/`fmt-check`, `tidy`/`tidy-check`, `mod-verify`, `vet`, `lint` (golangci-lint, including the `nettest`-tagged tests) and `vuln` (`govulncheck`, pinned as a Go tool in `go.mod`); `test`, `coverage`/`cover` (coverage of `internal/` from every test package, failing below 90%), `fuzz` (every `Fuzz*` target, `FUZZTIME` each), `chaos` (the chaos and parallel suites at length), `soak` (90 simulated days, build tag `soak`); the Docker suites `test-net` and `test-systemd` (§9); the gates `check` and `check-all`; `tools` (`capcheck` for both targets into `dist/tools/`), `oui`, `fixtures`, `shell`, `run-dev`, `clean` and `clean-cache`. `package` (after `release` and `tools`) packs one reproducible tarball per target, `dist/release/lan-tracker-<version>-linux-<arch>.tar.gz` (binary, `capcheck`, the reference unit and configs from `deploy/`, `deploy/README.md`, `LICENSE`, and a `SHA256SUMS` of the two binaries), with `SHA256SUMS` for the tarballs. CI runs the same targets: `make mod-verify check`, `make fuzz`, the Docker suites and `make package`. Releases are cut from `main` by the manual `release` workflow (`.github/workflows/release.yml`): it refuses to run off `main` or without a `## vX.Y.Z` section in `RELEASE.md` (the release notes), takes a patch, minor or major bump, computes the next version from the highest `vX.Y.Z` tag (`build/next-version.sh`; the first is v0.1.0), runs the CI gates on the commit, builds the tarballs twice and compares their checksums, checks the amd64 binary reports the version, then creates the annotated tag on that commit and publishes the GitHub Release with the two tarballs and `SHA256SUMS`.

## 5. OT safety controls

The rates are probe starts per second, not packets on the wire (ADR 0011): one ARP request, ICMP echo, name query send or TCP connect is one start. These defaults are a modest starting profile, not a measured guarantee for every OT device.

| Control | Default | Enforcement |
| --- | --- | --- |
| Active discovery | Disabled | Must be enabled per interface |
| Scan scope | Configured `networks` only | ARP sweeps the configured networks; ICMP and TCP probe only known hosts inside them; name queries ask only about known names and addresses and configured DNS-SD types. Discovered subnets are never scanned automatically |
| Max auto-scan prefix | /24 IPv4 | Wider prefixes refused at config validation unless `allow_wide_scan: true` |
| Sweep size | 1,024 targets per interface | The limit is a target count, not a prefix: an interface's networks, less excludes, may hold at most `max_sweep_targets` addresses (default 1,024: four /24s, about 4.3 minutes: 5 ARP requests/s, but an unanswered request holds one of the 4 concurrency slots for its 1 s reply wait, so an empty sweep runs at about 4/s). Above the default is an expert override: it needs `allow_wide_scan`, and the global and ARP rates and the concurrency cap may not exceed their defaults, so a wider sweep only takes longer, never sends faster. `config validate` shows each interface's sweep size and duration and warns when a sweep cannot fit its interval; an operator sweep of more than 1,024 addresses is refused unless `--allow-wide` is given (review `scan plan` first), and one over `max_sweep_targets` is always refused. Validation keeps the value at most 2^24 (a /8) |
| IPv6 | No active probes | v1 is IPv4-first: no IPv6 sweeping and no NDP probing; IPv6 and NDP are decoded passively |
| Start rate | 5 starts/s total | One budget shared by all engines and interfaces, periodic passes and operator scans, paced and enforced over a sliding 1.02 s window, with no accumulated burst. The kernel's own packets (its ARP to resolve a probed known host, handshake ACKs, SYN retransmissions, the RST close) are not counted: they are bounded by timeouts, `TCP_SYNCNT` and concurrency |
| Per-protocol rate | ARP 5 starts/s, ICMP 1, name queries 1, TCP 1 connect/s | One budget per protocol beneath the global one, each at most the global rate |
| Concurrency | 4 jobs in flight | Semaphore across engines and interfaces, held until the reply or timeout; restart to change |
| TCP concurrency | 2 per interface, 1 per target host | Extra semaphores in the shared budget, taken by TCP probes; restart to change |
| Per-target spacing | ≥ 5 s between any two probes to one IP on one interface | Booked with the send time; name queries are paced by their own budget instead |
| Dry run | `scan plan` | Same planner as `scan run`; refuses identically; lists known hosts without a direct route; estimated typical and no-response durations with their assumptions |
| Cadence | ARP sweeps, ICMP and TCP checks every 15 min, name queries every 30 min | A pass that overruns its interval is flagged in `daemon status`; missed passes are never made up |
| Startup delay | 30 s + up to `jitter` × interval | Avoids ~1,000 boxes probing at once after a fleet update |
| Interval jitter | ±10% | Desynchronises sites over time |
| Target order | Randomised | Avoids sequential hammering |
| Excludes | Empty list | IPs/CIDRs never probed, checked before every send |
| TCP probe style | Bare connect, immediate close with RST (`SO_LINGER` 0), SYN retransmissions capped at 2, timeout 750 ms | No SYN scans, no payload, no banners, TLS, HTTP or Modbus; at most 3 packets per connect (below). Dialled only on a direct route with a source address on the interface; ICMP never gates it. Ports 80, 443, 8080 and 502 are the compiled targets, off until `active.tcp.enabled` |
| Name queries | Off (`active.names.enabled: false`) | mDNS to 224.0.0.251 only and unicast DNS to the first configured resolver only, checked by the policy before every send; TTL and negative caching; at most `max_queries_per_pass` (64) per pass. No other UDP is sent |
| Back-off | On | Per interface, address, probe type and port: 3 unanswered probes in a row (no reply, TIMEOUT, UNREACHABLE) and that probe goes at 4× interval; an answer ends it, fresh positive evidence for the address eases it (next probe at the normal interval, back at the next miss); other probes are not affected; ARP sweeps and name queries are exempt (name queries rely on TTL and negative caching) |
| Kill switch | `LAN_TRACKER_ACTIVE_DISABLED=1`, API or `active disable` | Checked before every send; running passes and scans are cancelled at once, without restart; the API/CLI setting persists across restarts. |

A bare TCP connect is one start of the global and TCP budgets, whatever its outcome. What the kernel sends for it is bounded:

| Outcome | Sent by the probe | Notes |
| --- | --- | --- |
| OPEN (connected) | SYN, ACK, RST: 3 | The handshake completes and the probe aborts at once; no data, no FIN exchange |
| REFUSED | SYN: 1 | The target's RST ends it |
| TIMEOUT | SYN plus at most 2 retransmissions: up to 3 | Retransmissions at about 1 s and 3 s (`TCP_SYNCNT` 2), so the default 750 ms timeout sends 1 SYN, up to 3 s sends 2, longer sends 3; the kernel gives up at about 7 s, which caps the wait |
| UNREACHABLE | none, or the SYNs as for TIMEOUT | The kernel found no neighbour (its own ARP is not counted, see above) or got an ICMP unreachable |
| LOCAL_ERROR | none | No direct route or source address (no start is spent), or a local failure such as a missing permission or interface; says nothing about the target |

Passive capture opens sockets for receive only, never injects frames, and promiscuous mode is a per-interface opt-in.

## 6. Configuration

The configuration is schema version 1 (`version: 1`). Validation is strict: unknown keys and any other version are errors. `deploy/config.active-example.yaml` is the reviewed ARP + TCP profile.

Precedence: flags → `LAN_TRACKER_*` environment → `/etc/lan-tracker/config.yaml` (or `$LAN_TRACKER_CONFIG`) → compiled defaults. SIGHUP (`systemctl reload`) and `lan-tracker config reload` (`POST /v1/config/reload`) re-read the file, all or nothing (FR-CFG-3): probes, each interface's `active` and `dhcp` settings, scan profiles, probe intervals, budgets, thresholds, identity settings other than the OUI override and the log level take effect at once (a DHCP allowlist at each server's next reply). Adding, removing or renaming interfaces, their passive settings, `passive`, `storage`, `api`, `metrics`, `logging.format`, `replay`, `identity.oui_override` (the table is loaded at start-up), `neighbor.resync_interval` (the neighbour collector's ticker is set up at start-up) and the concurrency caps (`active.max_concurrent_jobs`, `active.budgets.tcp.max_concurrent_per_interface` and `max_concurrent_per_host`) require a restart; those keys keep their running values, the API answer lists them, and the merged configuration is validated again before it applies. Reloads are serialised; the daemon logs each with its caller and the applied keys. The message itself (what `journalctl` shows) says what active discovery now runs, e.g. `configuration reloaded; active discovery: arp every 15m on eth0 (192.168.0.0/24)` (with ` exclude …` when excludes are set), `off` (`off (replay)` in replay mode), or `stopped by the kill switch (…)`; start-up logs the same as `active discovery: …`. Passes and sweeps are never logged.

If `logging.format` is not set anywhere (file, environment or flag) and stderr is a terminal, the daemon logs text to stderr; setting `format: journald` explicitly turns this off. If journald is configured but its socket is unreachable (e.g. `daemon run` outside systemd or in a container), it logs text to stderr with a warning.

```yaml
version: 1                           # the configuration schema
interfaces:
  - name: eth0
    passive: { enabled: true, promiscuous: false }
    active:  { enabled: false }
  - name: eth1
    passive: { enabled: true, promiscuous: false }   # true only on a mirror/SPAN port
    active:
      enabled: true
      networks: [192.168.110.0/24, 192.168.120.0/24]  # candidate IPv4 ranges on this one Ethernet segment
      exclude:  [192.168.110.1]
    dhcp:
      servers: [192.168.110.1]       # expected DHCP server identifiers (option 54); omit for no verdict
passive:
  protocols: { arp: true, ipv4: true, ipv6: false, dhcp: true, mdns: true, dns: true, lldp: true }
  ring_size: 2MiB                   # AF_PACKET ring per captured interface (256KiB-256MiB)
neighbor:
  resync_interval: 10m              # full neighbour-table dump in addition to notifications
active:                             # rates are probe starts per second, not packets (ADR 0011)
  startup_delay: 30s
  jitter: 0.1
  max_starts_per_second: 5          # every ARP request, ICMP echo, name query and TCP connect start, together
  max_concurrent_jobs: 4            # probes in flight across engines and interfaces; restart to change
  min_target_interval: 5s           # spacing between probes to one IP
  budgets:                          # each <= the global start rate
    arp:   { starts_per_second: 5 }
    icmp:  { starts_per_second: 1 }
    names: { starts_per_second: 1 }  # DNS and mDNS query sends
    tcp:   { connects_per_second: 1, max_concurrent_per_interface: 2, max_concurrent_per_host: 1 }
  max_auto_scan_prefix_v4: 24
  max_sweep_targets: 1024           # addresses one interface's sweep may cover; above: expert override with allow_wide_scan
  allow_wide_scan: false
  arp:  { enabled: true,  interval: 15m }
  icmp: { enabled: false, interval: 15m }   # extra positive evidence only; never gates TCP
  tcp:
    enabled: true
    interval: 15m
    targets:                        # connect only, closed at once, no payload; names are labels, not protocol proof
      - { port: 80,   name: http,     timeout: 750ms }
      - { port: 443,  name: https,    timeout: 750ms }
      - { port: 8080, name: http-alt, timeout: 750ms }
      - { port: 502,  name: modbus,   timeout: 750ms }
  names:                            # questions about known names and addresses only (FR-AC-12)
    enabled: true
    interval: 30m
    mdns: true                      # one-shot mDNS queries for known .local names and service_types
    service_types: [_http._tcp]     # DNS-SD types to enumerate; none by default
    enumerate_services: false       # one _services._dns-sd._udp.local query per pass; its answers are not chased
    resolvers: [192.168.110.1]      # unicast DNS (first resolver) for PTR of known addresses and A/AAAA of known names; none: no unicast DNS
    max_queries_per_pass: 64
profiles:
  modbus:
    arp: true
    icmp: false
    tcp: [502]
presence: { active: 5m, recent: 30m, stale: 24h }
identity:
  hostname_preference: [mdns, dhcp, dns, dns_ptr, lldp]
  address_expiry: 24h         # an address not confirmed this long while its host is seen is closed (an inference)
  name_expiry: 168h           # names without a TTL not confirmed this long show as stale; time never closes them
  proxy_arp_threshold: 16
  oui_override: ""            # optional path
storage:
  path: /data/lan-tracker/hosts.db
  retention:
    observations: 168h    # raw
    rollups: 2160h        # hourly roll-ups
    events: 17520h
    max_db_size: 200MB    # prune observations, then roll-ups, then oldest events
api:
  socket: /run/lan-tracker/api.sock
  listen: ""              # e.g. 127.0.0.1:9734 (also serves /metrics)
metrics: { enabled: true }
logging:
  level: info               # trace | debug | info | warn | error
  format: journald          # journald | json | text
```

Replay interface (works on any development machine; `deploy/config.dev.yaml` uses this):

```yaml
version: 1
interfaces:
  - name: eth1
    prefixes: [192.168.110.0/24]            # replaces the interface manager
    replay:
      file: test/fixtures/site-a-eth1.pcapng  # .pcap, .pcapng (optionally .gz) or .jsonl
      speed: 0                               # 0 = simulated clock, as fast as possible; N = real time x N
replay: { exit_when_done: false }
storage: { path: ./dev/hosts.db }
api:     { socket: ./dev/api.sock }
logging: { level: debug, format: text }
```

`replay` is mutually exclusive with `passive` and `active` on the same interface.

## 7. Logging

Each event is one journald entry with structured fields (`EVENT=ip_added`, `IFACE`, `HOST_ID`, `MAC`, `IP` from the evidence, `OLD_VALUE`, `NEW_VALUE`, `RELATED_HOST_ID`, `SOURCE` = the cause, `TS` = event time, `CLOCK_SYNC` when the clock is not synchronised) and a readable `MESSAGE` such as `IP_ADDED eth1 00:1b:1b:aa:bb:01 192.168.110.51`. Priority: notice for changes, info for `SCAN_STARTED` and `SCAN_COMPLETED`, warning for `DUPLICATE_IP_DETECTED`, `PROXY_ARP_DETECTED`, `ACTIVE_DISABLED`, `INTERFACE_DOWN`, `DATABASE_RECREATED`, `DHCP_SERVER_UNEXPECTED`, `DHCP_SERVER_MAC_CHANGED`, `DHCP_LEASE_NAK`, `DHCP_LEASE_DECLINED` and `OBSERVATION_GAP`, error for daemon faults (such as a corrupt database being quarantined). `journalctl -u lan-tracker EVENT=ip_added` works directly. Observations are never logged.

## 8. systemd and privileges

```ini
[Unit]
Description=LAN Tracker
Documentation=https://github.com/otfabric/lan-tracker
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
ExecStart=/usr/local/bin/lan-tracker daemon run --config /etc/lan-tracker/config.yaml
ExecReload=/bin/kill -HUP $MAINPID
# Runs as root (decided 6 Oct 2026), but the bounding set below leaves root
# only CAP_NET_RAW, and the sandbox keeps the rest of the system read-only.
UMask=0027
WatchdogSec=60
Restart=on-failure
RuntimeDirectory=lan-tracker
RuntimeDirectoryMode=0750
ReadWritePaths=/data/lan-tracker
CapabilityBoundingSet=CAP_NET_RAW
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateMounts=true
PrivateDevices=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
# The daemon reads the kernel clock's sync state with a read-only adjtimex
# (NFR-REL-2), which ProtectClock=true would block. The clock still cannot
# be changed: no CAP_SYS_TIME (the kernel refuses any change without it),
# the call filter below allows no other @clock call, and PrivateDevices
# hides /dev/rtc.
ProtectClock=false
ProtectHostname=true
ProtectKernelLogs=true
ProtectProc=invisible
ProcSubset=pid
DevicePolicy=closed
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6 AF_PACKET AF_NETLINK
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
RemoveIPC=true
LockPersonality=true
MemoryDenyWriteExecute=true
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged @resources
# No SysV or POSIX IPC: the daemon uses none. This stands in for
# PrivateIPC=, which systemd 247 (Debian 11) does not know.
SystemCallFilter=~msgctl msgget msgrcv msgsnd semctl semget semop semtimedop shmat shmctl shmdt shmget
SystemCallFilter=~mq_getsetattr mq_notify mq_open mq_timedreceive mq_timedsend mq_unlink
# Read-only clock sync state (see ProtectClock= above); listed after the
# deny line because @privileged contains @clock.
SystemCallFilter=adjtimex
SystemCallErrorNumber=EPERM
MemoryMax=128M
CPUQuota=20%

[Install]
WantedBy=multi-user.target
```

`CAP_NET_ADMIN` is not needed: netlink neighbour, link and address dumps and multicast subscriptions work unprivileged, and promiscuous mode is set through `PACKET_ADD_MEMBERSHIP` (`PACKET_MR_PROMISC`) on the packet socket rather than `SIOCSIFFLAGS` (which would need `CAP_NET_ADMIN`). `tools/capcheck` checks each operation:

| Operation | Requirement |
| --- | --- |
| `AF_PACKET` socket, TPACKET_V3 ring, BPF attach | `CAP_NET_RAW` |
| `PACKET_ADD_MEMBERSHIP` with `PACKET_MR_PROMISC` | `CAP_NET_RAW` |
| ARP / NDP transmit on `AF_PACKET` | `CAP_NET_RAW` |
| ICMP via ping socket | none, if GID within `net.ipv4.ping_group_range` |
| ICMP via raw socket | `CAP_NET_RAW` |
| rtnetlink dump and `RTNLGRP_NEIGH`/`LINK`/`IPV4_IFADDR`/`IPV6_IFADDR` subscribe | none |
| Receive frames on the ring (capture) | `CAP_NET_RAW` |
| TCP `connect()` bound with `SO_BINDTODEVICE` | none |
| `SO_BINDTODEVICE` on probe sockets | `CAP_NET_RAW` (kernels < 5.7), none on newer |
| `adjtimex` read (modes 0: clock sync state, NFR-REL-2) | none; allowed by the unit's call filter, which is why `ProtectClock=` is off |

The clock check is a read-only `adjtimex` of the kernel's NTP status (`STA_UNSYNC`, `STA_CLOCKERR`, `TIME_ERROR`), so it works whichever NTP client keeps the clock (chrony on the deployed devices; ntpd or systemd-timesyncd elsewhere) and depends on none of them. Every event records the state at write time: `synced`, `unsynced` or `unknown` (the state could not be read, or a replay). `systemd-analyze security` charges the exception three times, 0.2 each (it counts `adjtimex` as an allowed `@privileged` and `@clock` call, and `ProtectClock=` as off), partly offset by `PrivateMounts=true`, and running as root instead of a service user adds 0.3: the unit scores 2.3 on systemd 247, 252 and 257 (`make test-systemd`, Debian 11, 12 and 13), below the 2.5 limit of NFR-SEC-1. It costs no capability: changing the clock needs `CAP_SYS_TIME`, which the bounding set does not hold. The unit uses no setting newer than systemd 247: instead of `PrivateIPC=` (systemd 248) its call filter denies the SysV and POSIX IPC calls, which the daemon never makes.

Run `tools/capcheck` under the reference unit's sandbox on each target board (`tools/capcheck/README.md`) and record kernel version, board and results below. `make test-net` runs the same check in Docker on every change. If any row needs `CAP_NET_ADMIN` or root, stop and decide before continuing.

| System | Kernel | Date | Result |
| --- | --- | --- | --- |
| Docker test network (`make test-net`), uid 65534, ambient `CAP_NET_RAW` only | 7.0.14-linuxkit arm64 | 5 Oct 2026 | PASS, every row above, including ARP/NDP transmit, ping and raw ICMP, bound TCP connect; AF_PACKET refused without `CAP_NET_RAW` |
| systemd in containers (`make test-systemd`) on Debian 11, 12 and 13, daemon as root, installed with the commands in `deploy/README.md` | 7.0.14-linuxkit arm64, systemd 247, 252, 257 | 6 Oct 2026 | PASS, every row above including the `adjtimex` read under the sandbox; uid 0 with `CapEff`/`CapBnd` = `CAP_NET_RAW`; socket `root:root` 0660; clock state readable (`unsynced` in the Docker VM); exposure 2.3 on each |
| Raspberry Pi Compute Module 4 Rev 1.1, Debian 11 (systemd 247), daemon installed with the reference unit | 6.1.21-v8+ aarch64 | 6 Oct 2026 | PASS: capcheck under the installed unit's sandbox (`tools/capcheck/README.md`), every row including ARP transmit, ping and raw ICMP, bound TCP connect and the `adjtimex` read (NDP transmit skipped: there is no IPv6 probing); running daemon `CapEff`/`CapBnd` = `CAP_NET_RAW`, nothing ambient; `systemd-analyze security` exposure 2.3. Without capabilities exactly the `CAP_NET_RAW` rows fail; `net.ipv4.ping_group_range` is `0-2147483647` |

The process stays in the foreground; systemd owns its lifecycle (no double-fork). `make test-systemd` installs the release files with the commands of `deploy/README.md` in a systemd container for each target release (Debian 11 bullseye, 12 bookworm, 13 trixie) on every change, and fails on any check: readiness, root with only `CAP_NET_RAW`, the socket's owner and mode, SIGHUP and `config reload` (applied, restart-only and invalid changes), `systemd-analyze security` exposure above 2.5 (it prints the score), an unreadable clock state, capcheck under the sandbox (the board-audit commands of `tools/capcheck/README.md`: a transient unit with the installed unit's `[Service]` settings less its service plumbing), a unit setting that systemd does not know, an upgrade by re-running the install, and a clean shutdown that removes the WAL.

## 9. Development and testing

The code base is Linux only. Every make target that runs Go runs in the Linux dev container (`build/dev.Dockerfile`), so development machines, Linux or macOS, need only Docker, make and git, and CI runs exactly the same commands. The container runs as the calling user with Go, lint and module caches on the `lan-tracker-cache` volume; `make shell` opens a shell in it and `make run-dev` runs the daemon there on a replay file. On macOS, editors should run gopls with `GOOS=linux`.

| Layer | Command |
| --- | --- |
| Format, tidy, vet, lint, vulnerabilities; unit, golden and crash tests with race; coverage of `internal/` at least 90% | `make check` |
| Fuzz targets (every `Fuzz*`) | `make fuzz` |
| Chaos and parallel suites at length, with race (`CHAOS_DURATION`, `CHAOS_SEEDS`, `CHAOS_STEPS`; `LT_CHAOS_SEED` replays a seed) | `make chaos` |
| 90 simulated days of a 50-host LAN (build tag `soak`) | `make soak` |
| Network integration (Docker test network, `CAP_NET_RAW` only) | `make test-net` |
| systemd unit, sandbox and privileges (systemd container) | `make test-systemd` |
| Privilege check on real hardware | `tools/capcheck` on the target boards (§8) |

`make test-net` builds the test binaries in the dev container (`build/test-bins.sh`, for the Docker host's architecture), then `test/net/run.sh` creates a bridge network with fixed subnets (`172.31.250.0/24` and, where Docker can create it, `fd5e:5e:1::/64`; without IPv6 the NDP checks are skipped) standing in for an OT LAN, plus a second network (`172.31.251.0/24`). Simulated hosts are `busybox` containers with fixed MACs: one with a TCP listener on port 502 (`.10`), one without (`.11`, REFUSED), one that is swapped for a host with another MAC on request (`.12`), one that is stopped on request (`.13`), a "PLC" with a real Siemens OUI MAC for the OUI registrant lookup (`.14`), one that active discovery learns and that is then stopped (`.130`), and an unused address (`.99`). On the second network a host carries the same MAC as `.10`, so per-interface scoping is tested. Go tests ask for such changes by writing request files (`<action>@<id>.request`, holding the action's arguments) into a shared directory; `run.sh` performs them (swap, stop, connect or disconnect the second network, inject frames, add an administratively down interface to the runner's namespace from a helper container) and acknowledges with `<action>@<id>.done` (or `.failed`). To inject, a test writes a pcap file of frames from other MACs (DHCP, mDNS, DNS, LLDP, ARP, NDP) and `run.sh` sends it from a separate container with `test/net/inject`; LLDP goes to the broadcast address in these tests because Linux bridges do not forward the 802.1D reserved group addresses. The netlink tests check interface listing, neighbour entries appearing, changing MAC and expiring, link events when the second network is connected and removed, and the daemon finding hosts per interface from the neighbour table alone. The capture tests check decoding of every protocol, that the box's own frames and VLAN frames are not captured (priority-tagged ones are), that a down interface is refused until it comes up, promiscuous mode with only `CAP_NET_RAW`, the kernel drop counter on an overflowing ring, and the daemon naming hosts from captured frames. The active-discovery test runs operator scans through the daemon while a counter (an unfiltered `AF_PACKET` ring in the runner) records every frame the runner sends with kernel timestamps: no one-second window holds more probe starts than the global or a protocol budget (the kernel's own packets are measured and logged apart), excluded addresses and addresses outside the configured network see nothing, two probes to one target are at least the 5 s spacing apart, the measured scan's probe starts and duration are within 10% of `scan plan`, no TCP payload and no UDP leaves the box, TCP results are classified (OPEN, REFUSED, and UNREACHABLE for a known host that is stopped on request, `.130`), the prefix guard refuses, and the kill switch stops a running sweep within one second and survives a restart. The name-query test enables `active.names` and checks that the only UDP sent is one-shot mDNS to 224.0.0.251:5353 and DNS to the configured resolver's port 53, at most one query a second and `max_queries_per_pass` per pass. The runner container starts with only `CAP_NET_RAW` (plus the capabilities `setpriv` needs to drop privileges) and runs the tests as uid 65534 with ambient `CAP_NET_RAW`: stricter than the reference unit (root with a `CAP_NET_RAW` bounding set), so the daemon is known to need neither root nor another capability. It runs `capcheck` with and without the capability, then the `test/net` package built with the `nettest` tag, which reads the network layout from `LT_TEST_*` environment variables. A second pass disables ping sockets (`net.ipv4.ping_group_range` "1 0") and checks that ICMP falls back to a raw socket.

Docker's kernel is not the target kernel, so the board check in §8 stays mandatory.

### Testing strategy

Each layer catches what the one below cannot; all but the board check run in CI on every change (`make mod-verify check`, `make fuzz`, `make test-net`, `make test-systemd`, `make package`).

| Layer | What it proves | Where | Run by |
| --- | --- | --- | --- |
| Unit tests | Each package's behaviour, error paths and validation, against `platform/fake` and the simulated clock; table-driven | every `internal/` package | `make test`, `make check` |
| Golden tests | The reconstruction scenario (`DATA_MODEL.md` §10) gives the expected bindings, events and queries, also after compaction; the synthetic pcap fixture replayed through the whole daemon; the same queries through the CLI online and `--offline` | `test/golden`, `test/fixtures` (`TestFixturesCurrent` keeps the fixture in step with `make fixtures`) | `make test`, `make check` |
| Fuzzing | Decoders, the config loader and the OUI table never panic on hostile input | `Fuzz*` targets | `make fuzz` (`FUZZTIME` each, default 30 s) |
| Crash | SIGKILL of a daemon at varying moments during writes leaves a database that passes the integrity check | `test/crash` | `make test`, `make check` |
| Coverage gate | Coverage of `internal/` from every test package stays at or above 90% (`COVER_MIN`) | `build/coverage.sh` | `make coverage`, `make check` |
| Chaos | Seeded random streams over a small pool of MACs and addresses (ARP, neighbour entries, probe results, names, DHCP exchanges from two servers, collector failures, link changes, late observations, long silences, restarts) keep the correlator's invariants: conflicts flagged exactly when an address has two open holders, intervals ordered, one open lease per client, endpoint history consistent, the in-memory state equal to the database, a restart changing nothing. The whole daemon on the real clock with fake backends, under capture floods with malformed and truncated frames, collectors and links failing, erratic probe answers, and operators reading, describing, scanning, reloading valid and invalid configurations and setting the kill switch at once: a clean exit, no failed store write, a sound database, no 0.9 s window over a budget, nothing sent while the kill switch is set, no goroutine left | `internal/correlate/chaos_test.go` (`TestChaosCorrelator`), `internal/daemon/chaos_test.go` (`TestChaosDaemon`) | short runs in `make test`, `make check`; `make chaos` at length |
| Parallel | Six daemons replaying the same capture at once record identical histories (no shared state, no dependence on scheduling); one daemon serving many concurrent operators applies every description write (the last wins per host), runs one scan at a time, serialises reloads and the kill switch, and answers every reader online and offline | `internal/daemon/parallel_test.go` (`TestParallelReplays`, `TestParallelOperators`) | `make test`, `make check`, `make chaos` |
| Soak | Database size under its cap, retention and heap and CPU over 90 simulated days | `test/soak` (tag `soak`) | `make soak` (not in CI) |
| Network | Kernel backends, capture, probes and the budgets measured on the wire with only `CAP_NET_RAW` | `test/net` (tag `nettest`) | `make test-net` |
| systemd | The reference unit, install and upgrade commands, privileges, sandbox and reload | `test/systemd` | `make test-systemd` |
| Boards | The privilege check on the target kernels | `tools/capcheck`, `docs/TEST_PLAN.md` | manual (§8) |
