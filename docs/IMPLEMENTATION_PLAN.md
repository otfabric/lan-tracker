# LAN Tracker — Implementation Plan

Six phases, each ending in a shippable build. Passive discovery lands before any active probing so the first field deployments cannot disturb OT equipment. LAN Tracker and its code base are Linux only; every build and test runs in a Linux dev container via `make`, on Linux or macOS development machines. Estimates assume one experienced Go engineer: 15–19 weeks of build plus about 4 weeks of pilot and soak time that overlaps later phases.

Phases 0–6 below are LAN Sentinel's plan, kept as the record of what was built. On 16 Oct 2026 the product became OT Fabric LAN Tracker, a connectivity tracker without identification (ADR 0012); the migration's own phases 0–5, from `migration-brief.md` §12, follow in [Migration to LAN Tracker](#migration-to-lan-tracker). Items that the migration removed or changed are marked where they stand.

| Phase | Scope | Estimate | Exit gate |
| --- | --- | --- | --- |
| 0 | Foundations, platform layer, test infrastructure | 2–3 weeks | Daemon runs under systemd with only `CAP_NET_RAW` and reloads on SIGHUP |
| 1 | Observation pipeline, interfaces, neighbours | 2–3 weeks | Hosts appear from the neighbour table with vendor, scoped per interface |
| 2 | Passive capture | 3–4 weeks | **v0.1 field release:** passive only, 7-day pilot on 3 sites |
| 3 | API, CLI and metrics | 2–3 weeks | Historical-reconstruction acceptance test passes through the CLI |
| 4 | Active discovery with OT safety | 3–4 weeks | No faults in OT device soak; packet-rate limits verified |
| 5 | Release builds, hardening, rollout | 2 weeks | **v1.0** on the full fleet; active discovery enabled per site |
| 6+ | ~~Identification plugins~~ | — | *Removed 16 Oct 2026 (ADR 0012)* |

## Open hardware and field checks

Deferred checks that need real hardware or data from real sites. Each closes a phase task or exit criterion; the Docker or replay equivalent already passes on every change, so development continues meanwhile. `TEST_PLAN.md` is the procedure for the RevPi Connect and the amd64 edge box, with a results table per test; record results where noted and tick here.

| | Check | Closes | Docker equivalent (passing) | Record results in |
| --- | --- | --- | --- | --- |
| [ ] | `tools/capcheck` under the reference unit on each target board (RevPi Connect, amd64 edge box): every row passes with only `CAP_NET_RAW` | Phase 0 exit | `make test-net` (capcheck with and without `CAP_NET_RAW`), `make test-systemd` (capcheck under the unit's sandbox) | `ARCHITECTURE.md` §8 table; passed on a Raspberry Pi CM4 with Debian 11 (6 Oct 2026) |
| [ ] | Daemon on a target board on a real test LAN: hosts appear from the kernel neighbour table alone, with the OUI registrant and per-interface scoping (config with `passive: { enabled: false }`) | Phase 1 exit | `TestDaemonFindsHosts` in `make test-net`: two networks, a real OUI registrant, same MAC on both networks as two hosts linked by `MAC_ALSO_OBSERVED` | Phase 1 exit note below |
| [ ] | pcap captures from the pilot sites (ARP, DHCP, mDNS, LLDP, NDP from real OT devices) added to `test/fixtures` with decoder table tests | Phase 2 task | Synthetic `test/fixtures/site-a-eth1.pcapng` (`make fixtures`) replayed by `TestCaptureScenario`; decoder tests on gopacket-built frames; fuzz targets | `test/fixtures`, decoder tests |
| [ ] | `systemd-analyze security lan-tracker` and `capcheck` (every row, including the `adjtimex` read) under the reference unit on each target board, on its Debian release | Phase 5 task | `make test-systemd` on Debian 11, 12 and 13 in containers: exposure 2.3, capcheck passes under the unit's sandbox | `ARCHITECTURE.md` §8 table; passed on a Raspberry Pi CM4 with Debian 11, exposure 2.3 (6 Oct 2026) |
| [ ] | 7-day representative active pilot against real OT devices (PLC, inverter, HMI), after passive observation: no device faults; device and network load reported | Phase 4 exit; migration phase 5 | `TestActiveDiscovery` in `make test-net` (start budgets per one-second window, excludes, spacing, no payload or UDP, kill switch) | Phase 4 status note; `TEST_PLAN.md` H1 |
| [ ] | Captured-wire evidence on a real network for ARP, TCP and name queries as enabled, and for the absence of identification traffic; kernel packets counted apart from starts | Migration phase 5 | `TestActiveDiscovery` (ARP, ICMP, TCP), `TestNameQueriesOnTheWire` (mDNS and DNS in Docker) | `TEST_PLAN.md` D2, D4 |
| [ ] | Resource use on CM4 / RevPi and the amd64 edge box against the unit's `MemoryMax=128M` and `CPUQuota=20%` and NFR-PERF-1 | Migration phase 5 | `make soak` on x86 in Docker, which does not establish board performance | `TEST_PLAN.md` E1 |
| [ ] | Migration from LAN Sentinel and rollback on a board, following `deploy/README.md` | Migration phase 5 | `TestLANSentinelUpgrade`; `make test-systemd` (install and upgrade blocks) | `TEST_PLAN.md` C7 |
| [ ] | Passive-only build for 7 days on 3 pilot sites: < 5% CPU and < 50 MB RSS on a RevPi; an injected IP change and duplicate IP reconstructed | Phase 2 exit | `TestCaptureScenario` (IP change, takeover, duplicate IP and its resolution reconstructed from frames through the daemon); capture tests in `make test-net` | Phase 2 exit note below |

## Phase 0 — Foundations, platform layer, test infrastructure (2–3 weeks)

- [x] Repo skeleton, Go module `lan-tracker`, `golangci-lint`, CI, self-documenting `Makefile` (`make` lists targets)
- [x] Hygiene gate `make check`: `fmt-check`, `tidy-check`, `vet`, `lint`, `vuln` (`govulncheck` pinned as a Go tool), tests with race and coverage of `internal/` ≥ 90%; `make fuzz` runs every `Fuzz*` target; `make check-all` adds the Docker suites
- [x] Static cross-compiled builds for `linux/amd64` and `linux/arm64` (`CGO_ENABLED=0`), version/commit/date via `-ldflags`, `SHA256SUMS`
- [x] Linux dev container (`build/dev.Dockerfile`): every make target that runs Go runs in it, as the calling user, with a cache volume; no macOS stubs or build tags
- [x] CI: `make mod-verify check`, `make fuzz`, `make test-net test-systemd`, `make release tools`, all in the dev container — *green on GitHub*
- [x] Platform layer: the four interfaces in `ARCHITECTURE.md` §3 with fakes for tests, collector registry with availability states
- [x] `internal/clock` (wall and simulated) used by everything that reads time
- [x] Config loader: YAML + `LAN_TRACKER_*` env + flags + defaults, strict validation (unknown keys fail)
- [x] Commands: `daemon run`, `config validate`, `config show [--sources]`, `version`
- [x] `slog` logging with journald handler and text/JSON fallback
- [x] SQLite store (`modernc.org/sqlite`) with embedded migrations, WAL, single writer goroutine, batched commits
- [x] Daemon lifecycle: context cancellation, `sd_notify` READY/WATCHDOG, graceful shutdown, SIGHUP reload — *verified under systemd in a container (`make test-systemd`)*
- [x] systemd container test (`make test-systemd`): reference unit unchanged, `Type=notify`, identity and capabilities, reload, clean stop, capcheck under the unit's sandbox, `systemd-analyze security` ≤ 2.5 (1.6 then; 2.3 since the daemon runs as root, 6 Oct 2026)
- [x] Docker network test harness (`make test-net`, `ARCHITECTURE.md` §9): test network with simulated hosts, runner as uid 65534 with only `CAP_NET_RAW`, `tools/capcheck` with and without the capability
- [ ] Capability check under the reference unit with only `CAP_NET_RAW`: every row of `ARCHITECTURE.md` §8, in Docker and on each target board; record results there. Any failure stops the project for a decision. — *Docker and systemd container: passed; boards deferred, tracked under Open hardware and field checks*
- [x] Golden reconstruction scenario (`DATA_MODEL.md` §10) committed under `test/golden/reconstruction/` as observation stream + expected bindings, events and query answers; a harness that validates the fixture (including that the expected answers follow from the expected bindings) and runs it through the correlator (skipped until phase 1, so CI stays green)
- [x] `deploy/`: reference unit, `sysusers.d`, `tmpfiles.d`, default config, replay `config.dev.yaml` — *since 6 Oct 2026 the daemon runs as root with a `CAP_NET_RAW` bounding set, so `sysusers.d` and `tmpfiles.d` are gone (`REQUIREMENTS.md` decisions)*

**Exit:** daemon starts under systemd as `lan-tracker` (as root with a `CAP_NET_RAW` bounding set since 6 Oct 2026) with only `CAP_NET_RAW`, creates `hosts.db`, reloads config on SIGHUP, shuts down cleanly; capability check passed in Docker and on every target board; golden scenario committed.

## Phase 1 — Observation pipeline, interfaces, neighbours (2–3 weeks)

- [x] `Observation` type, `Source` enum, JSONL encoding, bounded bus with drop counter, ordered interface states and barriers
- [x] Replay collector, JSONL input: time-ordered merge of several files, simulated-clock and real-time modes, `exit_when_done`; replay and live interfaces cannot be mixed; `make run-dev`
- [x] Interface manager over `InterfaceMonitor` (netlink): one network context per interface name, `context_prefixes` history, follow changes, emit `INTERFACE_UP/DOWN`, `SUBNET_CHANGED` (applied by the correlator, in order with observations)
- [x] Neighbour collector over `NeighborSource` (netlink): startup dump, `RTM_NEWNEIGH`/`RTM_DELNEIGH` subscription, resubscribe and resync on loss and on interval, source `kernel_neighbor` with NUD state, stamped with the kernel's confirmation time
- [x] Docker network tests for the netlink backends: neighbours appearing, changing MAC (gratuitous ARP) and expiring (unresolvable, and a host that disappears); a network connected to and disconnected from the runner (link and address events); the daemon on two test networks finding hosts with their vendor (a real Siemens OUI), scoped per interface (the same MAC on both networks is two hosts with `MAC_MOVED`)
- [x] Correlator v1: `DATA_MODEL.md` §5.1 (host = context + MAC, unbound observations), §5.2 on-link check, §5.3 open/replace/takeover/conflict/expiry with interval semantics of §3, late-observation rule, §5.5 services; data-driven time; state loaded at startup; `HOST_DISCOVERED`, `IP_ADDED`, `IP_CHANGED`, `IP_REMOVED`, `MAC_MOVED`, `DUPLICATE_IP_DETECTED/RESOLVED`, `SERVICE_OPENED/CLOSED` — *since migration phases 1–2 a MAC's addresses are independent (no `IP_CHANGED`), a second interface is `MAC_ALSO_OBSERVED` and endpoints change with `SERVICE_STATE_CHANGED`; the older types stay for legacy events*
- [x] Schema constraints and partial unique indexes from `DATA_MODEL.md` §4 (in the phase 0 migration)
- [x] Presence state machine with configurable thresholds; `HOST_DISAPPEARED`, `HOST_REAPPEARED`
- [x] Event engine writing to `events` (with `cause` and `evidence_json` snapshot) and journald
- [x] OUI vendor lookup (IEEE MA-L/MA-M/MA-S, longest prefix) with a generator (`make oui`) that builds a compact embedded table; optional override file; locally administered flag — *since 6 Oct 2026 a precomputed table searched in place (`internal/ouitable`): no parsing at start-up, which took 6 s on a CM4 under `CPUQuota=20%`*

**Exit:** `make run-dev` replays the golden stream and the resulting database matches the expected bindings; in the Docker test network and on a test LAN, hosts appear from the kernel neighbour table alone, with vendor and correct per-interface scoping; the golden scenario passes at correlator level except the conflict steps (phase 2). — *Status: replay, golden scenario (including the conflict steps, implemented early) and Docker test network met; real test LAN deferred, tracked under Open hardware and field checks.*

## Phase 2 — Passive capture (3–4 weeks)

- [x] `AF_PACKET` capture per interface, BPF filter, optional promiscuous mode, ring buffer sizing (`passive.ring_size`), capture-drop counters; the host's own frames are never captured; reopen with back-off when the interface goes away
- [x] Docker network tests for capture: frames from other MACs injected from another container, own frames not captured, promiscuous mode with only `CAP_NET_RAW`, drop counter on an overflowing ring, the daemon naming hosts from captured frames
- [x] Decoders: ARP (incl. gratuitous and probes), IPv4 source, IPv6 + NDP, DHCP (option 12 hostname, 61 client-id, 55 parameter list and 60 vendor class; *no fingerprinting since ADR 0012, and the full DHCPv4 decoder replaced this one in migration phase 4*), mDNS (A/AAAA/PTR/SRV/TXT), DNS PTR responses, LLDP; repeats suppressed for 2 minutes
- [x] Fuzz target per decoder (and for whole frames)
- [ ] pcap fixtures from real sites — *synthetic fixture in place (`make fixtures`); real captures tracked under Open hardware and field checks*
- [x] Replay collector, pcap/pcapng input through the same decoders and refresh suppression
- [x] Name bindings (`DATA_MODEL.md` §5.4) and `preferred_name` precedence; `HOSTNAME_ADDED/CHANGED/REMOVED`; DNS PTR names attach without presence
- [x] Conflict detection from capture (conflicting ARP replies, gratuitous ARP) feeding the correlator's conflict handling (done in phase 1); proxy-ARP flagging (`DATA_MODEL.md` §5.2)
- [x] Retention, hourly roll-up and compaction job; `max_db_size` enforcement; golden scenario re-run after simulated 7-day compaction gives identical answers and evidence
- [x] Startup integrity check with quarantine and recreate; `DATABASE_RECREATED`

**Exit:** full golden scenario passes at correlator level; passive-only build runs 7 days on 3 pilot sites with < 5% CPU and < 50 MB RSS on a RevPi, and reconstructs an injected IP change and duplicate IP correctly. First field-deployable release (v0.1). — *Status: golden scenario passes at correlator level, also after a simulated 7-day compaction; the site capture replayed through the daemon reconstructs the IP change and the duplicate IP from frames; capture verified in Docker with only `CAP_NET_RAW`. The pilot run is pending, tracked under Open hardware and field checks.*

## Phase 3 — Read side: API, CLI, metrics (2–3 weeks)

- [x] REST API over `/run/lan-tracker/api.sock` (0660; owner root since 6 Oct 2026), optional `127.0.0.1` listener; `/v1/status` includes per-collector state (`API.md`)
- [x] Endpoints: `/v1/status`, `/v1/interfaces`, `/v1/hosts`, `/v1/hosts/{id}`, `/v1/hosts/{id}/history`, `/v1/hosts/{id}/evidence`, `/v1/hosts/find?q=&interface=&at=`, `/v1/history?q=&interface=&since=&until=` (IP/MAC/name timelines), `/v1/observations?host=&interface=&mac=&ip=&source=&unbound=&since=&until=&rollups=`, `/v1/events`, `/v1/events/stream` (JSON lines, backs `watch`), `/v1/services`, `/v1/config`, `/v1/db`, `/v1/db/check` (scan and kill-switch endpoints in phase 4)
- [x] CLI per `CLI.md`: subcommand tree, query auto-detection, `find --at` result cases (0 / 1 / ≥2 holders, unconfirmed, replaced-by), IP history, `hosts evidence`, `observations list`, `table`/`json`/`jsonl`/`csv` output, exit codes; online and offline behind one interface over the same queries
- [x] `--offline` per the contract in `CLI.md` §1, tested for: live DB beside the writer, no `-wal` (immutable open), unreadable `-wal`, `-shm` missing in a read-only directory, busy timeout
- [x] Prometheus `/metrics` on the local listener, low-cardinality metrics only (enforced by the metrics writer)

**Exit:** the golden scenario's queries pass end to end through the CLI, online and with `--offline`. — *Met: `TestCLIReconstruction` (test/golden) replays the scenario through the daemon and runs every point-in-time query and IP history through the CLI online, offline beside the running daemon, and offline after it stopped; `make test-systemd` runs `daemon status`, `hosts list` and an offline read under the hardened unit.*

## Phase 4 — Active discovery with OT safety (3–4 weeks)

- [x] Probe scheduler: per-probe intervals, startup delay, jitter, randomised target order, global and per-protocol token buckets, concurrency cap, TCP per-interface and per-host caps, per-target spacing, excludes, timeout back-off
- [x] Kill switch: env, `POST /v1/active/disable` and `/v1/active/enable`, persisted in `runtime_state`, `ACTIVE_DISABLED/ENABLED` events, `active disable|enable` CLI
- [x] Scan planner shared by `scan plan` (`POST /v1/scans/plan`, and offline from config) and `scan run` (`POST /v1/scans`), with identical refusal rules
- [x] `Transmitter`: `AF_PACKET` frame writes, `SO_BINDTODEVICE` TCP connects, ping or raw ICMP sockets
- [x] ARP sweep (native Go) within configured networks only; `max_auto_scan_prefix_v4` guard
- [x] TCP connect probes bound to the interface, with OPEN/REFUSED/TIMEOUT/UNREACHABLE; `SERVICE_OPENED/CLOSED` — *results per address and port with `LOCAL_ERROR` and `SERVICE_STATE_CHANGED` since migration phase 2*
- [x] ICMP echo via unprivileged ping sockets where allowed, else raw with `CAP_NET_RAW`
- [ ] ~~IPv6 NDP solicitation for known addresses (no IPv6 sweeping)~~ — *deferred: v1 active discovery is IPv4-first (decided 6 Oct 2026)*
- [x] ~~UDP framework that only runs protocol-specific probes; no generic UDP open/closed claims; NTP and EtherNet/IP ListIdentity probes (disabled by default, enrichment only)~~ — *removed 16 Oct 2026 (ADR 0012); no UDP is sent except the name queries*
- [x] Scan profiles from config; `scan run` CLI; `SCAN_STARTED/COMPLETED` events and `scans` rows
- [x] Docker network tests for probes: packet counting per protocol and per target against the budgets, excludes, prefix guard, result classification

**Exit:** active probing defaults to off, and when enabled never exceeds the global, per-protocol and per-host limits on a traffic-counting test rig; `scan plan` estimates match measured packets within 10%; the kill switch stops probing within one tick and survives a restart; soak test against real OT devices (PLC, inverter, HMI) shows no faults.

*Status (6 Oct 2026, LAN Sentinel's packet budgets; since ADR 0012 the same test measures start budgets, with kernel packets logged apart):* met in Docker (`make test-net`, `TestActiveDiscovery`, as uid 65534 with only `CAP_NET_RAW`): every one-second window on the wire within the global and per-protocol budgets (at most 10 ARP requests and 10 packets in any second against budgets of 10 and 20), no probe to excluded or unconfigured addresses, ≥ 1 s between probes to one target, a measured scan of 145 packets in about 17.5 s against a plan of 147 packets and an estimated typical duration of 17.1 s, OPEN/REFUSED/UNREACHABLE classified, the kill switch stopping a running sweep within one second and persisting across a restart. Open: the soak test against real OT devices (PLC, inverter, HMI), which needs the site hardware.

## Phase 5 — Release builds, hardening, pilot rollout (2 weeks)

- [x] Release artifacts: static binaries for `linux/amd64` and `linux/arm64` with SHA-256 checksums; no `.deb` or other packages — *`make release` and `make package` (reproducible: the commit's date is stamped, not the build's); the manual `release` workflow cuts a release from `main` with a patch, minor or major bump (first v0.0.1), tags the tested commit and publishes one tarball per target (binary, `capcheck`, deploy files) with `SHA256SUMS`*
- [ ] Capability audit on the boards; `systemd-analyze security` exposure ≤ 2.5 on the target systemd versions — *containers: `make test-systemd` on Debian 11, 12 and 13 (systemd 247, 252, 257) passes with exposure 2.3 (root, from 6 Oct 2026), capcheck (now with the clock read) under the unit's sandbox; on a Raspberry Pi CM4 with Debian 11 the same (6 Oct 2026); the other boards tracked under Open hardware and field checks*
- [x] Clock-jump handling: mark events written before NTP sync — *a read-only `adjtimex` of the kernel's `STA_UNSYNC` (works with chrony, needs no capability); each event records `synced`, `unsynced` or `unknown` (migration 0004); `daemon status` shows the clock; the unit allows `adjtimex` alone of the `@clock` calls (`ProtectClock=` off)*
- [x] `lan-tracker config reload` (requested 6 Oct 2026): the daemon re-reads its file on request and answers what took effect and what needs a restart; each interface's `active` settings are reloadable, so active discovery is enabled per site without a restart (`CLI.md`, `API.md`, FR-CFG-3)
- [x] Passive DHCP server monitoring (requested 6 Oct 2026, FR-PA-3, FR-PA-7): server identities (option 54) per interface with sender, relay and advertised configuration in `dhcp_servers`, an optional per-interface allowlist, `DHCP_SERVER_DISCOVERED`/`UNEXPECTED`/`MAC_CHANGED` and `DHCP_CONFIG_CHANGED` (*withdrawn in migration phase 4: compared per client instead*), server-confirmed leases (`passive_dhcp_lease`, *replaced by the lease history in migration phase 4*) kept apart from client claims, `dhcp servers` (CLI, API), `lan_tracker_dhcp_servers`; covered by the capture scenario (a rogue server, a configuration change) and `make test-net`
- [x] Host descriptions (requested 7 Oct 2026, FR-MD-1): `hosts set <host> --description`, `hosts unset <host> --description`, through the daemon as an operator action, `HOST_DESCRIBED` per change with the caller (migration 0007), shown in `hosts show`, `hosts find` and `hosts list` (`--description` filter)
- [x] ~~Device type in `hosts list`: a `DEVICE` column and `--device` filter (7 Oct 2026)~~ — *removed 16 Oct 2026 (ADR 0012); the column is the OUI registrant*
- [ ] Staged fleet rollout (below) — *runbook with checks, upgrade, rollback and what to watch per stage in `deploy/README.md`; the rollout itself is a field task*

**Exit:** v1.0 on the full fleet with active discovery enabled per site.

## Phase 6+ — Identification plugins (removed)

*Removed 16 Oct 2026 (ADR 0012, `MIGRATION.md` §2). Kept as a record; do not rebuild any of it.*

Plugin interface in `internal/identify` taking a host's evidence and returning (field, value, confidence, evidence). Candidates in order of expected value: mDNS service types, Modbus Device Identification (FC 43/14), HTTP `Server` header and title, TLS certificate subject, DHCP fingerprint, SNMP `sysDescr`, hostname patterns.

- [x] ~~Plugin interface (`identify.Identifier`: a name and `Identify(observation) []Claim`) applied by the correlator to every observation of a present host, each identifier switchable on reload (`identity.identifiers`); a source's value changes only to a more confident claim, the adopted one is marked `current` (migration 0006) and `hosts.device_type` is the most confident claim of any source (`DATA_MODEL.md` §5.7) — *started 7 Oct 2026*~~ — *removed*
- [x] ~~Passive identifiers, sending nothing: mDNS service types and `_device-info` model, DHCP fingerprint (vendor class, the iOS/macOS request list), hostname patterns, LLDP capabilities; `hosts show` shows the device type and current claims, `hosts evidence` every claim~~ — *removed*
- [x] ~~Active identifiers: Modbus Device Identification (FC 43/14) — *opt-in, once per MAC (ADR 0011, 8 Oct 2026)*~~ — *removed*
- [x] ~~Active identifiers: HTTP `Server` and title, TLS certificate subject, SNMPv2c `sysDescr`, SSH banner, Telnet banner, FTP SYST — *opt-in, once per MAC (ADR 0011, 8 Oct 2026)*~~ — *removed*

## Migration to LAN Tracker

The brief (`migration-brief.md` §12) sets six phases, each a reviewable change. Phases 0–4 are commits on `migrate/lan-tracker`; `MIGRATION.md` §1–5 is their change log. The tests named here run in the dev container (`make check`) unless marked `make test-net`; `TEST_PLAN.md` maps the brief's acceptance matrix (§13) to them.

### Migration phase 0 — Baseline and contract (16 Oct 2026)

- [x] Baseline at `a437a22` recorded, with `make mod-verify check`, `make test-net` and `make test-systemd` passing (`MIGRATION.md`, Baseline gates)
- [x] Narrowed purpose, scope and evidence contract in ADR 0012, `REQUIREMENTS.md` and `AGENTS.md`; ADR 0011 superseded
- [x] Licensing and repository transfer status recorded (`MIGRATION.md`, Lineage)
- [x] A `LICENSE` file — MIT, the maintainer's choice (17 Oct 2026); in the release tarballs
- [ ] Repository transfer or rename of `boeboe/lan-sentinel` — *the maintainer's action; this branch carries the full history into `otfabric/lan-tracker` instead*

**Exit:** met. The contract and the baseline report are in the repository.

### Migration phase 1 — Rename and remove identification

- [x] Module, binary, unit, paths, `LAN_TRACKER_*` and `lan_tracker_*`
- [x] Identification probes, UDP NTP and ENIP probes, passive classifiers, `identify run`, device types and their config removed; legacy rows readable
- [x] OUI shown as a registrant hint; proxy ARP kept as `hosts.proxy_arp` (migration 0010)
- [x] Configuration schema 2 with start rates; removed keys and `LAN_SENTINEL_*` refused; `config convert` leaves active discovery off — `TestSchemaTwoRefusesLegacy`, `TestConvertLegacy`, `TestConvertRefuses`, `TestDeployConfigs`

**Exit:** met in Docker. `TestActiveDiscovery` (`make test-net`) checks that ARP, ICMP and TCP work and that no payload or UDP leaves the box; removed keys are rejected; `TestLANSentinelUpgrade` opens the legacy history.

### Migration phase 2 — The connectivity model

- [x] Typed evidence; presence refreshed only by frames, confirmed addresses and endpoint answers (migration 0011) — `TestEvidence`, `TestProbeResultsProvePresenceOnlyWhenAnswered`
- [x] TCP endpoints per address and port, last result and last definite answer, `LOCAL_ERROR`, history — `TestEndpointsPerAddress`, `TestEndpointAttributionNeedsOneHolder`, `TestServices`
- [x] Independent addresses per MAC; `MAC_ALSO_OBSERVED` — `TestIPv4MultipleAddressesAreIndependent`, `TestMACOnTwoInterfacesIsAnotherHost`
- [x] Name records with type, advertiser and TTL (migration 0012); mDNS proxy attribution, goodbye and cache flush; LLDP sender as attachment — `TestMDNSProxyAdvertisesAnotherDevice`, `TestMDNSAliasesGoodbyeAndCacheFlush`, `TestLLDPIdentifiers`
- [x] Legacy facts kept as unknown, not invented — `TestLANSentinelUpgrade`

**Exit:** met by the golden and correlator tests: two IPs on one MAC, the same IP on two interfaces, duplicate IPs, gateway and proxy advertisements, timeout against refusal.

### Migration phase 3 — Bounded discovery and naming

- [x] Name queries (mDNS, unicast DNS) behind the shared budget and policy, off by default — `TestMDNSQuery`, `TestDNSQueries`, `TestNameQueriesTakeTheBudget`, `TestNamePassAsksOnlyWhatIsKnown`, `TestNamePolicy`, `TestNameQueriesOnTheWire` (test-net)
- [x] Route and source checks before TCP; `LOCAL_ERROR` with the reason; `scan plan` lists unreachable hosts — `TestNoDirectRouteSendsNothing`, `TestPlanNoDirectRoute`
- [x] Deduplicated sweep scope and the 1,024-address default cap — `TestSweepTargets`, `TestWideSweeps`
- [x] `scan plan` simulates concurrency slots and phase barriers; overrunning passes flagged, never made up — `TestComputePlan`, `TestSimulateMatchesPacing`, `TestStatusShowsLastPass`

**Exit:** met with fakes and in Docker for ARP, TCP and name queries (`TestNameQueriesOnTheWire`: only the mDNS group and the configured resolver are asked, within the names budget). Not on the wire in `make test-net`: a `/32` interface reaching off-subnet candidates by raw ARP.

### Migration phase 4 — DHCP depth and historical queries

- [x] Full DHCPv4 decoder: header, options 1–121 and 249 as listed in FR-PA-3, overload, RFC 3396, bounded lists, malformed and truncated flags, relay traffic on 67 to 67 — `TestDecode`, `TestDecodeMalformed`, `TestDHCPGather`, `TestDHCPOptionValues`, `FuzzDHCP`
- [x] Bounded exchanges (migration 0013) — `TestDHCPExchangeOutcomes`, `TestDHCPExchangeLimit`
- [x] Per-client lease history with snapshots, relay and partial visibility, per-client configuration comparison; `DHCP_CONFIG_CHANGED` withdrawn — `TestDHCPLeaseHistory`, `TestDHCPServerConfig`, `TestCaptureScenario`
- [x] Backfill of LAN Sentinel's lease observations — `TestLANSentinelUpgrade`
- [x] `dhcp leases`, `dhcp exchanges`, `/v1/dhcp/leases`, `/v1/dhcp/exchanges`, leases in `hosts show` — `internal/cli` and `internal/api` tests
- [x] Compaction keeps open leases and prunes ended ones — `TestCompactRetention`

**Exit:** met for complete and partial exchanges, competing offers, renewals, NAK, DECLINE and RELEASE, changed DNS, classless routes and relayed clients. Surviving compaction is checked on the tables (`TestCompactRetention`), not by replaying a DHCP scenario through compaction; the golden reconstruction after compaction has no DHCP.

### Migration phase 5 — Migration, hardening and field validation (container gates done; field validation open)

- [x] Upgrade, copy and rollback runbook in `deploy/README.md` (Migrating from LAN Sentinel, Rolling back to LAN Sentinel); a schema-0009 fixture upgraded by `TestLANSentinelUpgrade`
- [ ] Gates at the final commit, recorded in `MIGRATION.md`: `make mod-verify check` (coverage ≥ 90%), `make fuzz`, `make test-net`, `make test-systemd`, `test/crash`, `make soak` (re-measured with migrations 0010–0013), `make package` with version strings, archive contents, checksums and capabilities checked
- [ ] Data pressure, clock changes, capture loss and cancellation exercised together under the new schema — *each has unit coverage (`TestCompactSizeCap`, `TestEventsRecordClockState`, `TestCaptureDropCounter` in `make test-net`, `TestConnectCancelledAndStopped`); no combined run*
- [x] Collection gaps visible in history — `OBSERVATION_GAP` for passive collectors and daemon downtime (`TestCollectorGaps`, `TestDowntimeGap`, `TestLivePipeline`)
- [ ] Board audits, captured-wire evidence, resource use, migration on a board, passive pilot, 7-day active pilot — *under Open hardware and field checks; only board and site results close them*
- [ ] First LAN Tracker release from `main`

**Exit:** an executable migration and rollback runbook (written, not yet run on a board), measured traffic and resource results on the boards, no lost descriptions or invented legacy evidence (`TestLANSentinelUpgrade`), and field acceptance of the intended active profile. Not met.

## Testing strategy

| Layer | How | What it proves |
| --- | --- | --- |
| Decoders | Table tests over captured pcaps from real sites (ARP, DHCP, mDNS, LLDP, NDP) | Parsing of real OT device traffic, including malformed frames |
| Replay | Site pcaps replayed through the daemon with a simulated clock, compared with expected inventories | End-to-end behaviour on any machine, reproducible field issues |
| Toolchain | Dev container for every make target, locally and in CI | Identical Go, lint and test environment on every machine |
| Correlator | Golden tests: observation sequence in → expected bindings and events out, starting with the reconstruction scenario (`DATA_MODEL.md` §10) | IP change, takeover, duplicate IP and resolution, MAC move, NIC swap (new host), same IP on two interfaces, routed source IPs ignored, unbound probe results |
| Store | Migrations on empty and previous-version DBs; compaction; kill -9 during write (`test/crash`: the daemon killed five times while it writes, then the integrity check, no quarantine, every committed row kept) | Crash safety and upgrade path |
| Platform backends and probes | Docker test network with simulated hosts (`make test-net`), runner as uid 65534 with only `CAP_NET_RAW`; in CI | Capture, netlink and transmit code under the real privilege model; rate limits, excludes, scope guards, result classification |
| Fuzzing | `go test -fuzz` on every decoder | No panics on hostile input |
| Soak | Simulated first (`make soak`: 90 days of a 50-host LAN through the correlator and store on a simulated clock, hourly compaction at the default retention, the size cap off), then 7 days on a lab rig with real PLC, inverter and HMI | Memory stable, no device faults, DB growth within budget |
| Acceptance | Scripted: change an IP, swap a device, inject a duplicate, then `hosts find --at` and `hosts history`; the brief's acceptance matrix mapped to tests in `TEST_PLAN.md` | The reconstruction requirement and the migration contract |

Performance budgets on a RevPi Connect: < 5% average CPU, < 50 MB RSS, DB < 200 MB after 90 days on a 50-host LAN.

*Simulated (`make soak`, 7 Oct 2026, LAN Sentinel's schema, dev container on Apple silicon; to be re-run with migrations 0010–0013):* a worst-case 50-host LAN, a mirror port with every host's traffic seen and the ARP sweep every 5 minutes (35 always-on OT devices, 12 weekday laptops, 3 phones with a new MAC every day, router, switch): 5.3 million observations in 90 days, 77.6 µs of CPU each (about 5 s of CPU a day here, well under a minute on a RevPi). The database reached 158.8 MB on day 90 with the size cap off (raw observations steady at about 80 MB from day 7, roll-ups growing to 90 days), within the 200 MB budget; heap 2.0 → 3.4 MB and RSS 25 → 27 MB while the hosts grew from 52 to 244. The 7-day board run (`TEST_PLAN.md` E1) confirms CPU and RSS on the hardware.

## Rollout (each release)

1. Lab rig, passive only, full soak.
2. 3 pilot sites passive only for 2 weeks; compare inventory against what field support knows is there.
3. 50 sites (5%) passive only; watch capture drops, CPU and DB size in Prometheus.
4. Whole fleet passive only.
5. Active discovery enabled on the pilot sites, then per site after review of that site's device mix.

Each collector has a config flag so a misbehaving decoder can be switched off fleet-wide through config without a new build.

## Risks

| Risk | Impact | Mitigation |
| --- | --- | --- |
| Fragile OT device faults under probing | Site outage, loss of trust | Passive-first rollout, low default pps, back-off, excludes, kill switch |
| Wrong attribution (proxy ARP, gateway MAC on routed traffic, duplicate IPs) | Misleading history | Host = MAC per interface, no merges; on-link check; proxy-ARP flagging; conflicts kept as parallel bindings |
| Data model drifts from the acceptance criterion | Rework late in the project | Golden reconstruction scenario from phase 0, run on every change |
| A capture or netlink path needs `CAP_NET_ADMIN` | Security model and unit change | `capcheck` in Docker on every change (passing) and on target boards in phase 0 |
| Docker's kernel differs from the target kernels | A path works in tests but not in the field | Board capability check in phase 0; pilot sites before fleet rollout |
| Capture drops on busy mirrored ports | Missed observations | BPF filter to discovery protocols only, ring sizing, drop metric |
| SD card wear from SQLite writes | Storage failure | Batched commits every 5 s, compaction, `synchronous=NORMAL` in WAL |
| Clock jumps on boxes without RTC | Broken timelines | Mark events written before NTP sync |
| Kernel access spreads outside the platform layer | Code becomes hard to test without a network | Platform interfaces with fakes; `AGENTS.md` rule 2 |
| OUI data goes stale | Unknown registrants | Regenerate each release; optional override file |
