# Migration from LAN Sentinel to OT Fabric LAN Tracker

This page is the change log of the migration: where the code came from, what was removed, which evidence semantics changed, how legacy databases and configuration are handled, and which gates passed. The brief the work followed is [`migration-brief.md`](migration-brief.md) (16 Oct 2026); the decision is [ADR 0012](adr/0012-connectivity-tracker-scope.md). Upgrade and rollback steps for operators are in [`deploy/README.md`](../deploy/README.md) (Upgrading from LAN Sentinel).

## Lineage and baseline

| Item | Value |
| --- | --- |
| Source repository | `boeboe/lan-sentinel` |
| Baseline commit | `a437a228c8e58fbf0bb10de509381ea2336448f2` (8 Oct 2026, "identify run takes several probes and SNI; CLI help and identification display") |
| History | Imported in full into `otfabric/lan-tracker` (branch `migrate/lan-tracker`); tags `v0.0.1`–`v0.0.8` kept. The brief names v0.0.9 as the latest published release; the local clone has tags up to v0.0.8 only, so v0.0.9 could not be verified here |
| Uncommitted work in the source checkout | Not migrated (maintainer's choice, 16 Oct 2026). It held CDP, NetBIOS, SSDP and WSD decoders and an `identify/discovery.go`, outside the narrowed scope, plus chaos/stress test targets |
| Schema at baseline | Migrations 0001–0009, unchanged |
| Licence | No `LICENSE` file at the baseline. The maintainer chose MIT, OT Fabric's default (17 Oct 2026): `LICENSE`, shipped in the release tarballs |
| Repository transfer | Not performed. The brief prefers transferring and renaming `boeboe/lan-sentinel`; this branch instead carries the full history into `otfabric/lan-tracker`. Either way is the maintainer's action |

### Baseline gates (16 Oct 2026, at `a437a22` with the lan-tracker README merged)

| Gate | Result |
| --- | --- |
| `make mod-verify check` | Pass. Coverage of `internal/` 91.0%. `govulncheck`: no vulnerabilities in called code (5 in required modules, not called) |
| `make test-net` | Pass |
| `make test-systemd` | Pass on Debian 11 (systemd 247), 12 (252) and 13 (257), exposure 2.3 |
| Hardware and field checks | Not run (no boards available to the agent) |

The brief's findings were rechecked against the baseline; they all reproduce by inspection.

## Changes

The steps were developed as separate commits on `migrate/lan-tracker` and merged as one migration commit; the lineage merge before it keeps LAN Sentinel's history.

### 1. Rename

Module `github.com/otfabric/lan-tracker`; binary, unit, paths, `LAN_TRACKER_*` environment and `lan_tracker_*` metrics. Migrations 0001–0009 keep their original comments.

### 2. Identification removed, configuration schema 2

Removed from the runtime (ADR 0012):

| Removed | Was |
| --- | --- |
| `internal/probe/idprobe` | Modbus FC 43/14, HTTP GET, TLS handshake, SNMPv2c, SSH banner, Telnet, FTP probes |
| `internal/probe/udp` | NTP and EtherNet/IP ListIdentity probes |
| `internal/identify/plugins.go`, `internal/correlate/identify.go` | Passive device type, OS and model rules, confidences and stickiness |
| `identify run`, `POST /v1/hosts/{id}/identify` | The operator retry of an identification probe |
| `hosts list --device`, `hosts.device_type`/`manufacturer` in the read model | The chosen device type and the OUI-as-manufacturer claim |
| Sources `udp_probe`, `identify_probe`; metrics `lan_tracker_identify_*` | Identification observations and counters |

Kept, changed in meaning:

- The OUI lookup is shown as `oui_registrant` (JSON) and "OUI REGISTRANT" (tables): the MAC prefix's IEEE registrant, a hint about the network interface, never the equipment's maker. No identification row is written for it.
- Proxy ARP is a diagnostic flag, `hosts.proxy_arp` (migration 0010, backfilled from LAN Sentinel's `proxy_arp` identifications), announced by `PROXY_ARP_DETECTED` instead of `VENDOR_IDENTIFIED`. The safeguard itself is unchanged.
- A MAC's addresses are independent: a new address never closes another (the IPv4 replacement after `identity.address_overlap` is gone), so `IP_CHANGED` is no longer emitted; an address not confirmed for `identity.address_expiry` while its host is seen is closed as an inference, with that reason in the event. A takeover by another MAC while the holder is not live still closes the binding. `hosts find --at` reports `next_address` (the host's next new address after a binding ended, not proof it moved) instead of `moved_to`.
- The same MAC on a second interface emits `MAC_ALSO_OBSERVED` instead of `MAC_MOVED`: two sightings do not establish movement.
- Rates are probe starts per second: a TCP connect costs one start, not three packet tokens. Defaults: 5 starts/s in all, ARP 5, ICMP 1, TCP 1 connect/s (2 per interface, 1 per host), 4 jobs in flight, 5 s between probes to one target, sweeps and TCP checks every 15 min, TCP connect timeout 750 ms, at most 1,024 addresses per interface sweep. Suggested TCP ports 80, 443, 8080, 502 are the compiled targets, off until `active.tcp.enabled`.

Legacy data and configuration:

- LAN Sentinel's event types stay valid in the `events` CHECK (migration 0010 rebuilds the table once, adding the tracker's types) and keep their original wording; `identifications` and `identify_attempts` rows stay readable (`legacy_identifications` in `hosts show -o json`, a legacy block in `hosts evidence`). Nothing writes them.
- Schema 2 (`version: 2`) refuses version 1, removed keys and any `LAN_SENTINEL_*` variable (ignoring `LAN_SENTINEL_ACTIVE_DISABLED` silently would leave probing on). `lan-tracker config convert FILE` prints the converted file on stdout and a report on stderr; it writes nothing, sends nothing, resets packet budgets to the start-rate defaults rather than copying numbers, and leaves every interface's active discovery off.
- `deploy/config.active-example.yaml` is the reviewed ARP + TCP profile; `TestDeployConfigs` loads it with the real loader.

### 3. The connectivity model

Evidence semantics (`DATA_MODEL.md` §2):

- Observations are classified by `Observation.Evidence()`: a frame from the MAC, an address confirmed at it (ARP answers, REACHABLE neighbours), an endpoint that answered (TCP OPEN or REFUSED, ICMP echo reply), cached or asserted (a STALE neighbour entry, a DHCP server's lease ACK), or none (DNS answers, TCP timeouts, local probe failures). Only the first three refresh presence. Hosts keep the three kinds' times apart (`last_frame_seen`, `last_address_confirmed`, `last_endpoint_answer`, migration 0011); `last_seen` stays their maximum and drives presence. LAN Sentinel's hosts have them unknown.
- A DHCP server's lease ACK no longer creates a host, opens a binding or refreshes the client: it is the server's statement, not the client's traffic (the lease history comes with the DHCP work). The `passive_dhcp_lease` address source is no longer written.
- A STALE (or DELAY, PROBE) neighbour entry records a MAC or address never seen before, but never makes a known host present again or confirms a binding.

TCP endpoints (`DATA_MODEL.md` §5.5, migration 0011):

- Results are kept per interface, address and port in `endpoints`, so one port on two addresses of one MAC no longer overwrite each other. The MAC that alone held the address at the result is an attribute; with none or several holders the result is kept unattributed. Every change of the result or of the attributed MAC is a row of `endpoint_results` (history, `services list --at`).
- The last result and the last definite answer (OPEN or REFUSED) are separate: a timeout never erases that the endpoint last answered OPEN. A local failure (permission, no source address, missing interface) is recorded as `LOCAL_ERROR` with its reason instead of being dropped.
- `SERVICE_STATE_CHANGED` (old and new result) marks an endpoint opening or leaving OPEN; `SERVICE_OPENED` and `SERVICE_CLOSED` are legacy. LAN Sentinel's `services` rows have no address and stay as `legacy_services`; no address is invented for them.

Names (`DATA_MODEL.md` §5.4, migration 0012):

- `name_records` keeps every advertised or resolved record with its name type, record type (A, AAAA, PTR, SRV, TXT, CNAME, NAME), the address or value it describes, source, advertiser, TTL and expiry. A record is linked to a host only by evidence: a NAME to the device that sent it (DHCP hostname, LLDP system name), an address record to the one MAC holding the address. An mDNS proxy or reflector advertising other devices' addresses binds nothing to itself; records for addresses no MAC holds stay unlinked and are shown by `hosts find <name>` as advertised.
- Several names per host and type are normal: a new name is `HOSTNAME_ADDED`, and `HOSTNAME_CHANGED` is legacy. Records end only on evidence: an mDNS goodbye (TTL 0, one second later), a cache-flush record replacing older ones (RFC 6762 §10), or the address passing to another MAC; the host's last record of a name ending is `HOSTNAME_REMOVED`. A TTL running out marks a record `expired` when read; time alone never ends a name. `preferred_name` is a display choice: the longest-held name of the first preferred type.
- The mDNS decoder emits one observation per response, the sender with its records; unicast DNS answers (PTR, A, AAAA, CNAME) carry no MAC. LAN Sentinel's names were copied into `name_records` as legacy records (record type NAME, link `legacy`, TTL and advertiser unknown); a legacy NAME confirmed again becomes a current record.
- LLDP's attachment is the Ethernet sender (the switch port); the chassis ID is metadata.

Other changes: operator scans sweep their known hosts first (so the probes after the sweep never wait out the 5 s target spacing behind their own ARP request) and `scan plan` simulates that order; compaction prunes `endpoint_results` and ended name records with the events' retention. `TestLANSentinelUpgrade` builds a schema-0009 database with LAN Sentinel rows and checks the upgrade and the correlator resuming on it.

### 4. Bounded discovery and naming

- Name queries (FR-AC-12, `active.names`, off by default): one-shot mDNS queries on interfaces with active discovery for the known `.local` names, the configured DNS-SD types and, if asked, one `_services._dns-sd._udp.local` enumeration whose answers are not chased; unicast DNS PTR for known addresses and A for known DNS names through the first configured resolver only. Names and addresses a record's TTL still covers are not asked; negative answers are kept for their SOA TTL (5 min to 1 h); CNAMEs are followed at most twice; at most `max_queries_per_pass`. Answers are name records only: an mDNS answer carries the responder's address, never a MAC, and makes nobody present. Every send is one start of the shared budget (`active.budgets.names`, 1/s), outside the per-target spacing; the policy allows only the mDNS group and configured resolvers. No other UDP is sent.
- TCP checks first ask the kernel for the route (unprivileged `RTM_GETROUTE` restricted to the interface): without a direct route and a source address on the interface nothing is dialled, no start is spent, and the endpoint records `LOCAL_ERROR` with why (routed via a gateway, no source address, no route). `scan plan` lists such known hosts and leaves them out of its estimate. A `/32` field interface reaches its candidates only through direct routes the operator adds; raw ARP still reaches them (an ARP probe from 0.0.0.0 without a matching address).
- `scan plan` simulates the concurrency slots: an unanswered probe holds its slot for its reply wait, so with 4 jobs in flight and a 1 s ARP timeout a sweep runs at about 4 requests/s, not 5; phases are barriers, as in a run. `config validate` uses the same rule for sweep times and warns when a sweep cannot fit its interval; a periodic pass that takes longer than its interval is flagged `overran` in `daemon status`; missed passes are never made up.
- The concurrency caps (`active.max_concurrent_jobs`, `active.budgets.tcp.max_concurrent_per_*`) are reported by `config reload` as needing a restart instead of being silently kept.

### 5. DHCP depth

- The DHCPv4 decoder reads the whole message into one observation (`observation.DHCPMessage`): BOOTP header (op, xid, flags, ciaddr, yiaddr, siaddr, giaddr, chaddr), options split across several instances joined (RFC 3396), the `file` and `sname` fields when option 52 overloads them, and options 1, 3, 6, 12, 15, 50, 51, 53, 54, 55, 58, 59, 60, 61, 81 (FQDN, wire or ASCII), 82 (circuit and remote ID), 119 (search list with bounded backward compression pointers), 121 and 249 (classless routes). Malformed options are named in the message and the rest kept; a truncated options field is flagged. Direction comes from the BOOTP op agreeing with the message type, so relay-to-server traffic (port 67 to 67) decodes too. Every list and string is bounded.
- A client message is `passive_dhcp`; its sender is the client only when the frame came straight from it (Ethernet source is chaddr, no giaddr), and only then does it carry the client's ciaddr claim and hostname. A server reply is `passive_dhcp_server` from the server or the relay that sent it; the separate `passive_dhcp_lease` observation is no longer written (replayed LAN Sentinel JSONL keeps working: its meta keys are read as a message).
- `dhcp_exchanges` (migration 0013): one row per transaction (interface, xid, chaddr) with its messages in order and retransmissions counted, each server's offer, the server the client selected, the requested address, the outcome (`ack`, `inform_ack`, `nak`, `offered`, `decline`, `release`, or `incomplete` for one closed unanswered after a minute idle) and whether both sides were seen. At most 1,024 are open per interface; the oldest is closed first. An exchange left open by a restart keeps its last outcome.
- `dhcp_leases` (migration 0013): one row per address and acknowledged configuration a server asserted for a client. A renewal with the same address, server and configuration extends the row (`last_ack`, `acks`, `expires_at`); a different address is `DHCP_LEASE_ASSIGNED`, a different configuration `DHCP_CLIENT_CONFIG_CHANGED`, both ending the old row as superseded. Configurations are compared on the options both ACKs carried: an option left out is unknown, not a change. The snapshot holds mask and prefix length, routers, DNS servers, domain, search list, hostname, FQDN, classless routes, relay agent information and the gateway they imply (the default route of option 121 when it is present, as RFC 3442 requires clients to do, else the first router). T1 and T2 are the advertised values or, without them, half and seven eighths of the lease, marked derived. `expires_at` is the expiry per the last ACK seen and never proof the address was released. A RELEASE or DECLINE of the open lease's address ends it; NAK, DECLINE and RELEASE are events. A lease neither creates a host nor makes its client present, and a relayed ACK describes a client that may be on another link.
- `DHCP_CONFIG_CHANGED` (one server's configuration compared across its clients) is legacy and no longer emitted: a server may answer clients differently, so changes are compared per client. `dhcp_servers.config_json` still shows the last offered or assigned configuration, without events.
- LAN Sentinel's `passive_dhcp_lease` observations still in the database are backfilled into `dhcp_leases` (each client's latest open, earlier rows superseded, all marked `backfilled`): what its decoder did not capture (sender, relay, configuration, T1/T2) is unknown.
- New reads: `dhcp leases` and `dhcp exchanges` (CLI), `/v1/dhcp/leases` and `/v1/dhcp/exchanges` (API), and a host's leases in `hosts show` (`dhcp_leases` in JSON). Compaction prunes ended leases with the events' retention and exchanges with the roll-ups'; open leases are kept however old.

### 6. Release readiness (v0.1.0)

- `OBSERVATION_GAP` is emitted: when a passive collector (capture, neighbour) leaves `running` and runs again, or is still down at shutdown, and for the daemon's own downtime, from the last heartbeat (`runtime_state` `observed_until`, written every 15 s and at shutdown) to the next start, when at least 1 minute. The daemon passes collector state changes to the correlator on the bus; disabled or unsupported collectors and replay record no gap.
- A MAC-less observation (TCP or ICMP result, address record) is attributed only to a sole holder whose binding was confirmed within `identity.address_expiry`: a stale binding may belong to a device that left.
- `services list --at T` leaves out endpoints with no result recorded by T instead of showing their current result.
- A takeover that closes every earlier holder of a conflicting address emits `DUPLICATE_IP_RESOLVED`.
- Name queries ask AAAA as well as A for known DNS names (FR-AC-12).
- `identity.oui_override` is reported by `config reload` as needing a restart (the table is loaded at start-up).
- Exchanges a restart left waiting become `incomplete` at start-up.
- A change of server identifier for the same address and configuration stays a `DHCP_CLIENT_CONFIG_CHANGED` (`server_id=` first): which server acknowledged a client is part of its configuration.
- The soak scenario feeds what the decoders now emit (mDNS records, DNS PTR answers, whole DHCP exchanges) and TCP results, and reports the tracker's tables.
- `LICENSE` (MIT), `SECURITY.md`, `CONTRIBUTING.md`, `RELEASE.md`, `codecov.yml`, README badges; the release tarballs carry the licence; the release workflow takes its notes from `RELEASE.md` and refuses a version without its section; CI uploads coverage to Codecov.

## Validation report (17 Oct 2026)

Gates for v0.1.0, run in the dev container and Docker on the release tree (the squashed migration commit); CI runs the same `make` targets on the pull request.

| Gate | Result |
| --- | --- |
| `make check` (fmt, tidy, vet, lint, vuln, race tests, crash tests) | Pass; coverage of `internal/` 92.3% (minimum 90%) |
| `make fuzz` (every `Fuzz*` target, 30 s each) | Pass, including `FuzzDHCP` on the new decoder |
| `make test-net` (uid 65534, `CAP_NET_RAW` only) | Pass. `TestActiveDiscovery`: probe starts equal to the plan and the duration within 10% of its estimate (131 starts against 131 planned, 39.5 s against 39.3 s on the earlier run), kernel packets counted apart, no payload and no UDP. `TestNameQueriesOnTheWire`: only mDNS to 224.0.0.251:5353 and DNS to the configured resolver, within the names budget and `max_queries_per_pass` |
| `make test-systemd` (Debian 11, 12, 13) | Pass; `systemd-analyze security` exposure 2.3 on each (≤ 2.5) |
| `make soak` (90 simulated days, 50-host LAN, with name records, TCP endpoints and DHCP exchanges) | Pass: 5.7 M observations, 80.1 µs CPU each, database 186.5 MB at day 90 (budget 200 MB; roll-ups are most of it), heap 5.3 MB, RSS 31.8 MB. Day 90: 2,220 name records, 50 endpoints with 31,238 result changes, 975 DHCP exchanges, 207 leases |
| `make package VERSION=v0.1.0` | Both tarballs with `SHA256SUMS`; contents the binary, `capcheck`, the unit, `config.yaml`, `config.active-example.yaml`, `README.md`, `LICENSE` and the inner checksums; the binary reports `v0.1.0`. Built locally only; the release is cut by the `release` workflow |

Upgrade: `TestLANSentinelUpgrade` builds a schema-0009 database with LAN Sentinel's rows (hosts with descriptions, bindings, names, services, identifications, an attempt, legacy events, the kill switch and lease observations) and checks the migration (foreign keys, integrity, IDs, descriptions, events with their wording, legacy names with TTL unknown, no invented endpoint addresses, backfilled leases with unknown configuration) and the correlator resuming on it.

Not done, and not claimable from these results:

- **Hardware and field checks** (`docs/TEST_PLAN.md`): no run on a Raspberry Pi CM4, RevPi Connect or amd64 edge box; no board audit with `capcheck`; no measurement of CPU, memory or the 128 MB unit ceiling on a board; no captured-wire evidence on a real switched or mirrored OT network; no passive or seven-day active pilot; no migration of a real LAN Sentinel database on a board. Docker and x86 results do not establish board performance.
- **Rollback** to LAN Sentinel is a runbook (`deploy/README.md`), not an automated test.
- **Soak margin:** 186.5 MB of the 200 MB budget after 90 days on the simulated LAN; a busier site reaches `storage.retention.max_db_size` sooner, which then shortens retention (oldest roll-ups first).
- **Repository:** the GitHub repository was not renamed from `boeboe/lan-sentinel`; this repository carries LAN Sentinel's history through the lineage merge. Merge the pull request with a merge commit, not a squash, to keep that history.
