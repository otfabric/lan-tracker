# LAN Tracker — Roadmap

What is not done in the released code (v0.2.1, 17 Oct 2026): checks that need hardware or real sites, features that were decided against for now, known gaps in the tests and the product, and open questions. Everything the code does is specified in `REQUIREMENTS.md`, `DATA_MODEL.md`, `CLI.md`, `API.md` and `ARCHITECTURE.md`; this file is the one place for what it does not do yet.

Everything below is built and tested only in the dev container, replay and the Docker suites (`make check`, `make test-net`, `make test-systemd`, `make soak`). Nothing has run on a board, on a real network or in a pilot. When an item is closed, remove it here and record the result where it says.

## Field validation (open)

The maintainer runs these on the RevPi Connect (`linux/arm64`) and the amd64 edge box, following `TEST_PLAN.md`. Agents record the results they are given and never close these from Docker results.

| Check | What it takes | Test plan | Record in |
| --- | --- | --- | --- |
| Board audit | Board facts, install from the release tarball, `systemd-analyze security` exposure ≤ 2.5 (2.3 in the containers) and `capcheck` (every row, the `adjtimex` read included) under the reference unit with only `CAP_NET_RAW`, on each board's Debian release. A need for another capability stops the rollout for a decision (NFR-SEC-1) | A1–A4 | `ARCHITECTURE.md` §8 board table |
| Neighbour-only discovery on a real test LAN | Hosts appear from the kernel neighbour table alone, with the OUI registrant, scoped per interface (`passive: { enabled: false }`); in Docker `TestDaemonFindsHosts` covers it | B1, B2 | `TEST_PLAN.md` |
| Passive capture, names and reconstruction on hardware | Devices and names found on a lab LAN; an injected IP change and duplicate IP reconstructed through the CLI, online and `--offline`; host descriptions | B3, B4, B6, B7 | `TEST_PLAN.md` |
| DHCP on a real network | A rogue server flagged against the allowlist; exchanges and leases on a switched port (partial) and on a mirror port (complete) | B5, B8 | `TEST_PLAN.md` |
| Robustness on the boards | Reload, restart and clean stop, watchdog, link down and up, power cut with the clock unsynchronised, corrupt database quarantined | C1–C6 | `TEST_PLAN.md` |
| Upgrade and rollback between releases | Needs a second release; follows `deploy/README.md` (Upgrade and rollback). In Docker only the schema refusal tests and the install block cover it | C7 | `TEST_PLAN.md` |
| Active discovery on a lab LAN | `scan plan`, `scan run`, periodic sweeps; start budgets counted on the wire with kernel packets apart; plan within 10% of the measured run; kill switch within a second and across a restart | D1–D3 | `TEST_PLAN.md` |
| Captured-wire evidence on a real network | ARP, TCP and name queries as enabled, no UDP other than DNS and mDNS, no TCP payload; a target without a direct route not dialled (`LOCAL_ERROR`) | D4 | `TEST_PLAN.md` |
| Resource use on the boards | 7 days passive against NFR-PERF-1 (< 5% CPU, < 50 MB RSS, database on track for < 200 MB) and the unit's `MemoryMax=128M` and `CPUQuota=20%`. `make soak` on x86 does not establish board performance | E1 | `TEST_PLAN.md` |
| Passive pilot | 3 pilot sites, passive only, 7 days; inventory compared with what field support knows | F1 | `TEST_PLAN.md` |
| Real site captures | 30-minute discovery captures from pilot sites added to `test/fixtures` with decoder table tests; today only the synthetic `site-a-eth1.pcapng` exists | G1 | `test/fixtures`, decoder tests |
| Representative active pilot | 7 days on an OT rig (PLC, inverter, HMI) after passive observation, enabling ARP, ICMP and TCP/502 a step at a time; no device faults; device and network load reported | H1 | `TEST_PLAN.md` |
| Staged fleet rollout | Lab rig, pilot sites, 5% of the fleet, the whole fleet passive, then active per site; runbook in `deploy/README.md` (Staged rollout) | — | `deploy/README.md` |

## Deferred features

Decided against for now; each needs a new recorded decision in `REQUIREMENTS.md` §5 before any code, interface or stub.

- **IPv6 active probing (NDP solicitation of addresses already observed, never sweeping).** Formerly FR-AC-7; deferred because v1 active discovery is IPv4-first (decided 6 Oct 2026, AGENTS.md rule 12). It would take an NDP engine behind `probe.Budget` and the platform `Transmitter`, an addition to `scan plan`, an on-the-wire test in `make test-net` and the `capcheck` NDP row (which SKIPs today). Passive IPv6/NDP decoding is implemented.
- **DHCPv6.** Needs its own decoder and identity model. `REQUIREMENTS.md` §4 lists it as out of scope for v1.
- **Synthetic DHCP requests or leasequery.** Monitoring is receive only: a request would change server state and needs its own specification. `REQUIREMENTS.md` §4 lists a DHCP client and leasequery as out of scope.
- **More host metadata than the description.** `hosts set` / `hosts unset` take only `--description`; the form leaves room for more fields, none of which has been requested.
- **More CLI commands.** `neighbors list` (the kernel neighbour table as the daemon sees it), `interfaces show` (one interface in detail), `scan status` (a running operator scan; today `daemon status` shows the last one), `db vacuum` (offline) and `db export`. Each needs a `CLI.md` specification, a `store.Reader` query and an API endpoint (`docs/COMMON_TASKS.md`).
- **Coloured table output.** `--no-color` is accepted, but no command prints colour.
- **Recording periodic passes as scans.** The `scans.trigger` column accepts `scheduled`, but only operator scans are written; periodic passes show in `daemon status` (last pass) and metrics only.
- **The `ndp_probe` source.** Defined and accepted in replay files (handled like `passive_ndp`), but no engine emits it until IPv6 active probing exists.

## Known gaps and limitations

### Automated coverage

Scenarios that `TEST_PLAN.md` (Acceptance matrix) marks partly covered or open, plus checks that have only unit coverage:

- **TCP route and source check on a `/32` interface.** Tested with fakes only (`TestNoDirectRouteSendsNothing`, `TestPlanNoDirectRoute`); `make test-net` has no `/32` interface reaching off-subnet candidates. Field check: D4.
- **Quiet device with ICMP off and TCP/502 open.** No simulated host drops ICMP; that TCP runs without a ping answer is shown with fakes only. It would take a test-net host that drops ICMP.
- **Known MAC, unknown IP, device silent.** The parts are tested; no test asserts the scenario as a whole (last-known record with the sweep coverage).
- **Name queries with an answering responder.** `TestNameQueriesOnTheWire` checks what is sent; no resolver or mDNS responder answers in the test network. Field check: D4.
- **Same DHCP server varying options across clients.** No test gives two clients of one server different options and asserts that neither gets `DHCP_CLIENT_CONFIG_CHANGED`.
- **Operator scan overlapping periodic passes, reloads during a scan.** Covered with fake backends by the chaos suite (`TestChaosDaemon`: budgets and the kill switch checked on every start); not run together on the wire.
- **Packet loss and slow TCP listeners.** Not simulated; the retransmission envelope (`TCP_SYNCNT` 2, RST close) is asserted on the socket but not measured under loss.
- **History through compaction.** The golden scenario is replayed through a 7-day compaction; DHCP and name-record incidents are not (`TestCompactRetention` checks the tables only).
- **Schema upgrade.** Only one migration exists (`0001_init.sql`), so no upgrade from an earlier database is tested; rollback is a runbook. Field check: C7.
- **Combined stress with data pressure and clock changes.** The chaos suite (`TestChaosDaemon`, `TestChaosCorrelator`, `make chaos`) runs capture floods and loss, collector and link failures, operator actions, reloads and restarts at once; storage pressure (`TestCompactSizeCap`) and clock changes (`TestEventsRecordClockState`) are tested on their own, not in that mix.

### Product limitations

- A probe that has reserved its budget slot and is then refused at the last policy check (its interface's active discovery switched off by a reload in that moment) leaves the slot spent until it passes: under reloads every fraction of a second the budget is under-used, never exceeded.

- On a switched port capture does not see unicast DHCP replies or renewals, so leases are often partial and silence never proves there is no DHCP server or lease (FR-PA-10).
- The simulated soak reached 186.5 MB of the 200 MB budget after 90 days on a worst-case 50-host LAN (`make soak`, 17 Oct 2026; heap 5.3 MB, RSS 31.8 MB); a busier site reaches `storage.retention.max_db_size` sooner, which then shortens retention, oldest roll-ups first.
- The unit's `CPUQuota=20%` stretches CPU-heavy start-up work (parsing the text OUI registry took 6 s on a CM4, hence the precomputed table, ADR 0010). Start-up work must stay small.
- Docker's kernel is not the target kernel, so the board audits stay mandatory before relying on the privilege model in the field.

## Open questions and rejected proposals

- **Throwaway mDNS names.** On a lab LAN (6 Oct 2026) browsers announced `<uuid>.local` names during WebRTC calls, and Android phones rotated `Android_XXXXXXXX` names, which fill a host's name records. No approach has been decided. Two proposals were rejected by the maintainer and must not be proposed again without new evidence:
  - the mDNS decoder ignoring a name that is exactly a UUID plus `.local` (heuristic UUID-name suppression is ruled out);
  - leaving Android names alone and counting them first.

  Ask before changing how names are handled.

## Out of scope (not planned)

Device identification of any kind, SYN or UDP port scanning, VLAN tagging, a web UI, alerting, any data leaving the box and the rest of the list in `REQUIREMENTS.md` §4 and ADR 0011 (AGENTS.md rule 11). These are not roadmap items: do not build, design, scaffold or stub them without an explicit request and a recorded decision.
