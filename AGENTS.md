# AGENTS.md — OT Fabric LAN Tracker

This file is the entry point for every AI coding agent working in this repository, whatever the tool. Read it before you change anything. The tool-specific files (`CLAUDE.md`, `.github/copilot-instructions.md`, `.cursor/rules/`) point here and add only notes about their own tool. The hard rules (§4) are numbered; code and docs cite them as "AGENTS.md rule N".

## Organisation standards

This repository follows **otfabric/engineering policy 1.1**, profile **application** (`.otfabric/policy.yaml`, mode `enforce`). Organisation skills come from the `otfabric` plugin (maintainer's user settings). Local exceptions: `.otfabric/exceptions.yaml`. Where this file and the organisation standards disagree, this file wins for this repository — say so in your report.

## 1. What this is

OT Fabric LAN Tracker keeps an evidence-based history of which MAC addresses, IP addresses, names and TCP endpoints are visible on each local OT (industrial) network, so operators can find equipment, troubleshoot connectivity and reconstruct incidents. It answers, after the fact: *which MAC held this IP at that time, which other addresses and names it had, what DHCP told it, whether the configured TCP ports answered, and what the collector could not see*.

**It is a connectivity tracker with history, not an identification product** ([ADR 0011](docs/adr/0011-connectivity-tracker-scope.md)). It does not determine equipment models, operating systems, firmware or physical asset identity; the operator's description on a MAC is the only statement about what a device is. Do not add identification probes, classifiers or confidence ranking under any name.

It is one static Go binary, `lan-tracker`, which is the daemon, the CLI client, the database inspector and the on-demand scanner. It runs under systemd on Linux edge devices (Raspberry Pi CM4, RevPi Connect, amd64 edge boxes).

Data flows one way: collectors and probes emit **observations** onto a bus. A single **correlator** goroutine turns them into hosts, bindings and events. A single SQLite **writer** commits them. The REST API on a Unix socket, the CLI and Prometheus read the result.

What is not built yet: [`docs/ROADMAP.md`](docs/ROADMAP.md). How operators use it: [`docs/USER_GUIDE.md`](docs/USER_GUIDE.md). Where things live: [`docs/AI_REPO_MAP.md`](docs/AI_REPO_MAP.md).

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
| `docs/USER_GUIDE.md` | How operators install, configure and use what is built |
| `TESTING.md` | How to run each test tier, prerequisites, coverage policy, replaying failures |
| `docs/TEST_PLAN.md` | The manual hardware test procedure for the boards |
| `docs/adr/` | Why the foundational decisions were taken, and what they rule out |
| `docs/AI_REPO_MAP.md` | The package map; where each concern lives and which tests cover it |
| `docs/COMMON_TASKS.md` | Recipes for recurring changes; which docs each change updates |
| `docs/ROADMAP.md` | The only list of open work: everything not implemented, deferred or blocked, open hardware checks, open questions and rejected proposals |
| `deploy/README.md` | Install, upgrade, rollback and rollout; `make test-systemd` runs its install block verbatim |

**Precedence when sources disagree**

1. The user's explicit instruction in the current task.
2. The hard rules in §4.
3. Recorded decisions: `REQUIREMENTS.md` §5 and the ADRs.
4. The specifications: `REQUIREMENTS`, `DATA_MODEL`, `CLI`, `API` and `ARCHITECTURE`.
5. `ROADMAP.md` and `USER_GUIDE.md`.
6. The code and tests.
7. Code comments.

**If code and docs disagree, stop and ask.** Do not silently "fix" either side.

**Do not infer architecture from the implementation.** If the specifications do not cover a behaviour, ask before you invent one. When you get an answer, record it in the relevant document: a new row in `REQUIREMENTS.md` §5 with the date, and an ADR if the decision is foundational.

## 3. Package map

Every package and top-level directory, with its responsibility: [`docs/AI_REPO_MAP.md`](docs/AI_REPO_MAP.md) (Package map).

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
   - Every active send (ARP request, ICMP echo, DNS or mDNS query, TCP connect) takes one start from the shared budget (`probe.Budget`): the global start rate, the per-protocol rates, the concurrency caps, the TCP caps, target spacing, excludes and the kill switch. The rate contract is probe starts per second, not packets on the wire (ADR 0011).
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
11. **Out of scope** (ADR 0011).
    - Device identification of any kind: payload probes, banner reading, passive OS/device/vendor classification, confidence ranking, fingerprint databases, and running `modbusctl`, `sunspecctl`, `otprobe` or any external tool from the daemon. Do not build, scaffold or stub it.
    - VLAN tagging, a web UI, any data leaving the box (fleet aggregation, remote API), alerting, automatic network repair, continuous packet recording, DHCPv6, a DHCP client or server.
    - Do not build, design, scaffold or stub these without an explicit request and a recorded decision.
12. **IPv4-first.** v1 active discovery is IPv4, and ARP is the primary LAN discovery mechanism. IPv6 active probing (NDP solicitation) is not built; it waits on a request (`docs/ROADMAP.md`). Passive IPv6/NDP decoding stays.

## 5. Read this before editing

| Before you touch | Read | Because |
| --- | --- | --- |
| `internal/correlate`, `test/golden` | `DATA_MODEL.md` §3, §5–§7, §10 | Interval semantics, correlation rules and the event catalogue; the reconstruction scenario must always pass |
| `internal/store`, `migrations/` | `DATA_MODEL.md` §4, §9; §13 below | Constraints, indexes, retention; migrations on `main` are immutable |
| `internal/probe/**` | `ARCHITECTURE.md` §3 (Active probes) and §5; rules 7, 11, 12 | The safety envelope: every send passes the budget, policy and kill switch |
| `internal/platform` | `ARCHITECTURE.md` §3 (Platform layer) and §8 | Privilege table; must work with only `CAP_NET_RAW` |
| `internal/collect/capture` | `ARCHITECTURE.md` §3 (Passive capture), `DATA_MODEL.md` §2 | MAC-evidence rules, own-frame exclusion, refresh suppression, DHCP provenance |
| `internal/config` | `ARCHITECTURE.md` §6, `REQUIREMENTS.md` FR-CFG-* | Strict keys; which keys reload and which need a restart |
| `internal/cli`, `internal/api` | `CLI.md`, `API.md` | Public contract, exit codes, the offline-access contract (`CLI.md` §1) |
| `internal/identify` | ADR 0011, `DATA_MODEL.md` §5.7 | The OUI is a registrant hint only; there are no classifiers to extend |
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
  - A server's reply (`passive_dhcp_server`, the whole message in `Observation.DHCP`) is kept apart from a client's own claim (`passive_dhcp`). An ACK is the server's statement about a client: it goes to the lease history, never binds an address or refreshes presence, and a relayed one may describe a client on another link.
  - The server identity is option 54. If it is missing or invalid, the identity is unknown and never allowlisted.
  - With no allowlist, servers are `unchecked`, never authorised. The allowlist is not authentication.
- **Reload is all or nothing.**
  - These keys reload: probes, each interface's `active` and `dhcp`, scan profiles, probe intervals, budgets, thresholds, identity settings (but `oui_override`) and the log level.
  - These keep their running values and are listed as needing a restart: adding, removing or renaming interfaces, their passive settings and `passive` (compiled into the BPF filter), `storage`, `api`, `metrics`, `logging.format`, `replay`, `identity.oui_override`, `neighbor.resync_interval` and the concurrency caps (`active.max_concurrent_jobs`, `active.budgets.tcp.max_concurrent_per_*`) (`ARCHITECTURE.md` §6).
  - The merged configuration is validated again before it applies.
- **The events catalogue and the schema match.** `internal/events` must list exactly the types in the newest migration's CHECK (`TestCatalogueMatchesSchema`).
- **Identical observations are suppressed** for 2 minutes per interface (`capture.Decoder`, shared by live capture and pcap replay). Presence (ACTIVE is 5 minutes) depends on that window.

## 7. Coding standards

- Go 1.26+, standard layout. cobra for the CLI. `log/slog` for logging. `context` and `errgroup` for concurrency: every goroutine stops when its context is cancelled.
- `net/netip` for addresses, `net.HardwareAddr` for MACs.
- Wrap errors with `%w`. No panics outside `main`.
- Config: schema version 1, YAML + `LAN_TRACKER_*` env + flags, with strict validation (unknown keys are errors).
- Tests are table-driven:
  - decoders are tested against frames built with gopacket (`test/frames`), and every decoder has a `go test -fuzz` target;
  - the correlator is tested with golden observation streams (`test/golden`);
  - platform backends and probes are tested in Docker (`test/net`).
- Coverage of `internal/` (from every test package, `-coverpkg`) stays at or above 90%. Test behaviour (error paths, retries, validation), not lines.
- Write like the surrounding code: comment density, naming and idiom. Comments explain why and cite the doc section or rule.
- Keep it simple. The maintainer prefers the smallest change that meets the requirement; do not add configurability, abstraction or options nobody asked for.

## 8. Testing: what to run

Every Go command runs in the Linux dev container through `make`. **Docker must be running.** The first run builds the image `lan-tracker-dev:<hash of build/dev.Dockerfile>`.

[`TESTING.md`](TESTING.md) (Make targets) says what each target does.

| You changed | Run |
| --- | --- |
| Any Go code (before you call it done) | `make check`: fmt-check, tidy-check, vet, lint, vuln, race tests, coverage ≥ 90% |
| One package, while iterating | `make test PKGS=./internal/store/...` or `make test PKGS='-run TestHostsSet ./internal/cli/'` |
| A decoder | `make check`, then `make fuzz FUZZTIME=1m` (or a focused `-fuzz` run in `make shell`) |
| `internal/platform`, `internal/service`, probes on the wire, `test/net` | `make check-all` (adds `test-net` and `test-systemd`) |
| `deploy/`, the unit, `deploy/README.md` install block, `tools/capcheck` | `make test-systemd` (and `make check-all`) |
| The correlator, store retention or compaction | `make check`; `make chaos` for invariants under random streams and restarts; consider `make soak` (about 6 minutes) for growth and memory |
| The daemon, API, scheduler or anything concurrent | `make check`, then `make chaos` (the race detector, budgets and kill switch under load) |
| `test/fixtures/fixtures.go` | `make fixtures`, then `make check` |
| A dependency | `make tidy`, `make check` |

Notes:

- `make` alone lists every target.
- CI (`.github/workflows/ci.yml`) runs `make mod-verify check`, `make fuzz`, `make test-net`, `make test-systemd` and `make package`.
- The race detector needs cgo inside the container; the Makefile sets that, and the product stays cgo-free.

## 9. Working rules for agents

- **Ask rather than guess** when a requirement is unclear or a decision is missing. Record the answer in the relevant doc.
- **The docs describe what is built.** The specifications describe exactly what the code does today; anything not implemented belongs in `docs/ROADMAP.md`, never in a specification as "planned" or "not yet". When you build a roadmap item, describe it in the specifications and remove it from `ROADMAP.md` in the same change, once its tests pass.
- **Keep docs in sync in the same change** (§12).
- **Do not commit, push, tag or release unless the user asks.** Never force-push `main`. Releases are cut by the user from the `release` workflow.
- **Do not edit generated artifacts by hand** (§11).
- **Report results faithfully.** If a gate fails, say which, with its output. Do not lower `COVER_MIN`, skip tests or add `//nolint` to get a gate green without the user's agreement.
- **Stay in scope.** Do not refactor unrelated code. Do not start new features while doing documentation or maintenance work.
- **Do not re-propose rejected ideas** listed in `docs/ROADMAP.md` without new evidence.
- **Hardware and site checks are the user's** (`docs/TEST_PLAN.md`). Do not record their results from Docker runs.

## 10. Common tasks

The step-by-step recipes (add a decoder, a CLI command, a config key or an event type, change the schema, change a probe engine): [`docs/COMMON_TASKS.md`](docs/COMMON_TASKS.md). Follow the recipe when the task matches one.

## 11. Generated artifacts: do not edit by hand

| Path | Produced by | Notes |
| --- | --- | --- |
| `data/oui/oui.tsv.gz`, `data/oui/oui.bin` | `make oui` (downloads the IEEE registries) | Refreshed each release; a test keeps the two in step |
| `test/fixtures/*.pcapng` | `make fixtures` from `test/fixtures/fixtures.go` | Edit the Go definition, then regenerate |
| `go.sum` | `make tidy` | |
| `bin/`, `dist/`, `.build/`, `dev/`, `coverage.out`, `coverage.html` | build, test and `run-dev` outputs | Git-ignored; never commit |
| Release tarballs and `SHA256SUMS` | `make package`, the `release` workflow | Published on GitHub Releases only |

## 12. Keeping docs in sync

Which document each kind of change updates in the same change, and the style of the docs: [`docs/COMMON_TASKS.md`](docs/COMMON_TASKS.md) (Keeping docs in sync).

## 13. Avoiding conflicts

- **Migrations are append-only.** Take the next free number. Never edit, rename or renumber one that has reached `main`: existing databases never run it again, and an older binary refuses a newer schema. Two branches adding a migration must renumber before merging.
- **Requirement ids, event types and metric names are public.** Add; do not rename. A rename is a decision for the user.
- **Released behaviour that operators rely on** needs an entry in `deploy/README.md` (upgrade notes) when it changes: install paths, the unit, config keys, CLI output used in scripts.
- **Edit docs in place.** Do not reflow or reformat whole files; it makes merges of parallel work painful.
- **`deploy/README.md` and `tools/capcheck/README.md` are executed by tests.** Keep their fenced blocks runnable, and run `make test-systemd` after editing them.
