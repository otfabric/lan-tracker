# LAN Tracker — Status

Snapshot as of **17 Oct 2026**: the migration is one pull request against `main` (`migrate/lan-tracker`), ready for v0.1.0. **No LAN Tracker release has been published yet.** The tags carried over from LAN Sentinel (v0.0.1–v0.0.8) are LAN Sentinel releases. Update this file whenever an area changes state.

`IMPLEMENTATION_PLAN.md` remains the authority for tasks and exit criteria; this page summarises it and adds what the plan does not hold: deferred work, blocked items, open questions and rejected proposals. The migration's change log is `MIGRATION.md`.

## Summary

- **The original phases 0–5** (LAN Sentinel, up to 8 Oct 2026) are built and verified in Docker and replay. Their hardware and field checks are still open. Phase 6 (identification) was removed on 16 Oct 2026 (ADR 0012).
- **Migration phases 0–4** (the brief's §12) are done: the rename, the contract, identification removed and configuration schema 2, the connectivity model, bounded discovery and naming, DHCP depth (squashed into the migration commit; `MIGRATION.md` §1–§6). The evidence is the automated tests that run in the dev container and the Docker suites; `TEST_PLAN.md` maps the brief's acceptance matrix to them.
- **Migration phase 5:** the container gates pass on the release tree (`MIGRATION.md`, Validation report); field validation is open: LAN Tracker has not run on a board, on a real network or in a pilot.
- **Maintainer actions:** merge the pull request (merge commit) and cut v0.1.0 (`minor`); `LICENSE` is MIT.

## By area

"Done" means implemented with automated tests in the dev container (and, where named, the Docker network and systemd suites). Nothing below is verified on hardware unless it says so.

| Area | Status | Notes | Validated by |
| --- | --- | --- | --- |
| Rename to `lan-tracker` (module, binary, unit, paths, env, metrics) | Done | | `make check`, `make test-systemd` |
| Identification removed (probes, UDP NTP/ENIP, classifiers, `identify run`) | Done | Legacy rows readable, nothing writes them | `TestActiveDiscovery` (no payload, no UDP on the wire), `TestLegacyIdentificationsReadable`, `TestLANSentinelUpgrade` |
| Configuration schema 2, `config convert` | Done | Version 1, removed keys and `LAN_SENTINEL_*` refused; conversion leaves active off | `TestSchemaTwoRefusesLegacy`, `TestConvertLegacy`, `TestConvertRefuses`, `TestDeployConfigs` |
| Passive capture and decoders (ARP, IPv4, IPv6/NDP, DHCPv4, mDNS, DNS, LLDP) | Done | Synthetic site capture only; no real site captures yet | `TestDecode`, `TestDecodeMalformed`, edge tests, `Fuzz*`, `test/net` capture tests, `TestCaptureScenario` |
| Typed evidence and presence | Done | Only frames, confirmed addresses and endpoint answers refresh presence | `TestEvidence`, `TestProbeResultsProvePresenceOnlyWhenAnswered`, `TestDHCPLeaseAndServerAddresses` |
| Independent addresses per MAC, conflicts, proxy ARP flag | Done | No `IP_CHANGED`; `MAC_ALSO_OBSERVED` replaces `MAC_MOVED` | `TestIPv4MultipleAddressesAreIndependent`, `TestSameIPOnTwoInterfaces`, `TestProxyARP*`, `TestScenarios` |
| TCP endpoints per address and port, exact results | Done | `LOCAL_ERROR` recorded; last definite answer kept | `TestEndpointsPerAddress`, `TestEndpointAttributionNeedsOneHolder`, `TestServices`, `TestClassify` |
| Name records (mDNS, DNS, DHCP, LLDP) | Done | Advertiser kept; proxies bind nothing | `TestNames`, `TestMDNSProxyAdvertisesAnotherDevice`, `TestMDNSAliasesGoodbyeAndCacheFlush`, `TestDNSNamesAttachWithoutPresence` |
| Correlator, golden reconstruction scenario | Done | Also passes after a simulated 7-day compaction | `TestScenarios`, `TestCLIReconstruction` |
| Store: WAL writer, retention, compaction, integrity, quarantine | Done | Soak figure (158.8 MB after 90 simulated days) is from LAN Sentinel, 7 Oct 2026; not re-run since the schema grew (migrations 0010–0013) | `test/crash` (in `make check`), `TestCompactRetention`, `make soak` |
| Legacy database upgrade (schema 0009 to 0013) | Done | IDs, descriptions, kill switch, events kept; no invented endpoint address, TTL or DHCP configuration; DHCP leases backfilled | `TestLANSentinelUpgrade`, migration tests in `internal/store` |
| API, CLI (online and `--offline`), metrics | Done | `dhcp leases`, `dhcp exchanges` added | `internal/{api,cli}` tests, `test/golden/cli_test.go` |
| Active discovery: ARP sweep, ICMP, bare TCP connect | Done in Docker | Off by default; start budgets measured on the wire | `TestActiveDiscovery` (`make test-net`) |
| Name queries (mDNS, unicast DNS) | Done in Docker | Off by default; on the wire only the mDNS group and the configured resolver are asked, within the names budget (no answering responder in the test network) | `TestMDNSQuery`, `TestDNSQueries`, `TestNameQueriesTakeTheBudget`, `TestNamePassAsksOnlyWhatIsKnown`, `TestNameQueriesOnTheWire` (`make test-net`) |
| TCP route and source checks (`RTM_GETROUTE`) | Done, fakes only | Not exercised in `make test-net` on a `/32` interface | `TestNoDirectRouteSendsNothing`, `TestPlanNoDirectRoute` |
| `scan plan` estimate (concurrency slots, phase barriers) | Done | | `TestComputePlan`, `TestSimulateMatchesPacing`, `TestActiveDiscovery` (plan within 10%) |
| Kill switch, `scan plan`, `scan run` | Done | | `TestKillSwitchPersistsAcrossRestarts`, `make test-net` |
| Config reload (`config reload`, SIGHUP) | Done | Concurrency caps reported as needing a restart | `internal/daemon` tests, `make test-systemd` |
| DHCP server monitoring | Done | Receive only, DHCPv4; `DHCP_CONFIG_CHANGED` no longer emitted | `TestDHCPServer*`, `TestCaptureScenario`, `make test-net` |
| DHCP exchanges and per-client lease history | Done | Full, partial, relayed, INFORM, NAK, DECLINE, RELEASE, option 121 gateway | `TestDHCPLeaseHistory`, `TestDHCPExchangeOutcomes`, `TestDHCPExchangeLimit`, `TestCaptureScenario` |
| Host descriptions (`hosts set` / `hosts unset`) | Done | | `internal/{cli,api,daemon,correlate,store}` tests |
| Clock-sync marking of events | Done | Read-only `adjtimex` | `make test-systemd` |
| Collection gaps in history | Done | `OBSERVATION_GAP` when a passive collector stops and recovers (or is still down at shutdown), and for the daemon's own downtime from the last heartbeat | `TestCollectorGaps`, `TestDowntimeGap`, `TestLivePipeline` |
| Release pipeline (tarballs, checksums, manual `release` workflow) | Ready for v0.1.0 | Notes from `RELEASE.md` (a version without its section is refused); MIT `LICENSE` in the tarballs; first release a `minor` bump | CI; LAN Sentinel releases v0.0.1–v0.0.8 |
| Unit hardening, root with `CAP_NET_RAW` bounding set | Done in containers | Exposure 2.3 on Debian 11, 12 and 13. A LAN Sentinel build passed on a Raspberry Pi CM4 (6 Oct 2026); LAN Tracker has not been audited on a board | `make test-systemd` |
| Board audits: RevPi Connect (CM4), Edgebox / amd64 edge box | **Pending (hardware)** | | `TEST_PLAN.md` A1–A4 |
| Neighbour-only run on a real test LAN | **Pending (hardware)** | | `TEST_PLAN.md` B1 |
| DHCP on a real network (switched port and mirror port) | **Pending (field)** | | `TEST_PLAN.md` B5 |
| Real site pcap fixtures | **Pending (field)** | | `TEST_PLAN.md` G1 |
| Captured-wire evidence of ARP, TCP and name queries on a real network, and of no identification traffic | **Pending (field)** | Docker evidence only | `TEST_PLAN.md` D2, D4 |
| Resource use on the boards (128 MB memory ceiling, CPU quota, NFR-PERF-1) | **Pending (hardware)** | Docker and x86 figures do not establish board performance | `TEST_PLAN.md` E1 |
| Passive pilot, 7 days on 3 sites | **Pending (field)** | | `TEST_PLAN.md` F1 |
| 7-day representative active pilot (PLC, inverter, HMI) | **Pending (field)** | | `TEST_PLAN.md` H1 |
| Upgrade from LAN Sentinel and rollback on a board | **Pending (hardware)** | Runbook in `deploy/README.md` | `TEST_PLAN.md` C7 |
| Staged fleet rollout | **Pending (field)** | Runbook in `deploy/README.md` | — |

## Deferred: decided, do not build

| Item | Why | Recorded in |
| --- | --- | --- |
| IPv6 active probing (NDP solicitation) | v1 is IPv4-first; passive IPv6/NDP decoding stays | `REQUIREMENTS.md` §5 (IPv4-first), AGENTS.md rule 12 |
| DHCPv6; synthetic DHCP requests; leasequery | DHCPv6 needs its own decoder and identity model; requests would change server state | `REQUIREMENTS.md` §4, §5 (DHCP server monitoring, DHCP depth) |
| VLAN tagging, web UI, any data leaving the box | Out of scope | `REQUIREMENTS.md` §4, AGENTS.md rule 11 |

## Removed: do not rebuild

Device identification of any kind (ADR 0012, 16 Oct 2026): the identification probes (Modbus FC 43/14, HTTP, TLS, SNMPv2c, SSH banner, Telnet, FTP, and the unspecified `ssh-kex`), the UDP NTP and EtherNet/IP probes, the passive classifiers and `identify run`. `MIGRATION.md` §2 lists what went.

## Blocked on the maintainer

- The hardware and field checks above. The maintainer runs `TEST_PLAN.md` on the boards; agents record the results they are given (`ARCHITECTURE.md` §8 table, plan checkboxes) and never tick these from Docker results.
- Merging the migration pull request (with a merge commit, so LAN Sentinel's history stays) and cutting v0.1.0 from the `release` workflow (`minor`).

## Open questions and rejected proposals

- **Throwaway mDNS names.** On a lab LAN (6 Oct 2026), browsers announced `<uuid>.local` names during WebRTC calls, and Android phones rotated `Android_XXXXXXXX` names.
  - Two proposals were rejected by the maintainer:
    - the mDNS decoder ignoring a name that is exactly a UUID plus `.local`;
    - leaving Android names alone and counting them first.
  - No approach has been decided; the brief also rules out heuristic UUID-name suppression. Do not re-propose either. Ask before changing how names are handled.

## Known caveats

- On Debian 11 (systemd 247) the unit runs without `PrivateIPC=`, and the journal shows a warning. This is expected.
- On a switched port, capture does not see unicast DHCP replies or renewals, so leases are often partial and silence never proves there is no DHCP server or lease.
- Docker's kernel is not the target kernel, so the board audits stay mandatory.
- The unit's `CPUQuota=20%` stretches CPU-heavy start-up work. Parsing the text OUI registry took 6 s on a CM4 under it, which is why the table is precomputed (ADR 0010). Keep start-up work small.

## Next tasks

1. Maintainer: merge the migration pull request and cut v0.1.0 (`minor`).
2. Maintainer: decide whether to archive or redirect `boeboe/lan-sentinel` to this repository.
3. Maintainer: run `TEST_PLAN.md` on the RevPi Connect and the amd64 edge box, then the passive pilot (3 sites, 7 days) and the 7-day active pilot. Record the results in `ARCHITECTURE.md` §8 and the plan.
4. Close the partly covered acceptance scenarios listed in `TEST_PLAN.md` (Acceptance matrix).
5. Add real site captures to `test/fixtures` with decoder table tests (`TEST_PLAN.md` G1).
6. Run `make oui` before each release.
