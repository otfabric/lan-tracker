# AGENTS.md — OT Fabric LAN Tracker

This file is the entry point for every AI coding agent working in this repository, whatever the tool. Read it before you change anything. The tool-specific files (`CLAUDE.md`, `.github/copilot-instructions.md`, `.cursor/rules/`) point here and add only notes about their own tool. The hard rules (§4) are numbered; code and docs cite them as "AGENTS.md rule N".

## 1. What this is

OT Fabric LAN Tracker keeps an evidence-based history of which MAC addresses, IP addresses, names and TCP endpoints are visible on each local OT (industrial) network, so operators can find equipment, troubleshoot connectivity and reconstruct incidents. It answers, after the fact: *which MAC held this IP at that time, which other addresses and names it had, what DHCP told it, whether the configured TCP ports answered, and what the collector could not see*.

**It is a connectivity tracker with history, not an identification product** ([ADR 0012](docs/adr/0012-connectivity-tracker-scope.md)). It does not determine equipment models, operating systems, firmware or physical asset identity; the operator's description on a MAC is the only statement about what a device is. The project was migrated from `boeboe/lan-sentinel` (baseline `a437a22`, 8 Oct 2026); [`docs/MIGRATION.md`](docs/MIGRATION.md) records what was removed and why. Do not restore identification probes, classifiers or confidence ranking under any name.

It is one static Go binary, `lan-tracker`, which is the daemon, the CLI client, the database inspector and the on-demand scanner. It runs under systemd on Linux edge devices (Raspberry Pi CM4, RevPi Connect, amd64 edge boxes).

Data flows one way: collectors and probes emit **observations** onto a bus. A single **correlator** goroutine turns them into hosts, bindings and events. A single SQLite **writer** commits them. The REST API on a Unix socket, the CLI and Prometheus read the result.

Current state of the work: [`docs/STATUS.md`](docs/STATUS.md). Where things live: [`docs/AI_REPO_MAP.md`](docs/AI_REPO_MAP.md).

Fixed names and paths:

| Item | Value |
| --- | --- |
| Repository / Go module | `otfabric/lan-tracker` / `github.com/otfabric/lan-tracker` |
| Binary | `lan-tracker` |
| systemd unit | `lan-tracker.service` |
| Service user | `root`, bounding set `CAP_NET_RAW` only (no service user or group) |
| Config | `/etc/lan-tracker/config.yaml` |
| Database | `/data/lan-tracker/hosts.db` |
| API socket | `/run/lan-tracker/api.sock` |
| Minimum kernel | 5.10 |
| Env prefix | `LAN_TRACKER_` |
| Metrics prefix | `lan_tracker_` |

## 2. Where the truth lives

| Document | Owns |
| --- | --- |
| `docs/REQUIREMENTS.md` | What the system must do (FR/NFR ids); §4 out of scope; **§5 recorded decisions, with dates** |
| `docs/ARCHITECTURE.md` | Components, data flow, key decisions (§2), OT safety controls (§5), config and reload (§6), systemd unit and privileges (§8), test harnesses (§9) |
| `docs/DATA_MODEL.md` | The `Observation` type, SQLite schema, correlation rules (§5), presence, the event catalogue (§7), retention, the golden reconstruction scenario (§10) |
| `docs/CLI.md` | The v1 command-line contract, the offline-access contract, exit codes |
| `docs/API.md` | The local REST API and the metrics |
| `docs/IMPLEMENTATION_PLAN.md` | Phases, tasks (checkboxes), exit criteria, open hardware checks, testing strategy |
| `docs/TEST_PLAN.md` | The manual hardware test procedure for the boards |
| `docs/adr/` | Why the foundational decisions were taken, and what they rule out |
| `docs/STATUS.md` | A dated snapshot of what is done, partial, deferred and blocked |
| `deploy/README.md` | Install, upgrade, rollback and rollout; `make test-systemd` runs its install block verbatim |

**Precedence when sources disagree**

1. The user's explicit instruction in the current task.
2. The hard rules in §4.
3. Recorded decisions: `REQUIREMENTS.md` §5 and the ADRs.
4. The specifications: `REQUIREMENTS`, `DATA_MODEL`, `CLI`, `API` and `ARCHITECTURE`.
5. `IMPLEMENTATION_PLAN.md` and `STATUS.md`.
6. The code and tests.
7. Code comments.

**If code and docs disagree, stop and ask.** Do not silently "fix" either side.

**Do not infer architecture from the implementation.** If the specifications do not cover a behaviour, ask before you invent one. When you get an answer, record it in the relevant document: a new row in `REQUIREMENTS.md` §5 with the date, and an ADR if the decision is foundational.

## 3. Package map

| Path | Responsibility |
| --- | --- |
| `cmd/lan-tracker` | `main`: hands off to `internal/cli` |
| `internal/cli` | cobra commands. A `backend` interface serves both online (API client) and `--offline` (read-only DB) reads; table, JSON, JSONL and CSV output |
| `internal/api` | REST server on the Unix socket (plus an optional `127.0.0.1` listener with `/metrics`) and the CLI's client. Operator endpoints go through `Control` |
| `internal/daemon` | Lifecycle: config, store, pipeline, SIGHUP and `config reload`, sd_notify and watchdog, shutdown; implements `api.Control` |
| `internal/config` | Schema version 2 (`types.go`), defaults, YAML + env + flags loading, strict validation (`LAN_SENTINEL_*` refused), the reload diff (`summary.go`), `config convert` from LAN Sentinel's schema 1 (`convert.go`) |
| `internal/observation` | The `Observation` type, sources, evidence kinds (`Evidence()`) and meta keys, the decoded DHCP message (`DHCPMessage`), JSONL encoding, the bounded bus with barriers and operator messages |
| `internal/correlate` | **The only writer of state and events**: host matching, bindings, conflicts, presence, name records, TCP endpoints, DHCP servers, exchanges and leases, the proxy-ARP flag, operator actions |
| `internal/events` | The event catalogue (it must match the migration CHECK) and the engine that writes events to the table and journald |
| `internal/store` | SQLite: migrations, the single writer, batches, compaction, the integrity check, and `Reader` (queries shared by the API and `--offline`); `storetest` seeds test databases |
| `internal/platform` | **The only code that touches AF_PACKET, netlink or raw sockets**: `Capturer`, `NeighborSource`, `InterfaceMonitor`, `Transmitter` (frames, TCP connect, UDP for name queries, ICMP, the kernel route lookup); fakes in `platform/fake` |
| `internal/service` | sd_notify and the journald handler |
| `internal/iface` | Interface manager: contexts, prefixes, up and down |
| `internal/collect/capture` | Passive capture loop and BPF filter; `decoders/` holds pure frame → observation functions (ARP, IP, NDP, DHCP, DNS/mDNS, LLDP) |
| `internal/collect/neighbor` | Kernel neighbour table: dump, watch, resync |
| `internal/collect/replay` | pcap/pcapng/JSONL replay on a simulated clock |
| `internal/probe` | What the engines share: the start budget (and `Simulate`, its replay for estimates), policy, kill switch, targets, back-off, the planner (`Compute`) |
| `internal/probe/{arp,icmp,tcp}` | Probe engines: ARP sweep, ICMP echo, bare TCP connect after a route check |
| `internal/probe/names` | Name query engine: one-shot mDNS and unicast DNS to configured resolvers, about known names and addresses only |
| `internal/probe/probetest` | Shared test helpers for the engines |
| `internal/probe/scheduler` | Periodic loops per interface and protocol; operator scans; last-pass summaries |
| `internal/netrange` | IPv4 range arithmetic and the keyed random walk for sweeps |
| `internal/identify` | OUI lookup (a MAC-prefix registrant hint, never a manufacturer) and MAC helpers. No classifiers |
| `internal/ouitable` | Format of the precomputed OUI table (`data/oui/oui.bin`) |
| `internal/clock` | Wall clock and simulated clock: everything that reads time takes a `clock.Clock` |
| `internal/metrics` | Prometheus text writer with a fixed list of allowed labels |
| `internal/logging`, `internal/buildinfo` | slog handler selection; version stamped through `-ldflags` |
| `migrations/` | Numbered `NNNN_description.sql` files, embedded, applied forward only at start-up |
| `data/oui/` | IEEE registry (`oui.tsv.gz`), the embedded table (`oui.bin`), the generator (`gen/`) |
| `test/golden` | Golden correlator scenarios, including reconstruction (`DATA_MODEL.md` §10), the CLI end-to-end tests and the LAN Sentinel database upgrade (`TestLANSentinelUpgrade`) |
| `test/frames`, `test/fixtures` | gopacket frame builders; the synthetic pcap fixture and its generator |
| `test/net` | Docker network tests (tag `nettest`, `make test-net`), run as uid 65534 with only `CAP_NET_RAW` |
| `test/systemd` | The daemon under systemd on Debian 11/12/13 (`make test-systemd`) |
| `test/crash`, `test/soak` | kill -9 during writes (in `make check`); 90 simulated days (tag `soak`, `make soak`) |
| `tools/capcheck` | Privilege and feasibility check for boards; shipped in the release tarballs |
| `build/` | The dev container (`dev.Dockerfile`) and the scripts make runs in it |
| `deploy/` | Reference unit, default config, the reviewed ARP + TCP example (`config.active-example.yaml`), replay dev config, install guide |

## 4. Hard rules

1. **Static Linux builds only.**
   - `CGO_ENABLED=0`; the delivered targets are `linux/amd64` and `linux/arm64`.
   - Never add a dependency that needs cgo: no libpcap, no mattn/go-sqlite3.
   - Use `modernc.org/sqlite`, `gopacket/afpacket` and `gopacket/layers` (also to build probe frames), and `vishvananda/netlink`.
   - Raw frame I/O for probes goes through the generic frame connection in `internal/platform`; protocol logic stays out of the platform package.
   - Do not add `mdlayher/arp` or `mdlayher/ndp` unless gopacket plus the platform layer cannot do something cleanly.
2. **Linux only, with kernel access behind the platform interfaces.**
   - No build tags, and no stubs or implementations for other OSes.
   - Capture, neighbour and interface monitoring, and probe transmission go through `internal/platform` (`Capturer`, `NeighborSource`, `InterfaceMonitor`, `Transmitter`), so the rest can be tested with fakes.
   - sd_notify and journald go through `internal/service`.
   - Run Go only through `make` (the dev container), never directly on a macOS host.
3. **No packages.**
   - A release is one tarball per target containing the static binary, `capcheck`, the reference unit and config from `deploy/`, plus SHA-256 checksums.
   - It is published by the manual `release` workflow from `main`.
   - No `.deb`, `.rpm` or installers.
4. **Collectors only emit observations.** Collectors and probes never touch the database or host state. They send `observation.Observation` values on the bus. Only the correlator goroutine mutates state and writes events.
5. **Single SQLite writer.** One writer goroutine, WAL mode, batched transactions. CLI `--offline` opens the database read-only (`mode=ro`).
6. **Host = MAC per interface.**
   - Within a network context (the interface), a host has exactly one MAC, and the MAC is the only identity evidence. Hosts are never merged or split. IPs, names, DHCP client identifiers, descriptions and ports are attributes or evidence, never merge keys.
   - A MAC is an observed network attachment, not an authenticated physical asset.
   - Rows are keyed by an internal UUID `host_id`.
   - The same IP or MAC on two interfaces is two different hosts or bindings.
   - Bindings use `first_seen`, `last_seen` and `ended_at` (NULL = open), as `DATA_MODEL.md` §3 defines.
7. **OT safety first.**
   - Active discovery is off by default and only scans explicitly configured networks.
   - Every active send (ARP request, ICMP echo, DNS or mDNS query, TCP connect) takes one start from the shared budget (`probe.Budget`): the global start rate, the per-protocol rates, the concurrency caps, the TCP caps, target spacing, excludes and the kill switch. The rate contract is probe starts per second, not packets on the wire (ADR 0012).
   - Never add SYN scanning, generic UDP port scanning, register scanning or IPv6 sweeping.
   - TCP checks are a plain `connect()` with immediate close and no payload. No banners, TLS handshakes, HTTP or Modbus requests.
   - ARP sweeps the configured networks; ICMP and TCP probe known hosts and configured targets only; TCP never waits for a ping. Name queries ask only about names and addresses already known, or configured DNS-SD types.
   - Silence, a timeout or an expired lease is never "offline".
8. **No journal spam.** Observations are never logged. Only state transitions (events) go to journald.
9. **No high-cardinality metrics.** Never put a MAC, IP, hostname or host ID in a Prometheus label.
10. **Least privilege.**
    - The reference unit runs the daemon as root (decided 6 Oct 2026) with `CapabilityBoundingSet=CAP_NET_RAW`, so it holds that capability alone.
    - The code must keep working as an unprivileged user with only `CAP_NET_RAW`; `make test-net` runs it that way.
    - Do not introduce anything that needs `CAP_NET_ADMIN`, another capability or root's file access.
11. **Out of scope** (ADR 0012).
    - Device identification of any kind: payload probes, banner reading, passive OS/device/vendor classification, confidence ranking, fingerprint databases, and running `modbusctl`, `sunspecctl`, `otprobe` or any external tool from the daemon. Removed on 16 Oct 2026; do not rebuild, scaffold or stub it.
    - VLAN tagging, a web UI, any data leaving the box (fleet aggregation, remote API), alerting, automatic network repair, continuous packet recording, DHCPv6, a DHCP client or server.
    - Do not build, design, scaffold or stub these without an explicit request and a recorded decision.
12. **IPv4-first.** v1 active discovery is IPv4, and ARP is the primary LAN discovery mechanism. IPv6 active probing (NDP solicitation) is deferred until it is requested. Passive IPv6/NDP decoding stays.

## 5. Read this before editing

| Before you touch | Read | Because |
| --- | --- | --- |
| `internal/correlate`, `test/golden` | `DATA_MODEL.md` §3, §5–§7, §10 | Interval semantics, correlation rules and the event catalogue; the reconstruction scenario must always pass |
| `internal/store`, `migrations/` | `DATA_MODEL.md` §4, §9; §13 below | Constraints, indexes, retention; released migrations are immutable |
| `internal/probe/**` | `ARCHITECTURE.md` §3 (Active probes) and §5; rules 7, 11, 12 | The safety envelope: every send passes the budget, policy and kill switch |
| `internal/platform` | `ARCHITECTURE.md` §3 (Platform layer) and §8 | Privilege table; must work with only `CAP_NET_RAW` |
| `internal/collect/capture` | `ARCHITECTURE.md` §3 (Passive capture), `DATA_MODEL.md` §2 | MAC-evidence rules, own-frame exclusion, refresh suppression, DHCP provenance |
| `internal/config` | `ARCHITECTURE.md` §6, `REQUIREMENTS.md` FR-CFG-* | Strict keys; which keys reload and which need a restart |
| `internal/cli`, `internal/api` | `CLI.md`, `API.md` | Public contract, exit codes, the offline-access contract (`CLI.md` §1) |
| `internal/identify` | ADR 0012, `DATA_MODEL.md` §5.7 | The OUI is a registrant hint only; there are no classifiers to extend |
| `deploy/`, `tools/capcheck/README.md` | `ARCHITECTURE.md` §8, `deploy/README.md` | `make test-systemd` runs the install and audit blocks verbatim, so edit them as working shell |
| `internal/metrics` | `API.md` (Metrics), rule 9 | Labels come from a fixed allow-list |

## 6. Invariants that are easy to break

- **Time is data.** The correlator uses each observation's timestamp. Timers are stamped with the moment their threshold was crossed. Everything takes a `clock.Clock`, so replay (`speed: 0`) gives the same results however fast it runs. Never call `time.Now()` in correlation logic.
- **Operator changes go through the daemon** (ADR 0009):
  - covers the kill switch, scans and host descriptions (a `config reload` stores nothing: it swaps the running configuration and is logged);
  - the API hands the change to the correlator with `Bus.PublishOperator`, then answers once it is committed;
  - the CLI and the API never write the database themselves.
- **Online and offline reads are the same queries.** `store.Reader` serves both the API and `--offline`. A new read goes into `Reader`, never into a handler.
- **The correlator assigns row ids itself**, so it can refer to rows the writer has not committed yet.
- **Evidence is typed** (`DATA_MODEL.md` §2). A frame from a MAC, an address claimed at a MAC (ARP, NDP, a REACHABLE neighbour), a DHCP server's assertion, an advertised name or address, an endpoint that answered, and a cached or last-known fact are different evidence. Only the first two and an answered probe refresh presence. A DHCP ACK about a client, a STALE neighbour entry, a DNS answer, a TCP TIMEOUT or a local probe failure never do, and silence is never read as offline.
- **DHCP provenance.**
  - A server's reply (`passive_dhcp_server`, the whole message in `Observation.DHCP`) is kept apart from a client's own claim (`passive_dhcp`). An ACK is the server's statement about a client: it goes to the lease history, never binds an address or refreshes presence, and a relayed one may describe a client on another link. `passive_dhcp_lease` is LAN Sentinel's form, still read from replay files.
  - The server identity is option 54. If it is missing or invalid, the identity is unknown and never allowlisted.
  - With no allowlist, servers are `unchecked`, never authorised. The allowlist is not authentication.
- **Reload is all or nothing.**
  - These keys reload: probes, each interface's `active` and `dhcp`, intervals, budgets, thresholds, identity settings (but `oui_override`) and the log level.
  - These keep their running values and are listed as needing a restart: adding, removing or renaming interfaces, their passive settings and `passive` (compiled into the BPF filter), `storage`, `api`, `metrics`, `logging.format`, `replay`, `identity.oui_override` and the concurrency caps (`active.max_concurrent_jobs`, `active.budgets.tcp.max_concurrent_per_*`) (`ARCHITECTURE.md` §6).
  - The merged configuration is validated again before it applies.
- **The events catalogue and the schema match.** `internal/events` must list exactly the types in the newest migration's CHECK (`TestCatalogueMatchesSchema`).
- **Identical observations are suppressed** for 2 minutes per interface (`capture.Decoder`, shared by live capture and pcap replay). Presence (ACTIVE is 5 minutes) depends on that window.

## 7. Coding standards

- Go 1.26+, standard layout. cobra for the CLI. `log/slog` for logging. `context` and `errgroup` for concurrency: every goroutine stops when its context is cancelled.
- `net/netip` for addresses, `net.HardwareAddr` for MACs.
- Wrap errors with `%w`. No panics outside `main`.
- Config: schema version 2, YAML + `LAN_TRACKER_*` env + flags, with strict validation (unknown and removed keys are errors). `LAN_SENTINEL_*` variables are refused, never read.
- Tests are table-driven:
  - decoders are tested against frames built with gopacket (`test/frames`), and every decoder has a `go test -fuzz` target;
  - the correlator is tested with golden observation streams (`test/golden`);
  - platform backends and probes are tested in Docker (`test/net`).
- Coverage of `internal/` (from every test package, `-coverpkg`) stays at or above 90%. Test behaviour (error paths, retries, validation), not lines.
- Write like the surrounding code: comment density, naming and idiom. Comments explain why and cite the doc section or rule.
- Keep it simple. The maintainer prefers the smallest change that meets the requirement; do not add configurability, abstraction or options nobody asked for.

## 8. Testing: what to run

Every Go command runs in the Linux dev container through `make`. **Docker must be running.** The first run builds the image `lan-tracker-dev:<hash of build/dev.Dockerfile>`.

```bash
make              # list all targets (make help)
make check        # fast gate: fmt-check, tidy-check, vet, lint, vuln, tests with race + coverage >= 90%
make check-all    # check + test-net + test-systemd
make build        # bin/lan-tracker for the Docker host's architecture
make release      # static linux/amd64 + linux/arm64 into dist/ with checksums
make package      # release tarballs per target into dist/release/ (VERSION=vX.Y.Z)
make test         # unit and golden tests with -race (PKGS=... to narrow)
make coverage     # tests with coverage of internal/ (coverage.out), fails below 90%; make cover opens the report
make fuzz         # every Fuzz* target, FUZZTIME each (default 30s)
make soak         # 90 simulated days of a 50-host LAN: database size, memory, CPU (about 6 minutes)
make test-net     # network integration tests in Docker (CAP_NET_RAW only)
make test-systemd # daemon under systemd in containers (Debian 11, 12, 13) with the deploy/ unit
make run-dev      # daemon in the dev container on deploy/config.dev.yaml (replay)
make tools        # tools/capcheck for linux/amd64 + linux/arm64 (privilege check for boards)
make oui          # regenerate the IEEE OUI registry and the embedded table (each release)
make fixtures     # regenerate the synthetic pcap fixtures in test/fixtures
make fmt / tidy   # fix formatting / go.mod
make shell        # shell in the dev container
```

| You changed | Run |
| --- | --- |
| Any Go code (before you call it done) | `make check`: fmt-check, tidy-check, vet, lint, vuln, race tests, coverage ≥ 90% |
| One package, while iterating | `make test PKGS=./internal/store/...` or `make test PKGS='-run TestHostsSet ./internal/cli/'` |
| A decoder | `make check`, then `make fuzz FUZZTIME=1m` (or a focused `-fuzz` run in `make shell`) |
| `internal/platform`, `internal/service`, probes on the wire, `test/net` | `make check-all` (adds `test-net` and `test-systemd`) |
| `deploy/`, the unit, `deploy/README.md` install block, `tools/capcheck` | `make test-systemd` (and `make check-all`) |
| The correlator, store retention or compaction | `make check`; consider `make soak` (about 6 minutes) for growth and memory |
| `test/fixtures/fixtures.go` | `make fixtures`, then `make check` |
| A dependency | `make tidy`, `make check` |

Notes:

- `make` alone lists every target.
- CI (`.github/workflows/ci.yml`) runs `make mod-verify check`, `make fuzz`, `make test-net`, `make test-systemd` and `make package`.
- The race detector needs cgo inside the container; the Makefile sets that, and the product stays cgo-free.

## 9. Working rules for agents

- **Ask rather than guess** when a requirement is unclear or a decision is missing. Record the answer in the relevant doc.
- **Work from the plan.** Follow `docs/IMPLEMENTATION_PLAN.md`; tick a checkbox only when the task is done and its tests pass. A phase is done only when its exit criterion is demonstrably met.
- **Keep docs in sync in the same change** (§12).
- **Do not commit, push, tag or release unless the user asks.** Never force-push `main`. Releases are cut by the user from the `release` workflow.
- **Do not edit generated artifacts by hand** (§11).
- **Report results faithfully.** If a gate fails, say which, with its output. Do not lower `COVER_MIN`, skip tests or add `//nolint` to get a gate green without the user's agreement.
- **Stay in scope.** Do not refactor unrelated code. Do not start new features while doing documentation or maintenance work.
- **Do not re-propose rejected ideas** listed in `docs/STATUS.md` without new evidence.
- **Hardware and site checks are the user's** (`docs/TEST_PLAN.md`). Do not tick their boxes from Docker results.

## 10. Common tasks

Which files each task touches: `docs/AI_REPO_MAP.md` (Common tasks).

**Add or change a passive decoder**

1. Read `ARCHITECTURE.md` §3 (Passive capture) and `DATA_MODEL.md` §2. Decide the MAC evidence and the `meta` keys first.
2. Write it in `internal/collect/capture/decoders` as a pure function of the frame; it never logs.
3. If it needs frames the BPF filter drops, extend `capture/filter.go` and its tests.
4. Add table tests on frames built in `test/frames`, plus a fuzz target in `decoders/fuzz_test.go`.
5. A new protocol needs a `passive.protocols` key (config, validation, docs).
6. If the synthetic site capture should show it, extend `test/fixtures/fixtures.go`, run `make fixtures`, and update the capture scenario expectations in `test/golden`.

**Add a CLI command**

1. Specify it in `docs/CLI.md` first: syntax, output, exit codes.
2. Reads: add the query to `store.Reader`, an endpoint in `internal/api` with a client method, and wire the CLI `backend` so it works online and `--offline`.
3. Writes: go through the daemon (an operator endpoint, `api.Control`, a `Bus.PublishOperator` message handled in `internal/correlate`); offline refuses.
4. Register it in `internal/cli/root.go`. Test all output formats, exit codes and the offline path. Update `docs/API.md`.

**Change the schema**

1. Add the next `migrations/NNNN_description.sql`. Never edit a migration that is on `main`.
2. Update `DATA_MODEL.md` §4 (the heading lists every migration) and the models and queries in `internal/store`.
3. Changing the event types means rebuilding `events`, because a CHECK constraint cannot be altered:
   - copy the pattern of `0007_host_description.sql`;
   - name the new table `events_rebuilt` (`events_new` is already an index name from 0002);
   - recreate **all nine** indexes on `events`.
4. Then update `internal/events` and `DATA_MODEL.md` §7. Add the CLI type name in `CLI.md` (Event type names) if users filter on it.
5. Tests: `TestCatalogueMatchesSchema`, the migration tests in `internal/store`, a previous-version database upgrading.

**Add an event type**

1. Read the schema steps above.
2. Emit it from `internal/correlate` only, with its evidence snapshot.
3. Give it a journald priority (`ARCHITECTURE.md` §7).
4. Events are state transitions: never one per observation.

**Add a config key**

1. Add it to `internal/config` (`types.go`, `defaults.go`, `validate.go`) with strict validation.
2. Decide whether it reloads. If it does not, it must show up in the reload answer as needing a restart (`config.Diff` in `summary.go`, the daemon's `reload`).
3. Update `ARCHITECTURE.md` §6, `internal/config/testdata/architecture-example.yaml`, and `deploy/config.yaml` if operators should see it. `TestDeployConfigs` keeps the deploy configs valid.

**Fix or extend an existing active probe engine** (ARP, ICMP, TCP, the DNS and mDNS name queries)

1. Every send goes through `probe.Budget` (policy, kill switch, budgets, spacing) and the platform `Transmitter`.
2. Engines only emit observations.
3. Keep `scan plan` (`probe.Compute`) consistent with what `scan run` sends.
4. Run `make check-all`: `TestActiveDiscovery` measures budgets on the wire.
5. **A new probe type, or any probe that sends a payload the contract does not already name**, changes the safety contract (rules 7 and 11, ADR 0012). Identification probes were removed on purpose: do not add one. Anything else needs a recorded decision before any code, interface or stub.

## 11. Generated artifacts: do not edit by hand

| Path | Produced by | Notes |
| --- | --- | --- |
| `data/oui/oui.tsv.gz`, `data/oui/oui.bin` | `make oui` (downloads the IEEE registries) | Refreshed each release; a test keeps the two in step |
| `test/fixtures/*.pcapng` | `make fixtures` from `test/fixtures/fixtures.go` | Edit the Go definition, then regenerate |
| `go.sum` | `make tidy` | |
| `bin/`, `dist/`, `.build/`, `dev/`, `coverage.out`, `coverage.html` | build, test and `run-dev` outputs | Git-ignored; never commit |
| Release tarballs and `SHA256SUMS` | `make package`, the `release` workflow | Published on GitHub Releases only |

## 12. Keeping docs in sync

| Change | Update in the same change |
| --- | --- |
| CLI syntax or output | `CLI.md`; `README.md` examples if affected |
| API endpoint or metric | `API.md`; `ARCHITECTURE.md` §3 (Metrics) for a metric |
| Schema or migration | `DATA_MODEL.md` §4 (heading and tables) |
| Event type | `DATA_MODEL.md` §7, `CLI.md` (Event type names) |
| Config key or reload behaviour | `ARCHITECTURE.md` §6, the example config, `deploy/config.yaml` |
| Unit, install or privileges | `ARCHITECTURE.md` §8, `deploy/README.md`, `tools/capcheck/README.md` |
| A decision | `REQUIREMENTS.md` §5 (with the date); an ADR if foundational |
| Task progress | `IMPLEMENTATION_PLAN.md` checkbox; `STATUS.md` |
| New package or moved responsibility | `ARCHITECTURE.md` §4, §3 of this file, `docs/AI_REPO_MAP.md` |

Style used throughout the docs:

- British English (behaviour, neighbour).
- Dates written as "7 Oct 2026".
- Plain sentences; tables for reference material.
- Requirement ids `FR-<AREA>-n` / `NFR-<AREA>-n`. Never reuse or renumber an id; check with `grep` before you add one.

## 13. Avoiding conflicts

- **Migrations are append-only.** Take the next free number. Never edit, rename or renumber one that has reached `main`: existing databases never run it again, and an older binary refuses a newer schema. Two branches adding a migration must renumber before merging.
- **Requirement ids, event types and metric names are public.** Add; do not rename. A rename is a decision for the user.
- **Released behaviour that operators rely on** needs an entry in `deploy/README.md` (upgrade notes) when it changes: install paths, the unit, config keys, CLI output used in scripts.
- **Edit docs in place.** Do not reflow or reformat whole files; it makes merges of parallel work painful.
- **`deploy/README.md` and `tools/capcheck/README.md` are executed by tests.** Keep their fenced blocks runnable, and run `make test-systemd` after editing them.
