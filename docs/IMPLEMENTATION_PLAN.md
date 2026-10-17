# LAN Tracker — Implementation Plan

Six phases, each ending in a shippable build. Passive discovery lands before any active probing so the first field deployments cannot disturb OT equipment. LAN Tracker and its code base are Linux only; every build and test runs in a Linux dev container via `make`, on Linux or macOS development machines. Estimates assume one experienced Go engineer: 15–19 weeks of build plus about 4 weeks of pilot and soak time that overlaps later phases. LAN Tracker is a connectivity tracker with history, without device identification (ADR 0011).

| Phase | Scope | Estimate | Exit gate |
| --- | --- | --- | --- |
| 0 | Foundations, platform layer, test infrastructure | 2–3 weeks | Daemon runs under systemd with only `CAP_NET_RAW` and reloads on SIGHUP |
| 1 | Observation pipeline, interfaces, neighbours | 2–3 weeks | Hosts appear from the neighbour table with their OUI registrant, scoped per interface |
| 2 | Passive capture | 3–4 weeks | Passive-only field build: 7-day pilot on 3 sites |
| 3 | API, CLI and metrics | 2–3 weeks | Historical-reconstruction acceptance test passes through the CLI |
| 4 | Active discovery with OT safety | 3–4 weeks | No faults in OT device soak; start-rate limits verified |
| 5 | DHCP depth, release builds, hardening, rollout | 2 weeks | **v1.0** on the full fleet; active discovery enabled per site |

The first release, v0.1.0, is cut once the container gates of phases 0–5 pass; the hardware and field checks below follow on the boards and pilot sites.

## Open hardware and field checks

Deferred checks that need real hardware or data from real sites. Each closes a phase task or exit criterion; the Docker or replay equivalent already passes on every change, so development continues meanwhile. `TEST_PLAN.md` is the procedure for the RevPi Connect and the amd64 edge box, with a results table per test; record results where noted and tick here.

| | Check | Closes | Docker equivalent (passing) | Record results in |
| --- | --- | --- | --- | --- |
| [ ] | `tools/capcheck` under the reference unit on each target board (RevPi Connect, amd64 edge box): every row passes with only `CAP_NET_RAW` | Phase 0 exit | `make test-net` (capcheck with and without `CAP_NET_RAW`), `make test-systemd` (capcheck under the unit's sandbox) | `ARCHITECTURE.md` §8 table; `TEST_PLAN.md` A4 |
| [ ] | Daemon on a target board on a real test LAN: hosts appear from the kernel neighbour table alone, with the OUI registrant and per-interface scoping (config with `passive: { enabled: false }`) | Phase 1 exit | `TestDaemonFindsHosts` in `make test-net`: two networks, a real OUI registrant, same MAC on both networks as two hosts linked by `MAC_ALSO_OBSERVED` | Phase 1 exit note below; `TEST_PLAN.md` B1, B2 |
| [ ] | pcap captures from the pilot sites (ARP, DHCP, mDNS, LLDP, NDP from real OT devices) added to `test/fixtures` with decoder table tests | Phase 2 task | Synthetic `test/fixtures/site-a-eth1.pcapng` (`make fixtures`) replayed by `TestCaptureScenario`; decoder tests on gopacket-built frames; fuzz targets | `test/fixtures`, decoder tests; `TEST_PLAN.md` G1 |
| [ ] | Passive-only build for 7 days on 3 pilot sites: < 5% CPU and < 50 MB RSS on a RevPi; an injected IP change and duplicate IP reconstructed | Phase 2 exit | `TestCaptureScenario` (IP change, takeover, duplicate IP and its resolution reconstructed from frames through the daemon); capture tests in `make test-net` | Phase 2 exit note below; `TEST_PLAN.md` B4, F1 |
| [ ] | 7-day representative active pilot against real OT devices (PLC, inverter, HMI), after passive observation: no device faults; device and network load reported | Phase 4 exit | `TestActiveDiscovery` in `make test-net` (start budgets per one-second window, excludes, spacing, no payload or UDP, kill switch) | Phase 4 status note; `TEST_PLAN.md` H1 |
| [ ] | Captured-wire evidence on a real network for ARP, TCP and name queries as enabled, and for the absence of identification traffic; kernel packets counted apart from starts | Phase 4 exit | `TestActiveDiscovery` (ARP, ICMP, TCP), `TestNameQueriesOnTheWire` (mDNS and DNS in Docker) | `TEST_PLAN.md` D2, D4 |
| [ ] | `systemd-analyze security lan-tracker` and `capcheck` (every row, including the `adjtimex` read) under the reference unit on each target board, on its Debian release | Phase 5 task | `make test-systemd` on Debian 11, 12 and 13 in containers: exposure 2.3, capcheck passes under the unit's sandbox | `ARCHITECTURE.md` §8 table; `TEST_PLAN.md` A3 |
| [ ] | Resource use on CM4 / RevPi and the amd64 edge box against the unit's `MemoryMax=128M` and `CPUQuota=20%` and NFR-PERF-1 | Phase 5 task | `make soak` on x86 in Docker, which does not establish board performance | `TEST_PLAN.md` E1 |
| [ ] | Upgrade from one release to the next and rollback on a board, following `deploy/README.md` (Upgrade and rollback); needs a second release | Phase 5 task | `TestMigrationsInOrderAndNewerSchemaRefused`, `TestOfflineRefusesOtherSchemaVersions`; `make test-systemd` (install block) | `TEST_PLAN.md` C7 |

## Phase 0 — Foundations, platform layer, test infrastructure (2–3 weeks)

- [x] Repo skeleton, Go module `github.com/otfabric/lan-tracker`, `golangci-lint`, CI, self-documenting `Makefile` (`make` lists targets)
- [x] Hygiene gate `make check`: `fmt-check`, `tidy-check`, `vet`, `lint`, `vuln` (`govulncheck` pinned as a Go tool), tests with race and coverage of `internal/` ≥ 90%; `make fuzz` runs every `Fuzz*` target; `make check-all` adds the Docker suites
- [x] Static cross-compiled builds for `linux/amd64` and `linux/arm64` (`CGO_ENABLED=0`), version/commit/date via `-ldflags`, `SHA256SUMS`
- [x] Linux dev container (`build/dev.Dockerfile`): every make target that runs Go runs in it, as the calling user, with a cache volume; no macOS stubs or build tags
- [x] CI: `make mod-verify check`, `make fuzz`, `make test-net test-systemd`, `make package`, all in the dev container
- [x] Platform layer: the four interfaces in `ARCHITECTURE.md` §3 with fakes for tests, collector registry with availability states
- [x] `internal/clock` (wall and simulated) used by everything that reads time
- [x] Config loader: schema version 1, YAML + `LAN_TRACKER_*` env + flags + defaults, strict validation (unknown keys fail)
- [x] Commands: `daemon run`, `config validate`, `config show [--sources]`, `version`
- [x] `slog` logging with journald handler and text/JSON fallback
- [x] SQLite store (`modernc.org/sqlite`) with embedded migrations, WAL, single writer goroutine, batched commits
- [x] Daemon lifecycle: context cancellation, `sd_notify` READY/WATCHDOG, graceful shutdown, SIGHUP reload — *verified under systemd in a container (`make test-systemd`)*
- [x] systemd container test (`make test-systemd`): reference unit unchanged, `Type=notify`, identity and capabilities, reload, clean stop, capcheck under the unit's sandbox, `systemd-analyze security` ≤ 2.5 (2.3, as root with a `CAP_NET_RAW` bounding set)
- [x] Docker network test harness (`make test-net`, `ARCHITECTURE.md` §9): test network with simulated hosts, runner as uid 65534 with only `CAP_NET_RAW`, `tools/capcheck` with and without the capability
- [ ] Capability check under the reference unit with only `CAP_NET_RAW`: every row of `ARCHITECTURE.md` §8, in Docker and on each target board; record results there. Any failure stops the project for a decision. — *Docker and systemd container: passed; boards tracked under Open hardware and field checks*
- [x] Golden reconstruction scenario (`DATA_MODEL.md` §10) committed under `test/golden/reconstruction/` as observation stream + expected bindings, events and query answers; a harness that validates the fixture (including that the expected answers follow from the expected bindings) and runs it through the correlator
- [x] `deploy/`: reference unit (root with a `CAP_NET_RAW` bounding set, no service user, `sysusers.d` or `tmpfiles.d`; ADR 0007), default config, replay `config.dev.yaml`

**Exit:** daemon starts under systemd as root with a `CAP_NET_RAW` bounding set, creates `hosts.db`, reloads config on SIGHUP, shuts down cleanly; capability check passed in Docker and on every target board; golden scenario committed. — *Status: met in Docker and the systemd containers; the board audits are open.*

## Phase 1 — Observation pipeline, interfaces, neighbours (2–3 weeks)

- [x] `Observation` type, `Source` enum, JSONL encoding, bounded bus with drop counter, ordered interface states and barriers
- [x] Replay collector, JSONL input: time-ordered merge of several files, simulated-clock and real-time modes, `exit_when_done`; replay and live interfaces cannot be mixed; `make run-dev`
- [x] Interface manager over `InterfaceMonitor` (netlink): one network context per interface name, `context_prefixes` history, follow changes, emit `INTERFACE_UP/DOWN`, `SUBNET_CHANGED` (applied by the correlator, in order with observations)
- [x] Neighbour collector over `NeighborSource` (netlink): startup dump, `RTM_NEWNEIGH`/`RTM_DELNEIGH` subscription, resubscribe and resync on loss and on interval, source `kernel_neighbor` with NUD state, stamped with the kernel's confirmation time
- [x] Docker network tests for the netlink backends: neighbours appearing, changing MAC (gratuitous ARP) and expiring (unresolvable, and a host that disappears); a network connected to and disconnected from the runner (link and address events); the daemon on two test networks finding hosts with their OUI registrant (a real Siemens OUI), scoped per interface (the same MAC on both networks is two hosts linked by `MAC_ALSO_OBSERVED`)
- [x] Correlator: `DATA_MODEL.md` §5.1 (host = context + MAC, unbound observations), §5.2 on-link check, §5.3 open/replace/takeover/conflict/expiry with the interval semantics of §3, late-observation rule; a MAC's addresses are independent (a new address never closes another); data-driven time; state loaded at startup; `HOST_DISCOVERED`, `IP_ADDED`, `IP_REMOVED`, `MAC_ALSO_OBSERVED`, `DUPLICATE_IP_DETECTED/RESOLVED` — `TestIPv4MultipleAddressesAreIndependent`, `TestMACOnTwoInterfacesIsAnotherHost`
- [x] Typed evidence (`DATA_MODEL.md` §2): presence refreshed only by frames, confirmed addresses and endpoint answers; a MAC-less observation attributed only to a fresh sole holder — `TestEvidence`, `TestProbeResultsProvePresenceOnlyWhenAnswered`
- [x] Schema constraints and partial unique indexes from `DATA_MODEL.md` §4 (`migrations/0001_init.sql`)
- [x] Presence state machine with configurable thresholds; `HOST_DISAPPEARED`, `HOST_REAPPEARED`
- [x] Event engine writing to `events` (with `cause` and `evidence_json` snapshot) and journald
- [x] OUI registrant lookup (IEEE MA-L/MA-M/MA-S, longest prefix) from a precomputed table searched in place (`internal/ouitable`, ADR 0010), generated by `make oui`; optional override file; locally administered flag

**Exit:** `make run-dev` replays the golden stream and the resulting database matches the expected bindings; in the Docker test network and on a test LAN, hosts appear from the kernel neighbour table alone, with their OUI registrant and correct per-interface scoping; the golden scenario passes at correlator level. — *Status: replay, golden scenario and Docker test network met; real test LAN open, tracked under Open hardware and field checks.*

## Phase 2 — Passive capture (3–4 weeks)

- [x] `AF_PACKET` capture per interface, BPF filter, optional promiscuous mode, ring buffer sizing (`passive.ring_size`), capture-drop counters; the host's own frames are never captured; reopen with back-off when the interface goes away
- [x] Docker network tests for capture: frames from other MACs injected from another container, own frames not captured, promiscuous mode with only `CAP_NET_RAW`, drop counter on an overflowing ring, the daemon naming hosts from captured frames
- [x] Decoders: ARP (incl. gratuitous and probes), IPv4 source, IPv6 + NDP, DHCPv4, mDNS (A/AAAA/PTR/SRV/TXT), DNS responses, LLDP; repeats suppressed for 2 minutes
- [x] Fuzz target per decoder (and for whole frames)
- [ ] pcap fixtures from real sites — *synthetic fixture in place (`make fixtures`); real captures tracked under Open hardware and field checks*
- [x] Replay collector, pcap/pcapng input through the same decoders and refresh suppression
- [x] Name records with type, advertiser and TTL (`DATA_MODEL.md` §5.4) and `preferred_name` precedence; `HOSTNAME_ADDED/REMOVED`; mDNS proxy attribution, goodbye and cache flush; LLDP sender as attachment; DNS names attach without presence — `TestMDNSProxyAdvertisesAnotherDevice`, `TestMDNSAliasesGoodbyeAndCacheFlush`, `TestLLDPIdentifiers`
- [x] Conflict detection from capture (conflicting ARP replies, gratuitous ARP) feeding the correlator's conflict handling; proxy-ARP flagging (`DATA_MODEL.md` §5.2)
- [x] Retention, hourly roll-up and compaction job; `max_db_size` enforcement; golden scenario re-run after simulated 7-day compaction gives identical answers and evidence
- [x] Startup integrity check with quarantine and recreate; `DATABASE_RECREATED`

**Exit:** full golden scenario passes at correlator level; passive-only build runs 7 days on 3 pilot sites with < 5% CPU and < 50 MB RSS on a RevPi, and reconstructs an injected IP change and duplicate IP correctly. — *Status: golden scenario passes at correlator level, also after a simulated 7-day compaction; the site capture replayed through the daemon reconstructs the IP change and the duplicate IP from frames; capture verified in Docker with only `CAP_NET_RAW`. The pilot run is open, tracked under Open hardware and field checks.*

## Phase 3 — Read side: API, CLI, metrics (2–3 weeks)

- [x] REST API over `/run/lan-tracker/api.sock` (0660, owner root), optional `127.0.0.1` listener; `/v1/status` includes per-collector state (`API.md`)
- [x] Endpoints: `/v1/status`, `/v1/interfaces`, `/v1/hosts`, `/v1/hosts/{id}`, `/v1/hosts/{id}/history`, `/v1/hosts/{id}/evidence`, `/v1/hosts/find?q=&interface=&at=`, `/v1/history?q=&interface=&since=&until=` (IP/MAC/name timelines), `/v1/observations?host=&interface=&mac=&ip=&source=&unbound=&since=&until=&rollups=`, `/v1/events`, `/v1/events/stream` (JSON lines, backs `watch`), `/v1/services`, `/v1/config`, `/v1/db`, `/v1/db/check` (scan and kill-switch endpoints in phase 4, DHCP endpoints in phase 5)
- [x] CLI per `CLI.md`: subcommand tree, query auto-detection, `find --at` result cases (0 / 1 / ≥2 holders, unconfirmed, replaced-by), IP history, `hosts evidence`, `observations list`, `table`/`json`/`jsonl`/`csv` output, exit codes; online and offline behind one interface over the same queries
- [x] `--offline` per the contract in `CLI.md` §1, tested for: live DB beside the writer, no `-wal` (immutable open), unreadable `-wal`, `-shm` missing in a read-only directory, busy timeout, a schema other than the binary's
- [x] Prometheus `/metrics` on the local listener, low-cardinality metrics only (enforced by the metrics writer)

**Exit:** the golden scenario's queries pass end to end through the CLI, online and with `--offline`. — *Met: `TestCLIReconstruction` (test/golden) replays the scenario through the daemon and runs every point-in-time query and IP history through the CLI online, offline beside the running daemon, and offline after it stopped; `make test-systemd` runs `daemon status`, `hosts list` and an offline read under the hardened unit.*

## Phase 4 — Active discovery with OT safety (3–4 weeks)

- [x] Probe scheduler: per-probe intervals, startup delay, jitter, randomised target order, global and per-protocol start budgets (probe starts per second, ADR 0011), concurrency cap, TCP per-interface and per-host caps, per-target spacing, excludes, timeout back-off
- [x] Kill switch: env, `POST /v1/active/disable` and `/v1/active/enable`, persisted in `runtime_state`, `ACTIVE_DISABLED/ENABLED` events, `active disable|enable` CLI
- [x] Scan planner shared by `scan plan` (`POST /v1/scans/plan`, and offline from config) and `scan run` (`POST /v1/scans`), with identical refusal rules; it simulates concurrency slots and phase barriers, and overrunning passes are flagged, never made up — `TestComputePlan`, `TestSimulateMatchesPacing`, `TestStatusShowsLastPass`
- [x] `Transmitter`: `AF_PACKET` frame writes, `SO_BINDTODEVICE` TCP connects, ping or raw ICMP sockets, route lookups
- [x] ARP sweep (native Go) within configured networks only; `max_auto_scan_prefix_v4` guard; deduplicated sweep scope and the 1,024-address default cap — `TestSweepTargets`, `TestWideSweeps`
- [x] TCP connect probes bound to the interface, dialled only on a direct route with a source address; results per address and port (OPEN, REFUSED, TIMEOUT, UNREACHABLE, `LOCAL_ERROR` with the reason), last result and last definite answer, `SERVICE_STATE_CHANGED` — `TestEndpointsPerAddress`, `TestEndpointAttributionNeedsOneHolder`, `TestServices`, `TestNoDirectRouteSendsNothing`, `TestPlanNoDirectRoute`
- [x] ICMP echo via unprivileged ping sockets where allowed, else raw with `CAP_NET_RAW`
- [x] Name queries (mDNS, unicast DNS) for known names and addresses and configured DNS-SD types, behind the shared budget and policy, off by default — `TestMDNSQuery`, `TestDNSQueries`, `TestNameQueriesTakeTheBudget`, `TestNamePassAsksOnlyWhatIsKnown`, `TestNamePolicy`, `TestNameQueriesOnTheWire` (test-net)
- [ ] ~~IPv6 NDP solicitation for known addresses (no IPv6 sweeping)~~ — *deferred: v1 active discovery is IPv4-first (decided 6 Oct 2026)*
- [x] Scan profiles from config; `scan run` CLI; `SCAN_STARTED/COMPLETED` events and `scans` rows
- [x] Docker network tests for probes: probe starts per protocol and per target against the budgets, kernel packets counted apart, excludes, prefix guard, result classification, no payload and no UDP other than name queries

**Exit:** active probing defaults to off, and when enabled never exceeds the global, per-protocol and per-host start limits on a traffic-counting test rig; `scan plan` estimates match measured starts within 10%; the kill switch stops probing within one tick and survives a restart; soak test against real OT devices (PLC, inverter, HMI) shows no faults.

*Status (17 Oct 2026):* met in Docker (`make test-net`, `TestActiveDiscovery`, as uid 65534 with only `CAP_NET_RAW`): every one-second window on the wire within the global and per-protocol start budgets, probe starts equal to the plan and the duration within 10% of its estimate, kernel packets counted apart, no probe to excluded or unconfigured addresses, the target spacing kept, no payload and no UDP, OPEN/REFUSED/UNREACHABLE classified, the kill switch stopping a running sweep within one second and persisting across a restart. `TestNameQueriesOnTheWire` asks only the mDNS group and the configured resolver, within the names budget. Not on the wire in `make test-net`: a `/32` interface reaching off-subnet candidates. Open: captured-wire evidence on a real network and the soak test against real OT devices (PLC, inverter, HMI), which need the site hardware.

## Phase 5 — DHCP depth, release builds, hardening, pilot rollout (2 weeks)

- [x] Release artifacts: static binaries for `linux/amd64` and `linux/arm64` with SHA-256 checksums; no `.deb` or other packages — *`make release` and `make package` (reproducible: the commit's date is stamped, not the build's); the manual `release` workflow cuts a release from `main` with a patch, minor or major bump (the first is v0.1.0), tags the tested commit and publishes one tarball per target (binary, `capcheck`, deploy files, MIT `LICENSE`) with `SHA256SUMS`, with notes from `RELEASE.md`*
- [ ] Capability audit on the boards; `systemd-analyze security` exposure ≤ 2.5 on the target systemd versions — *containers: `make test-systemd` on Debian 11, 12 and 13 (systemd 247, 252, 257) passes with exposure 2.3 and capcheck (with the clock read) under the unit's sandbox; the boards tracked under Open hardware and field checks*
- [x] Clock-jump handling: mark events written before NTP sync — *a read-only `adjtimex` of the kernel's `STA_UNSYNC` (works with chrony, needs no capability); each event records `synced`, `unsynced` or `unknown`; `daemon status` shows the clock; the unit allows `adjtimex` alone of the `@clock` calls (`ProtectClock=` off)* — `TestEventsRecordClockState`
- [x] `lan-tracker config reload` (requested 6 Oct 2026): the daemon re-reads its file on request and answers what took effect and what needs a restart; each interface's `active` settings are reloadable, so active discovery is enabled per site without a restart (`CLI.md`, `API.md`, FR-CFG-3)
- [x] Passive DHCP server monitoring (requested 6 Oct 2026, FR-PA-7): server identities (option 54) per interface with sender, relay and advertised configuration in `dhcp_servers`, an optional per-interface allowlist, `DHCP_SERVER_DISCOVERED`/`UNEXPECTED`/`MAC_CHANGED`, `dhcp servers` (CLI, API), `lan_tracker_dhcp_servers`; covered by the capture scenario (a rogue server) and `make test-net`
- [x] Full DHCPv4 decoder: header, options as listed in FR-PA-3, overload, RFC 3396, bounded lists, malformed and truncated flags, relay traffic on 67 to 67 — `TestDecode`, `TestDecodeMalformed`, `TestDHCPGather`, `TestDHCPOptionValues`, `FuzzDHCP`
- [x] Bounded DHCP exchanges (FR-PA-8) — `TestDHCPExchangeOutcomes`, `TestDHCPExchangeLimit`
- [x] Per-client lease history with snapshots, relay and partial visibility, per-client configuration comparison (FR-PA-9, FR-PA-10) — `TestDHCPLeaseHistory`, `TestDHCPServerConfig`, `TestCaptureScenario`
- [x] `dhcp leases`, `dhcp exchanges`, `/v1/dhcp/leases`, `/v1/dhcp/exchanges`, leases in `hosts show` — `internal/cli` and `internal/api` tests
- [x] Compaction keeps open leases and prunes ended ones — `TestCompactRetention`
- [x] Host descriptions (requested 7 Oct 2026, FR-MD-1): `hosts set <host> --description`, `hosts unset <host> --description`, through the daemon as an operator action, `HOST_DESCRIBED` per change with the caller, shown in `hosts show`, `hosts find` and `hosts list` (`--description` filter)
- [x] Collection gaps visible in history: `OBSERVATION_GAP` for passive collectors and daemon downtime — `TestCollectorGaps`, `TestDowntimeGap`, `TestLivePipeline`
- [ ] Data pressure, clock changes, capture loss and cancellation exercised together — *each has unit coverage (`TestCompactSizeCap`, `TestEventsRecordClockState`, `TestCaptureDropCounter` in `make test-net`, `TestConnectCancelledAndStopped`); no combined run*
- [ ] Gates on the release commit: `make mod-verify check` (coverage ≥ 90%), `make fuzz`, `make test-net`, `make test-systemd`, `test/crash`, `make soak`, `make package` with version strings, archive contents, checksums and capabilities checked
- [ ] First release, v0.1.0, from `main` by the `release` workflow — *the maintainer's action*
- [ ] Staged fleet rollout (below) — *runbook with checks, upgrade, rollback and what to watch per stage in `deploy/README.md`; the rollout itself is a field task*

**Exit:** v1.0 on the full fleet with active discovery enabled per site. — *Not met: the DHCP work is met for complete and partial exchanges, competing offers, renewals, NAK, DECLINE and RELEASE, changed DNS, classless routes and relayed clients (surviving compaction is checked on the tables by `TestCompactRetention`, not by replaying a DHCP scenario through compaction); the release, the board audits and the field checks are open.*

## Testing strategy

| Layer | How | What it proves |
| --- | --- | --- |
| Decoders | Table tests over captured pcaps from real sites (ARP, DHCP, mDNS, LLDP, NDP) | Parsing of real OT device traffic, including malformed frames |
| Replay | Site pcaps replayed through the daemon with a simulated clock, compared with expected inventories | End-to-end behaviour on any machine, reproducible field issues |
| Toolchain | Dev container for every make target, locally and in CI | Identical Go, lint and test environment on every machine |
| Correlator | Golden tests: observation sequence in → expected bindings and events out, starting with the reconstruction scenario (`DATA_MODEL.md` §10) | Independent addresses, takeover, duplicate IP and resolution, a MAC on a second interface, NIC swap (new host), same IP on two interfaces, routed source IPs ignored, unbound probe results |
| Store | Migrations on an empty database; a newer schema refused; compaction; kill -9 during write (`test/crash`: the daemon killed five times while it writes, then the integrity check, no quarantine, every committed row kept) | Crash safety and the forward-only schema |
| Platform backends and probes | Docker test network with simulated hosts (`make test-net`), runner as uid 65534 with only `CAP_NET_RAW`; in CI | Capture, netlink and transmit code under the real privilege model; start budgets, excludes, scope guards, result classification |
| Fuzzing | `go test -fuzz` on every decoder | No panics on hostile input |
| Soak | Simulated first (`make soak`: 90 days of a 50-host LAN through the correlator and store on a simulated clock, hourly compaction at the default retention, the size cap off), then 7 days on a lab rig with real PLC, inverter and HMI | Memory stable, no device faults, DB growth within budget |
| Acceptance | Scripted: change an IP, swap a device, inject a duplicate, then `hosts find --at` and `hosts history`; the acceptance scenarios mapped to tests in `TEST_PLAN.md` | The reconstruction requirement |

Performance budgets on a RevPi Connect: < 5% average CPU, < 50 MB RSS, DB < 200 MB after 90 days on a 50-host LAN.

*Simulated (`make soak`, 17 Oct 2026, dev container):* a worst-case 50-host LAN (always-on OT devices, weekday laptops, phones with a new MAC every day, router, switch), with name records, TCP endpoints and DHCP exchanges: 5.7 million observations in 90 days, 80.1 µs of CPU each. The database reached 186.5 MB on day 90 with the size cap off (roll-ups are most of it), within the 200 MB budget; heap 5.3 MB and RSS 31.8 MB. On day 90: 2,220 name records, 50 endpoints with 31,238 result changes, 975 DHCP exchanges and 207 leases. The 7-day board run (`TEST_PLAN.md` E1) confirms CPU and RSS on the hardware.

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
| Fragile OT device faults under probing | Site outage, loss of trust | Passive-first rollout, low default start rates, back-off, excludes, kill switch |
| Wrong attribution (proxy ARP, gateway MAC on routed traffic, duplicate IPs) | Misleading history | Host = MAC per interface, no merges; on-link check; proxy-ARP flagging; conflicts kept as parallel bindings |
| Data model drifts from the acceptance criterion | Rework late in the project | Golden reconstruction scenario from phase 0, run on every change |
| A capture or netlink path needs `CAP_NET_ADMIN` | Security model and unit change | `capcheck` in Docker on every change (passing) and on target boards in phase 0 |
| Docker's kernel differs from the target kernels | A path works in tests but not in the field | Board capability check in phase 0; pilot sites before fleet rollout |
| Capture drops on busy mirrored ports | Missed observations | BPF filter to discovery protocols only, ring sizing, drop metric |
| SD card wear from SQLite writes | Storage failure | Batched commits every 5 s, compaction, `synchronous=NORMAL` in WAL |
| Clock jumps on boxes without RTC | Broken timelines | Mark events written before NTP sync |
| Kernel access spreads outside the platform layer | Code becomes hard to test without a network | Platform interfaces with fakes; `AGENTS.md` rule 2 |
| OUI data goes stale | Unknown registrants | Regenerate each release; optional override file |
