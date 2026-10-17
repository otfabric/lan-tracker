# OT Fabric LAN Tracker — Requirements

Status: agreed 5 October 2026, revised the same day after spec review; scope set on 16 October 2026 (ADR 0011); frozen to the implemented behaviour of v0.1.0 on 17 Oct 2026 (unchanged in v0.2.0). Every requirement below is implemented; what is not (deferred features, field validation, known gaps) is in `ROADMAP.md`. Requirement ids are stable and never reused (gaps in the numbering are intended, e.g. FR-AC-7, retired when IPv6 active probing was deferred); reference them in code reviews and tests.

## 1. Goal

LAN Tracker SHALL keep an evidence-based history of which MAC addresses, IP addresses, names and TCP endpoints are visible on each local OT network, so that operators can find equipment, troubleshoot connectivity and reconstruct incidents. It SHALL correlate passive capture, the kernel neighbour table and paced active checks into that history, keep the provenance of every fact, and keep different kinds of evidence apart: a frame sent by a MAC, an address claimed at a MAC, a DHCP server's assertion, an advertised name, an endpoint that answered, and a cached or last-known fact. It SHALL report where its visibility has gaps. Discovery methods SHALL be individually configurable and designed to minimise disruption to OT equipment.

It is a connectivity tracker with history. It SHALL NOT determine equipment models, operating systems, firmware, serial numbers or physical asset identity (ADR 0011); an operator's description is the only statement about what a device is.

### Primary acceptance criterion

For any interface and IP address, the CLI SHALL answer: which MAC or MACs claimed that address at a given past time, which other addresses and names that MAC had and how fresh each was, what DHCP configuration was offered or acknowledged to it, whether the configured TCP ports answered (open, refused, unanswered, locally unroutable or never tested), when each of these changed, which observations led to each conclusion, and where the collector could not see.

```bash
lan-tracker hosts find 192.168.1.200 --interface eth1 --at "2026-10-04 10:15"
lan-tracker hosts history 192.168.1.200 --since 7d
```

## 2. Functional requirements

### 2.1 Identity and correlation

| Id | Requirement |
| --- | --- |
| FR-ID-1 | Each host SHALL have an internal persistent UUID (`host_id`) as its record key. Within a network context a host SHALL correspond to exactly one MAC address, and the MAC SHALL be the only evidence that decides host identity. IP, hostname and vendor are attributes, never identity. |
| FR-ID-2 | All data SHALL be scoped to a network context, which in v1 is the interface, stable across subnet changes. The interface's own subnets SHALL be kept as separate history. The same IP or MAC on different interfaces SHALL be treated as different bindings and different hosts. |
| FR-ID-3 | A host MAY have multiple IPs and names over time. Each binding SHALL carry `first_seen`, `last_seen` and `ended_at` with the interval semantics in `DATA_MODEL.md` §3. A device with several NICs is several hosts. |
| FR-ID-4 | Every discovery mechanism SHALL emit normalised observations; a single correlation engine SHALL turn observations into host state and events. |
| FR-ID-5 | Correlation SHALL follow the rules in `DATA_MODEL.md` §5, including raising `DUPLICATE_IP_DETECTED` instead of merging two live MACs on one IP. |
| FR-ID-6 | Hosts SHALL never be merged or split, automatically or manually. There are no merge/split operations in v1. |
| FR-ID-7 | An observation without a MAC SHALL only attach to a host that holds its IP unambiguously; otherwise it is stored unbound and counted. It SHALL never create a host. |

### 2.2 Passive discovery

| Id | Requirement |
| --- | --- |
| FR-PA-1 | The daemon SHALL capture on configured interfaces using `AF_PACKET` with a kernel BPF filter limited to discovery protocols. |
| FR-PA-2 | It SHALL decode ARP (including gratuitous ARP), IPv4 source addresses, IPv6 source addresses and NDP, DHCP, mDNS, DNS responses and LLDP. Each protocol SHALL be individually enabled/disabled; IPv6 SHALL be independently configurable. |
| FR-PA-3 | DHCPv4 decoding SHALL read the whole message into one observation: the BOOTP header (op, xid, secs, flags, ciaddr, yiaddr, siaddr, giaddr, and chaddr when the hardware type and length are Ethernet's) and options 1, 3, 6, 12, 15, 50, 51, 53, 54, 55, 58, 59, 60, 61, 81 (FQDN with its flags), 82 (circuit and remote ID), 119 (search list), 121 and 249 (classless routes, 249 kept apart and never used for the gateway). Split options SHALL be joined (RFC 3396) and the `file` and `sname` fields read when option 52 overloads them. Every list, string, route count and compression-pointer walk SHALL be bounded; a malformed option SHALL be named and the rest kept, and a truncated options field flagged, so it never reads as a complete configuration. An option a message leaves out is unknown, not empty. Direction SHALL come from the BOOTP op agreeing with the message type, not from the ports, so relay traffic on UDP 67 to 67 decodes too. A client message (`passive_dhcp`) speaks for its sender, with its ciaddr claim and hostname, only when the frame came straight from the client (Ethernet source is chaddr, no giaddr); a server reply (`passive_dhcp_server`) is evidence about its sender, which may be a relay. The hostname, client identifier, FQDN, parameter request list and vendor class are evidence, never identity and never classified. A server's ACK SHALL NOT create a host, bind an address or refresh its client's presence; a message without an Ethernet chaddr is kept as its observation only (`DATA_MODEL.md` §2, 17 Oct 2026) |
| FR-PA-4 | mDNS decoding SHALL extract A, AAAA, PTR, SRV and TXT records, including service types (e.g. `_http._tcp`, `_modbus._tcp`). |
| FR-PA-5 | Promiscuous mode SHALL be configurable per interface and default to off; it SHALL be set with `PACKET_ADD_MEMBERSHIP` (no `CAP_NET_ADMIN`). |
| FR-PA-6 | Passive capture SHALL never transmit frames. |
| FR-PA-7 | The daemon SHALL monitor DHCPv4 servers per interface from their replies (OFFER, ACK, NAK), passively: the server identifier (option 54) kept apart from the sender MAC and IP (the server, or the relay: `giaddr`), and the router, DNS servers and subnet mask they advertise. It SHALL emit `DHCP_SERVER_DISCOVERED` for a server identity new on the interface; `DHCP_SERVER_UNEXPECTED` for one outside the interface's allowlist (`interfaces[].dhcp.servers`), where a reply without a valid identifier is never allowed and no allowlist means no verdict is claimed (`unchecked`); and `DHCP_SERVER_MAC_CHANGED` when a known identity replies from another sender MAC. The server identifier SHALL NOT be assigned to a relay's MAC. Several servers, or several offers in one exchange, are reported as observed, never as a rogue-server verdict. `dhcp_servers` SHALL show the last offered or assigned configuration without events: a server may answer clients differently, so configurations are compared per client (FR-PA-9), never across one server's clients. Routine lease acknowledgements SHALL NOT produce events. The allowlist detects unexpected advertised identities; it does not authenticate servers. (`DATA_MODEL.md` §5.6) |
| FR-PA-8 | The daemon SHALL keep each DHCP transaction it sees as an exchange, keyed by interface, xid and chaddr: its messages in order with retransmissions counted, every server's offer, the server the client selected, the requested address, the outcome (`ack`, `inform_ack`, `nak`, `offered`, `decline`, `release`, or `incomplete` when it is left unanswered for a minute) and whether both sides were seen (`complete` or `partial`). Pending exchanges SHALL be bounded (1,024 open per interface, the oldest closed first). An OFFER is a proposal and creates no lease; an orphan REQUEST or ACK is kept and marked partial (`dhcp exchanges`, `/v1/dhcp/exchanges`; 17 Oct 2026) |
| FR-PA-9 | The daemon SHALL keep a per-client lease history from server ACKs: one row per interface, client, address and acknowledged configuration, with server identifier, sender MAC and IP, relay (giaddr and option 82), first and last ACK and their count, lease time (or infinite or unknown), T1 and T2 as advertised or derived (marked so), the expiry according to the last ACK seen, whether the exchange was seen in part, and an immutable snapshot of mask and prefix, routers, DNS servers, domain, search list, hostname, FQDN, classless routes and the gateway they imply (the default route of option 121 when present, as RFC 3442 requires, else the first router). A renewal with the same address, server and configuration SHALL extend the row; a new address SHALL emit `DHCP_LEASE_ASSIGNED` and a new configuration `DHCP_CLIENT_CONFIG_CHANGED`, each superseding the old row. Configurations SHALL be compared per client, on the options both ACKs carried. NAK, DECLINE and RELEASE SHALL be events (`DHCP_LEASE_NAK`, `DHCP_LEASE_DECLINED`, `DHCP_LEASE_RELEASED`); a RELEASE or DECLINE of the open lease's address ends it. The ACK to an INFORM is configuration only, never an allocation. A relayed ACK describes a client that may be on another link: it SHALL NOT establish local attachment or presence. The expiry is never proof that an address was released, and a lease never makes its client present (`dhcp leases`, `hosts show`, `/v1/dhcp/leases`; 17 Oct 2026) |
| FR-PA-10 | DHCP history SHALL admit what the collector could not see: on a switched port unicast replies and renewals are usually missed, so leases and exchanges seen in part are marked partial, silence never proves there is no server or no lease, and a missing reply is never filled in. Compaction SHALL keep open leases however old and prune ended leases with the events' retention and exchanges with the roll-ups' (17 Oct 2026) |

### 2.3 Kernel neighbour table

| Id | Requirement |
| --- | --- |
| FR-NL-1 | The daemon SHALL dump the neighbour table at startup and then subscribe to rtnetlink `RTM_NEWNEIGH` and `RTM_DELNEIGH`. It SHALL take a fresh dump after notification loss (`ENOBUFS`) and every `neighbor.resync_interval` (default 10 min). It SHALL NOT poll or shell out to `ip neigh`. |
| FR-NL-2 | Neighbour observations SHALL use source `kernel_neighbor` and record the NUD state (REACHABLE, STALE, DELAY, PROBE, FAILED) as `neighbor_state`. `STALE` SHALL NOT be interpreted as offline. |
| FR-NL-3 | The interface manager SHALL track link up/down and address/subnet changes via netlink and emit `INTERFACE_UP`, `INTERFACE_DOWN` and `SUBNET_CHANGED`. |

### 2.4 Active discovery

| Id | Requirement |
| --- | --- |
| FR-AC-1 | Active discovery SHALL be disabled by default and enabled per interface. |
| FR-AC-2 | Active probes SHALL only target explicitly configured networks. Discovered subnets SHALL never be scanned automatically. |
| FR-AC-3 | ARP sweeping SHALL be implemented natively in Go and be the primary IPv4 discovery probe. |
| FR-AC-4 | ICMP echo SHALL be supported, using unprivileged ping sockets where `net.ipv4.ping_group_range` allows, else raw sockets under `CAP_NET_RAW`. Disabled by default. |
| FR-AC-5 | TCP checks SHALL use a full `connect()`, bound to the probed interface with `SO_BINDTODEVICE`, with a per-target timeout (default 750 ms) and an immediate bounded close, sending no payload. Each result SHALL be classified exactly: OPEN (a connection completed), REFUSED (actively rejected during connect), TIMEOUT (nothing completed in time), UNREACHABLE (the stack reported a network or host error) or LOCAL_ERROR (the probe could not be made locally: permission, no usable source address, no direct route, a missing interface). Only OPEN and REFUSED are answers; none of the others is evidence against the remote device. TCP checks SHALL never wait for an ICMP answer (`DATA_MODEL.md` §5.5) |
| FR-AC-8 | Operators SHALL be able to trigger a scan on demand (`scan run`), which is subject to the same safety controls. |
| FR-AC-9 | Named scan profiles (e.g. `modbus`) SHALL be definable in config. |
| FR-AC-12 | Name queries SHALL ask only about names and addresses already known, or explicitly configured DNS-SD service types, through the shared budget: mDNS on the configured interfaces for known `.local` names, PTR for known addresses and A/AAAA/CNAME for known DNS names through explicitly configured resolvers. Answers are name records, never MAC ownership; a failed lookup changes nothing (`DATA_MODEL.md` §5.4) |
| FR-AC-10 | A dry run (`scan plan`) SHALL report target count, excludes applied, probes and ports, rates, estimated probe starts and duration, and whether the scan would be refused and why, without transmitting. `scan run` SHALL refuse anything `scan plan` refuses. |

### 2.5 Names, OUI registrants and descriptions

| Id | Requirement |
| --- | --- |
| FR-NM-1 | Every hostname SHALL be stored with its type/provenance (mDNS, DNS, DNS PTR, DHCP, LLDP) and all names SHALL be kept, not overwritten. |
| FR-NM-2 | A `preferred_name` SHALL be derived from configurable precedence (`identity.hostname_preference`). |
| FR-VD-1 | The MAC prefix's IEEE registrant (MA-L, MA-M and MA-S, embedded in the binary, with an optional override file) SHALL be shown as an OUI registrant hint, never as the equipment's manufacturer. Locally administered MACs SHALL be flagged |
| FR-MD-1 | An operator SHALL be able to set, change and remove a free-text description on a host (`lan-tracker hosts set <host> --description`, `hosts unset <host> --description`) for context the network cannot tell, e.g. "Solar panel rooftop". The description SHALL be one line of at most 200 characters, changed only through the running daemon, and each change SHALL be recorded as `HOST_DESCRIBED` with the old and new text and the calling user. It SHALL show in `hosts show` and `hosts list` and be searchable (`hosts list --description`) (`DATA_MODEL.md` §5.8). |

### 2.6 Presence

| Id | Requirement |
| --- | --- |
| FR-PR-1 | Host presence SHALL be one of ACTIVE, RECENT, STALE, MISSING, never a boolean online flag. |
| FR-PR-2 | Thresholds SHALL be configurable; defaults ACTIVE < 5 min, RECENT < 30 min, STALE < 24 h, MISSING beyond. |

### 2.7 Events

| Id | Requirement |
| --- | --- |
| FR-EV-1 | State transitions SHALL produce events from the catalogue in `DATA_MODEL.md` §7. Repeated identical observations SHALL NOT produce events. |
| FR-EV-2 | Each event SHALL reference its host, context, old and new values and its cause, and SHALL carry a self-contained snapshot of the causing evidence (`DATA_MODEL.md` §7) so that provenance survives compaction of raw observations. |
| FR-EV-3 | Events SHALL be written to the `events` table and to journald with structured fields. |

### 2.8 Storage and retention

| Id | Requirement |
| --- | --- |
| FR-ST-1 | Data SHALL be stored in SQLite (WAL) at the configured path, default `/data/lan-tracker/hosts.db`, with schema migrations embedded and applied at startup. |
| FR-ST-2 | Current state and event history SHALL be stored separately; raw observations SHALL be compacted. |
| FR-ST-3 | Retention SHALL be configurable per site: raw observations (default 7 days), hourly roll-ups (default 90 days), events (default 2 years) and a maximum database size (default 200 MB). When over the size cap, the oldest raw observations are rolled up first, then the oldest roll-ups (with DHCP exchanges) pruned, then the history the events explain and the oldest events; current state and address bindings are never pruned. |
| FR-ST-4 | At startup the daemon SHALL run an integrity check; a corrupt database SHALL be quarantined and recreated, with an event and a log entry. |

### 2.9 API, CLI, logging, metrics, configuration

| Id | Requirement |
| --- | --- |
| FR-API-1 | The daemon SHALL serve a local REST API over a Unix socket (`/run/lan-tracker/api.sock`, owned by the daemon's user, root under the reference unit, mode 0660). No additional authentication in v1. An optional `127.0.0.1` TCP listener MAY be enabled. |
| FR-CLI-1 | The CLI SHALL implement the contract in `CLI.md`, in the same binary as the daemon. |
| FR-CLI-2 | The CLI SHALL work through the daemon socket by default and SHALL support `--offline` read-only access to the database following the contract in `CLI.md` §1. |
| FR-LOG-1 | Logging SHALL use journald with structured fields; text or JSON on stderr when run from a terminal or without journald. Observations SHALL NOT be logged; state transitions SHALL. |
| FR-STAT-1 | `daemon status` and the API SHALL report, per interface and collector (capture, neighbour, interface monitor, each probe engine, replay), whether it is running, disabled, unsupported or failed, with the error (e.g. permission denied on the `AF_PACKET` socket). A configured collector that is not running SHALL make the status degraded. |
| FR-MET-1 | The daemon SHALL expose Prometheus metrics on the local listener with prefix `lan_tracker_` and no per-host labels. |
| FR-CFG-1 | Configuration precedence SHALL be CLI flags > `LAN_TRACKER_*` environment > config file > compiled defaults. |
| FR-CFG-2 | The configuration SHALL be schema version 1. Another version, unknown keys and invalid values SHALL fail validation. `config validate` SHALL be usable before rollout. |
| FR-CFG-3 | SIGHUP and `lan-tracker config reload` SHALL reload probes (each interface's `active` settings included), DHCP allowlists, intervals, budgets, thresholds and log level, all or nothing: a file that does not validate changes nothing. Adding, removing or renaming interfaces and passive capture, storage, API, metrics and log-format changes require a restart and SHALL be reported as not applied. |
| FR-CFG-4 | A kill switch (`LAN_TRACKER_ACTIVE_DISABLED=1`, API or `active disable`) SHALL stop all active probes without a restart. A kill switch set through the API or CLI SHALL persist across restarts until cleared, and SHALL be audited with `ACTIVE_DISABLED`/`ACTIVE_ENABLED` events. |

### 2.10 Platform, development and replay

| Id | Requirement |
| --- | --- |
| FR-PLAT-1 | LAN Tracker SHALL run on Linux only (kernel 5.10+), delivered as `linux/amd64` and `linux/arm64` binaries under systemd. |
| FR-PLAT-2 | The code base SHALL target Linux only, without build tags or stubs for other OSes. Kernel access (AF_PACKET, rtnetlink, raw and ping sockets, `SO_BINDTODEVICE`) SHALL sit behind small interfaces in `internal/platform`, and sd_notify and journald in `internal/service`, so the rest is testable with fakes. Every build, lint and test SHALL run in a Linux dev container via `make`, on Linux and macOS development machines and in CI. |
| FR-PLAT-3 | Network integration tests SHALL run in Docker containers on Docker networks with simulated hosts, as an unprivileged user with only `CAP_NET_RAW` (`make test-net`), and the systemd unit SHALL be tested in a systemd container (`make test-systemd`). |
| FR-RP-1 | A replay collector SHALL feed an interface's observations from a pcap/pcapng file (decoded by the same decoders as live capture) or from a JSONL observation stream. Replayed observations SHALL keep their recorded `Source` and timestamp. |
| FR-RP-2 | Replay SHALL drive a simulated clock from the observation timestamps (as fast as possible) or replay in real time scaled by a speed factor, so presence and expiry behave as they did at the site. The golden scenarios SHALL run through the same mechanism. |
| FR-RP-3 | Replay is configured per interface and is mutually exclusive with live passive and active discovery; the interface's prefixes come from config. All interfaces of a daemon are replay interfaces or none is, since replay runs the daemon on recorded time; several replay files are merged in time order. Replay works in the dev container. |

## 3. Non-functional requirements

| Id | Requirement |
| --- | --- |
| NFR-SAFE-1 | Active discovery rates SHALL be application-initiated probe starts per second (ADR 0011): one ARP request, ICMP echo, DNS or mDNS query send, or TCP connect initiation is one start. Defaults: 5 starts/s in all, with no accumulated burst, shared by every engine, interface and operator scan; at most 4 jobs in flight; a 30 s startup delay plus jitter; ±10% interval jitter; randomised target order; ARP sweeps and TCP checks every 15 min. Kernel packets (ARP resolution, handshake ACKs, retransmissions, the close) SHALL be bounded by timeouts and concurrency and measured (`make test-net`), and no total packet rate is promised |
| NFR-SAFE-2 | Scan prefixes wider than `/24` IPv4 SHALL be refused at validation unless `allow_wide_scan: true`, and one interface's sweep SHALL cover at most `active.max_sweep_targets` addresses (default 1,024) after excludes |
| NFR-SAFE-3 | A host returning TIMEOUT three times in a row SHALL be probed at 4× its interval. |
| NFR-SAFE-4 | Excludes (IPs and CIDRs) SHALL be checked before every transmitted probe. |
| NFR-SAFE-5 | Each probe protocol SHALL have its own start rate beneath the global one (defaults: ARP 5, ICMP 1, name queries 1 starts/s; TCP 1 connect/s). TCP SHALL additionally be capped at 2 concurrent connects per interface and 1 per target address, and any two probes to the same target SHALL be at least 5 s apart. A TCP connect is one start of the global budget |
| NFR-PERF-1 | Budget on a RevPi Connect class device: < 5% average CPU, < 50 MB RSS, database < 200 MB after 90 days on a 50-host LAN. `make soak` checks the database budget, retention and heap growth on a simulated 90-day 50-host LAN; measurement on the boards is open (`ROADMAP.md`). |
| NFR-PERF-2 | SQLite commits SHALL be batched (target every 5 s) to limit SD-card wear; `synchronous=NORMAL` with WAL. |
| NFR-SEC-1 | The service SHALL hold only `CAP_NET_RAW` (it runs as root, with that capability alone in its bounding set) under the hardened unit in `ARCHITECTURE.md` §8; target `systemd-analyze security` exposure ≤ 2.5 on each target systemd (247, 252, 257). This SHALL be verified on every change in Docker (`make test-net`, `make test-systemd`); the board audit is open (`ROADMAP.md`), and any need for another capability stops the rollout for a decision. |
| NFR-PORT-1 | Deliverables SHALL be static `CGO_ENABLED=0` binaries for `linux/amd64` and `linux/arm64` with SHA-256 checksums, shipped as one tarball per target with the reference deploy files. No `.deb` or other packages. Minimum kernel 5.10. |
| NFR-PORT-2 | CI SHALL, on every change, run the same make targets as developers in the dev container: `check` (format, tidy, vet, lint, vulnerabilities, tests with race and coverage of `internal/` of at least 90%), `fuzz`, the Docker network and systemd suites, and the release build of both targets. |
| NFR-REL-1 | The daemon SHALL use `Type=notify` with READY and WATCHDOG, shut down gracefully on SIGTERM, and tolerate interfaces appearing and disappearing at runtime. |
| NFR-REL-2 | Timestamps SHALL survive clock jumps on devices without an RTC: events written before NTP sync SHALL be marked (`adjtimex` `STA_UNSYNC`). |

## 4. Out of scope

Device identification of any kind (ADR 0011): payload probes (Modbus, HTTP, TLS, SNMP, SSH, Telnet, FTP banners), protocol-specific UDP probes (NTP, EtherNet/IP ListIdentity), passive device, OS or vendor classification and confidence ranking, fingerprint databases, and running OT Fabric's protocol tools from the daemon. SYN scanning; generic UDP port scanning; register scanning; IPv6 sweeping; a DHCP client, server or leasequery; DHCPv6; running on or building for any OS other than Linux; VLAN-tagged networks; a web UI; alerting; automatic address, route, DNS or firewall repair; continuous packet recording; any consumption of the data off the box (fleet aggregation, remote API access).

## 5. Recorded decisions

| Question | Decision |
| --- | --- |
| Target hardware | amd64 and arm64 Linux edge devices |
| Platforms | Linux only, `linux/amd64` and `linux/arm64`; the code base has no stubs for other OSes. Every build and test runs in a Linux dev container via `make`, so Linux and macOS machines work for development (decided 5 Oct 2026) |
| Minimum kernel | Linux 5.10 |
| Go toolchain | Go 1.26+, the floor set by `x/net`, `x/sys` and `modernc.org/sqlite` (decided at the start of development) |
| Network tests | Docker bridge networks with simulated hosts; runner as uid 65534 with ambient `CAP_NET_RAW` (`make test-net`) |
| Neighbour source | `kernel_neighbor`, NUD state in `neighbor_state` |
| API authentication | Unix socket permissions only; no UI in this project |
| Retention | Configurable per site, with defaults and a size cap |
| Host data leaving the box | Out of scope; later phase; local API is the integration point |
| VLAN tagging | Out of scope; no VLAN sites today |
| Promiscuous mode | Configurable per interface, default off; useful only on mirror/SPAN ports or hubs |
| Deliverables | Static binaries only, no packages |
| Naming | OT Fabric LAN Tracker: repository `otfabric/lan-tracker`, module `github.com/otfabric/lan-tracker`; `lan-tracker` for binary, unit and paths (`/etc/lan-tracker`, `/data/lan-tracker`, `/run/lan-tracker`), `LAN_TRACKER_*` environment and `lan_tracker_*` metrics. There is no `lan-tracker` user (6 Oct 2026, see Service identity) |
| Packet capture | Pure-Go `AF_PACKET` (no libpcap, no cgo) |
| Host identity | Within an interface, host = MAC. No merge/split. Multi-NIC devices are several hosts (decided 5 Oct 2026) |
| Network context | The interface only; subnet changes are history (`context_prefixes`), not new contexts |
| Interval semantics | `ended_at` NULL = open; `last_seen` = last evidence; half-open intervals |
| Event provenance | Evidence snapshot copied into every event; `observation_id` is informational |
| Kill switch | Persists across restarts when set via API/CLI; audited with events |
| Extra OT controls | `scan plan` dry run and per-protocol / per-host TCP budgets in v1 |
| Extra CLI | `observations list`, `hosts evidence`, `active enable` and `active disable` in v1 |
| Probe targets | ARP sweeps every address of the configured networks; ICMP and TCP probe only known hosts (open bindings inside those networks); `scan plan` estimates use the known-host count (decided 6 Oct 2026) |
| Scan records | Only operator-triggered `scan run` writes `scans` rows and `SCAN_STARTED`/`SCAN_COMPLETED`; scheduled probe runs show in `daemon status` and metrics (decided 6 Oct 2026) |
| IPv4-first | v1 active discovery is IPv4 with ARP as the primary mechanism; NDP solicitation and other IPv6 active probing are deferred (decided 6 Oct 2026) |
| Probe libraries | Probe frames built with gopacket/layers, raw frame I/O behind a generic frame connection in `internal/platform` bound to one interface; no mdlayher/arp or mdlayher/ndp (decided 6 Oct 2026) |
| Probe presence | A MAC-less probe result is presence evidence only when the host answered: TCP OPEN or REFUSED, an ICMP echo reply. TCP TIMEOUT and UNREACHABLE record the service result but neither refresh presence nor count against the host (`DATA_MODEL.md` §5.1, 6 Oct 2026) |
| Budget enforcement | Budgets count probe starts, not packets (ADR 0011, NFR-SAFE-1, 16 Oct 2026). They are paced and enforced over a sliding window of 1.02 s on actual send times (a late send is re-booked when it leaves), so no one-second window on the wire holds more starts than a budget allows (6 Oct 2026). The kernel's own packets are counted apart (`make test-net`, `TEST_PLAN.md` D2) |
| TCP close and packets | The bare TCP connect probe closes with a RST (`SO_LINGER` 0) and caps SYN retransmissions at 2 (`TCP_SYNCNT`), so it sends 3 packets when OPEN, 1 when REFUSED and at most 3 on TIMEOUT (`ARCHITECTURE.md` §5, 6 Oct 2026). A connect is one start of the budget (ADR 0011, 16 Oct 2026) |
| Back-off scope | NFR-SAFE-3 applies per interface, address, probe type and port (TCP), to every known-host probe that gets no answer (ICMP without reply, TCP TIMEOUT or UNREACHABLE); a miss of one probe never slows another. An answer to the probe ends its back-off; fresh positive evidence for the address (its binding confirmed after the last miss) eases it: the next probe goes at the normal interval, and one more miss restores the back-off. ARP sweeps are exempt, so new devices are still found (6 Oct 2026) |
| Sweep size | The limit is a target count, not a prefix: an interface's networks less excludes may hold at most `active.max_sweep_targets` addresses (default 1,024). Raising it is an expert override: it needs `allow_wide_scan`, and the global and ARP rates and the concurrency cap may not exceed their defaults; `config validate` shows sweep sizes and durations, and an operator sweep beyond 1,024 addresses needs `--allow-wide` after its `scan plan` preview. Sweeps stream their addresses (validation keeps the value at most a /8) (6 Oct 2026; default 1,024 since 16 Oct 2026) |
| Probe metric ports | `lan_tracker_probe_total`'s `port` label is a configured TCP port, empty for the other protocols; ports given only to `scan run` count as `other` (rule 9, 6 Oct 2026) |
| Clock sync detection | NFR-REL-2 reads the kernel's NTP status with a read-only `adjtimex` (`STA_UNSYNC`), independent of the NTP client (chrony on the deployed devices; no chronyc or chrony files) and without `CAP_SYS_TIME`. Each event records `synced`, `unsynced` or `unknown`; a state that cannot be read is `unknown`, never assumed synchronised. The unit allows `adjtimex` alone of the `@clock` calls, with `ProtectClock=` off (exposure 2.0 on Debian 11, 12 and 13) (6 Oct 2026) |
| Service identity | The daemon runs as root, not as a `lan-tracker` user: no `sysusers.d` or `tmpfiles.d`, so installing needs only the binary, the config, `/data/lan-tracker` and the unit (a failed `sysusers.d` install on a Debian 11 board prompted it). `CapabilityBoundingSet=CAP_NET_RAW` leaves root that capability alone and the sandbox is unchanged; exposure 2.3. The socket and database are root's, so the CLI runs with `sudo`. The code still needs only `CAP_NET_RAW` as any user (`make test-net` runs it as uid 65534) (6 Oct 2026) |
| Host descriptions | Operators describe hosts with free text (requested 7 Oct 2026): `hosts set <host> --description` and `hosts unset <host> --description`, a form that leaves room for more fields later; one line of at most 200 characters on a host (a MAC on an interface: a phone that changes its MAC gets a new host, without the description); every change a `HOST_DESCRIBED` event with the caller, through the daemon so the single writer holds (7 Oct 2026) |
| DHCP server monitoring | Passive DHCPv4 server monitoring in v1, on the existing capture (requested 6 Oct 2026): server identities matched by option 54 per interface against an optional allowlist (no allowlist: servers are reported as `unchecked`, never as authorised; empty list: no server expected); sender MAC changes kept and flagged; discovery, unexpected-server and MAC-change events. Configurations are compared per client, not per server (DHCP depth below). Receive only: synthetic DHCP requests would change server state and need their own specification; DHCPv6 needs its own decoder and identity model; both deferred. On a switched port unicast replies are not seen, so silence never proves there is no server (6 Oct 2026) |
| Config reload | `lan-tracker config reload` (requested 6 Oct 2026) makes the daemon re-read its file through `POST /v1/config/reload` and prints which keys took effect and which need a restart; SIGHUP (`systemctl reload`) does the same. Each interface's `active` settings became reloadable, so active discovery is switched on or off per site without a restart; the set of interfaces and passive capture (whose protocols are compiled into the kernel filter) stay restart-only (6 Oct 2026) |
| Target systems | Debian 11 bullseye (systemd 247), 12 bookworm (252) and 13 trixie (257); `make test-systemd` checks the unit on each (6 Oct 2026) |
| Releases | Cut from `main` only, by the manual `release` workflow with a patch, minor or major bump from the highest `vX.Y.Z` tag (the first release is v0.1.0). After the same gates as CI and a reproducibility check, it tags the tested commit and publishes a GitHub Release with one tarball per target (binary, `capcheck`, reference deploy files, inner `SHA256SUMS`) and `SHA256SUMS` for the tarballs (6 Oct 2026) |
| Scan estimate | `scan plan` gives two durations, labelled the estimated typical duration (known hosts answer at once; only the ARP sweep waits out its reply timeout) and the estimated no-response duration (every phase waits out its reply timeout), and lists the assumptions they rest on (6 Oct 2026) |
| Corrupt database | Quarantined as `<path>.corrupt-<UTC time>`, a new one created, logged as an error and recorded as the new database's first event, `DATABASE_RECREATED` (decided 6 Oct 2026) |
| Proxy ARP vs duplicate IP | Only ARP answers count. Two or more contested addresses, or more than `proxy_arp_threshold` addresses, flag proxy ARP; a single contested address is a duplicate IP. Gratuitous ARP never counts as proxy behaviour but does raise duplicate-IP conflicts (`DATA_MODEL.md` §5.2, decided 6 Oct 2026) |
| Name expiry | Names are last-known attributes, kept as name records: several names per type coexist, and a record never ends because time passed, whether the host stays visible or goes MISSING. Records end only on an mDNS goodbye, a cache flush or the address passing to another MAC; a TTL running out marks a record expired when read, and `name_expiry` only marks a name stale when read and never clears `preferred_name` (`DATA_MODEL.md` §5.4, 6 Oct 2026; name records 16 Oct 2026) |
| Tracker scope | A connectivity tracker with history, without device identification: no payload or protocol-specific UDP probes, no passive classifiers, no device types or manufacturers; the OUI is a registrant hint and the operator's description the only statement about what a device is. Active rates are probe starts per second (ADR 0011, 16 Oct 2026) |
| DHCP depth | Full passive DHCPv4 decoding, exchanges and a per-client lease history (FR-PA-3, FR-PA-7 to FR-PA-10). A lease is the server's assertion: it creates no host, binds no address and refreshes no presence; a relayed ACK may describe a client on another link. Configurations are compared per client on the options both ACKs carried, never across a server's clients. Receive only; no DHCP client, server, leasequery or injection; DHCPv6 stays deferred (17 Oct 2026) |
| Licence and first release | MIT (`LICENSE`, OT Fabric's default), shipped in the release tarballs; the first release is v0.1.0, with notes from `RELEASE.md` (17 Oct 2026) |
| Collection gaps | `OBSERVATION_GAP` records, per interface, a time nothing was observed: a passive collector (capture, neighbour) leaving `running` until it runs again or the daemon stops, and the daemon's own downtime from its last heartbeat (`runtime_state` `observed_until`, every 15 s) to the next start when at least 1 minute. Disabled or unsupported collectors and replay record none (`DATA_MODEL.md` §7, 17 Oct 2026) |
| Fresh attribution | A MAC-less observation (a TCP or ICMP result, an address record) is attributed to the sole holder of its address only when that binding was confirmed within `identity.address_expiry`; otherwise it stays unattributed (`DATA_MODEL.md` §5.1, 17 Oct 2026) |
