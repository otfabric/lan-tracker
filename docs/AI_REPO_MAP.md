# LAN Tracker — Repository map

Where each concern lives, what to read before changing it, and which tests cover it. This lets an agent open the few files a task needs instead of searching the tree. The rules are in [`AGENTS.md`](../AGENTS.md) and the task recipes in [`COMMON_TASKS.md`](COMMON_TASKS.md); this page only answers "where".

## Package map

| Path | Responsibility |
| --- | --- |
| `cmd/lan-tracker` | `main`: hands off to `internal/cli` |
| `internal/cli` | cobra commands. A `backend` interface serves both online (API client) and `--offline` (read-only DB) reads; table, JSON, JSONL and CSV output |
| `internal/api` | REST server on the Unix socket (plus an optional `127.0.0.1` listener with `/metrics`) and the CLI's client. Operator endpoints go through `Control` |
| `internal/daemon` | Lifecycle: config, store, pipeline, SIGHUP and `config reload`, sd_notify and watchdog, shutdown; implements `api.Control` |
| `internal/config` | Schema version 1 (`config.go`; value types in `types.go`), defaults, YAML + env + flags loading, strict validation, the reload diff (`summary.go`) |
| `internal/observation` | The `Observation` type, sources, evidence kinds (`Evidence()`) and meta keys, the decoded DHCP message (`DHCPMessage`), JSONL encoding, the bounded bus with barriers and operator messages |
| `internal/correlate` | **The only writer of state and events**: host matching, bindings, conflicts, presence, name records, TCP endpoints, DHCP servers, exchanges and leases, the proxy-ARP flag, operator actions |
| `internal/events` | The event catalogue (it must match the migration CHECK) and the engine that writes events to the table and journald |
| `internal/store` | SQLite: migrations, the single writer, batches, compaction, the integrity check, and `Reader` (queries shared by the API and `--offline`); `storetest` seeds test databases |
| `internal/platform` | **The only code that touches AF_PACKET, netlink or raw sockets**: `Capturer`, `NeighborSource`, `InterfaceMonitor`, `Transmitter` (frames, TCP connect, UDP for name queries, ICMP, the kernel route lookup), `ClockSource` (read-only `adjtimex`); fakes in `platform/fake` |
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
| `test/golden` | Golden correlator scenarios, including reconstruction (`DATA_MODEL.md` §10) and the CLI end-to-end tests |
| `test/frames`, `test/fixtures` | gopacket frame builders; the synthetic pcap fixture and its generator |
| `test/net` | Docker network tests (tag `nettest`, `make test-net`), run as uid 65534 with only `CAP_NET_RAW` |
| `test/systemd` | The daemon under systemd on Debian 11/12/13 (`make test-systemd`) |
| `test/crash`, `test/soak` | kill -9 during writes (in `make check`); 90 simulated days (tag `soak`, `make soak`) |
| `tools/capcheck` | Privilege and feasibility check for boards; shipped in the release tarballs |
| `build/` | The dev container (`dev.Dockerfile`) and the scripts make runs in it |
| `deploy/` | Reference unit, default config, the reviewed ARP + TCP example (`config.active-example.yaml`), replay dev config, install guide |

## Concerns

| Concern | Code | Read first | Tests |
| --- | --- | --- | --- |
| Host identity, bindings, conflicts, presence, evidence kinds | `internal/correlate` (`rules.go`, `state.go`, `correlate.go`); `Observation.Evidence()` in `internal/observation` | `DATA_MODEL.md` §2, §3, §5.1–§5.3, §6 | `internal/correlate/correlate_test.go`; `test/golden` (`TestScenarios`, reconstruction) |
| Name records and `preferred_name` | `internal/correlate/names.go`, `decoders/dns.go` | `DATA_MODEL.md` §5.4 | `internal/correlate/names_test.go` |
| Proxy ARP and duplicate IP | `internal/correlate/proxyarp.go`, `rules.go` | `DATA_MODEL.md` §5.2; `REQUIREMENTS.md` §5 (Proxy ARP vs duplicate IP) | `proxyarp_test.go`; `test/golden` |
| TCP endpoints and their result history | `internal/correlate/endpoints.go` | `DATA_MODEL.md` §5.5; `REQUIREMENTS.md` FR-AC-5 | `internal/correlate/probes_test.go` (`TestEndpoints*`) |
| Collection gaps (`OBSERVATION_GAP`) | `internal/correlate/gaps.go` | `DATA_MODEL.md` §7; `ARCHITECTURE.md` §3 (Correlator) | `internal/correlate/gaps_test.go` |
| Operator actions (kill switch, scans, descriptions) | `internal/correlate/probes.go` | `DATA_MODEL.md` §5.8, §8; ADR 0009 | `internal/correlate/probes_test.go`, `internal/daemon/*_test.go` |
| DHCP messages, servers, exchanges and leases | `decoders/dhcp.go`, `internal/observation/dhcp.go` (`DHCPMessage`), `internal/correlate/dhcp.go`, `internal/store/dhcp.go`, `internal/config` (`InterfaceDHCP`) | `DATA_MODEL.md` §5.6; `REQUIREMENTS.md` FR-PA-3, FR-PA-7 | `correlate/dhcp_test.go`, `correlate/dhcp_lease_test.go`, `store/dhcp_test.go`, `test/golden/capture_test.go` |
| OUI registrant lookup (a hint, not identification) | `internal/identify` (`oui.go`), `internal/ouitable`, `data/oui` | ADR 0011; `DATA_MODEL.md` §5.7 | `identify/oui_test.go`, `ouitable` tests |
| Events and journald | `internal/events`; emitted from `internal/correlate` | `DATA_MODEL.md` §7; `ARCHITECTURE.md` §7 | `events_test.go` (`TestCatalogueMatchesSchema`) |
| Observation type, bus, operator messages | `internal/observation` | `DATA_MODEL.md` §2; `ARCHITECTURE.md` §3 (Observation bus) | `internal/observation/*_test.go` |
| Passive capture and BPF filter | `internal/collect/capture` (`capture.go`, `filter.go`, `decoder.go`) | `ARCHITECTURE.md` §3 (Passive capture) | `capture_test.go`; `test/net/capture_test.go` |
| Frame decoders | `internal/collect/capture/decoders` | `ARCHITECTURE.md` §3; `DATA_MODEL.md` §2 (meta keys) | `decoders_test.go`, `edge_test.go`, `fuzz_test.go` |
| Neighbour table | `internal/collect/neighbor`, `platform/netlink.go` | `ARCHITECTURE.md` §3 (Neighbour collector) | `neighbor` tests; `test/net/netlink_test.go` |
| Interfaces and contexts | `internal/iface`, `platform/netlink.go` | `ARCHITECTURE.md` §3 (Interface manager) | `iface` tests; `test/net/netlink_test.go` |
| Replay | `internal/collect/replay`, `internal/clock` | `ARCHITECTURE.md` §3 (Replay collector) | `replay` tests; `test/golden/capture_test.go` |
| Kernel access (capture, netlink, transmit, `Route`, `ListenUDP`) | `internal/platform` (`afpacket.go`, `netlink.go`, `transmit.go`); fakes in `platform/fake` | `ARCHITECTURE.md` §3 (Platform layer), §8 | `platform` tests; `test/net` |
| Active probe safety (start budget, `Simulate`, policy, kill switch, back-off, plan) | `internal/probe` (`budget.go`, `policy.go`, `engine.go`, `plan.go`) | `ARCHITECTURE.md` §3 (Active probes), §5; ADR 0011 | `budget_test.go`, `policy_test.go`, `plan_test.go`; `test/net/active_test.go` (`TestActiveDiscovery`) |
| Probe engines (ARP, ICMP, TCP with route check) | `internal/probe/{arp,icmp,tcp}`; helpers in `internal/probe/probetest` | `ARCHITECTURE.md` §3, §5 | per-engine tests on `platform/fake`; `test/net/active_test.go` |
| Name queries (mDNS, unicast DNS) | `internal/probe/names`, `internal/probe/scheduler` (`nameQueries`), `store/query.go` (`Reader.NameTargets`) | `ARCHITECTURE.md` §3 (Active probes), §5; `REQUIREMENTS.md` FR-AC-12 | `probe/names/names_test.go`, `store/probes_test.go` (`TestNameTargets`); `test/net/names_test.go` (`TestNameQueriesOnTheWire`) |
| Scheduling and operator scans | `internal/probe/scheduler`, `internal/netrange` | `ARCHITECTURE.md` §3 (scheduler paragraph) | `scheduler_test.go`, `netrange` tests |
| Storage, writer, compaction, integrity | `internal/store` (`store.go`, `compact.go`, `integrity.go`, `migrate.go`) | `DATA_MODEL.md` §4, §9; `ARCHITECTURE.md` §3 (Store) | `store_test.go`, `compact_test.go`, `integrity_test.go`; `test/crash`; `make soak` |
| Read model (API and `--offline`) | `internal/store/reader.go`, `query.go`, `model.go` | `CLI.md` §1, §4; `DATA_MODEL.md` §4 (Point-in-time query) | `reader_test.go`; `test/golden/cli_test.go` |
| Schema | `migrations/NNNN_*.sql` (`0001_init.sql` holds the whole initial schema) | `DATA_MODEL.md` §4; `COMMON_TASKS.md`; `AGENTS.md` §13 | `store_test.go` (migrations, constraints) |
| REST API | `internal/api` (`api.go` reads, `active.go` operator endpoints, `client.go`) | `API.md` | `api_test.go`, `active_test.go`, `fail_test.go` |
| CLI | `internal/cli` (`root.go`, `hosts.go`, `lists.go`, `describe.go`, `configcmd.go`, `status.go`, `output.go`, `backend.go`) | `CLI.md` | `cli_test.go`, `read_test.go`, `formats_test.go`, `describe_test.go`, `active_test.go` |
| Daemon lifecycle, reload | `internal/daemon` (`daemon.go`, `pipeline.go`, `server.go`, `active.go`) | `ARCHITECTURE.md` §6; `REQUIREMENTS.md` FR-CFG-3 | `daemon_test.go`; `test/systemd` |
| Configuration (schema 1) | `internal/config` (`config.go`, `types.go`, `defaults.go`, `validate.go`, `load.go`, `summary.go`) | `ARCHITECTURE.md` §6 | `config_test.go`, `fuzz_test.go`; `TestDeployConfigs` |
| Metrics | `internal/metrics`; values gathered in `internal/daemon` | `API.md` (Metrics); AGENTS.md rule 9 | `metrics_test.go` |
| systemd, sd_notify, journald | `internal/service`, `deploy/lan-tracker.service` | `ARCHITECTURE.md` §8 | `service` tests; `test/systemd/run.sh` |
| Install, upgrade, rollout | `deploy/README.md` (its install block is run by `test/systemd/run.sh`) | `deploy/README.md` | `make test-systemd` |
| Board privilege audit | `tools/capcheck` (its README audit block is run by `test/systemd/run.sh`) | `tools/capcheck/README.md`; `ARCHITECTURE.md` §8 | `make test-net`, `make test-systemd` |
| Build, CI, release | `Makefile`, `build/*.sh`, `.github/workflows/{ci,release}.yml` | `ARCHITECTURE.md` §4 (Tooling); `README.md` (Releases) | CI itself |
| Chaos and parallel suites | `internal/correlate/chaos_test.go`, `internal/daemon/chaos_test.go`, `internal/daemon/parallel_test.go` | `ARCHITECTURE.md` §9 (Testing strategy) | short runs in `make check`; `make chaos` at length |
| User guide | `docs/USER_GUIDE.md` (examples from a replayed scenario, the unit under systemd and a Docker test network) | `CLI.md` | regenerate the output when commands or output change |
| Hardware validation | `docs/TEST_PLAN.md` | `ROADMAP.md` (open hardware and field checks) | manual, on boards |

## Common tasks: files to touch

The steps are in [`COMMON_TASKS.md`](COMMON_TASKS.md). These are the files each task usually touches.

| Task | Files |
| --- | --- |
| Passive decoder | `internal/collect/capture/decoders/<proto>.go`, `decoders.go`, `decoders_test.go`, `fuzz_test.go`; `capture/filter.go` if new frames are needed; `test/frames`; optionally `test/fixtures/fixtures.go` + `make fixtures` + `test/golden/capture_test.go`; `ARCHITECTURE.md` §3, `DATA_MODEL.md` §2 |
| Read-only CLI command | `internal/store/reader.go` (+ `query.go`, `model.go`), `internal/api/api.go`, `internal/api/client.go`, `internal/cli/backend.go`, a command file in `internal/cli`, `root.go`; tests in `store`, `api`, `cli`; `CLI.md`, `API.md` |
| Operator (write) command | the above, plus `internal/api/active.go` (`Control`), `internal/daemon/active.go`, `internal/observation/bus.go` (`Operator` kind), `internal/correlate/probes.go`; usually a migration and an event |
| Schema change | `migrations/NNNN_*.sql`, `internal/store` models and queries, `internal/correlate/db.go` if the correlator writes it; `DATA_MODEL.md` §4 |
| Event type | migration (events rebuild), `internal/events/events.go`, emitting code in `internal/correlate`; `DATA_MODEL.md` §7, `CLI.md` (Event type names) |
| Config key | `internal/config/{config,defaults,validate,summary}.go`, `testdata/architecture-example.yaml`, `deploy/config.yaml`; the consumer; `ARCHITECTURE.md` §6 |
| Metric | `internal/metrics` (label allow-list), gathering in `internal/daemon`; `API.md`, `ARCHITECTURE.md` §3 (Metrics) |
| Probe engine fix | `internal/probe/<engine>` (`arp`, `icmp`, `tcp`, `names`), `internal/probe` (budget, policy, plan), `internal/probe/scheduler`; `test/net/active_test.go`, `test/net/names_test.go`; `ARCHITECTURE.md` §3, §5 |
| Unit or install change | `deploy/lan-tracker.service`, `deploy/README.md`, `tools/capcheck/README.md`, `test/systemd/run.sh`; `ARCHITECTURE.md` §8 |
