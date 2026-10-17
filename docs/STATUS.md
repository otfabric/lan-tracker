# LAN Tracker — Status

Snapshot as of **17 Oct 2026**: the first release, v0.1.0, has not been cut. Everything below is built and tested in the dev container and the Docker suites; nothing has run on a board, on a real network or in a pilot. Update this file whenever an area changes state.

`IMPLEMENTATION_PLAN.md` remains the authority for tasks and exit criteria; this page summarises it and adds what the plan does not hold: deferred work, blocked items, open questions and rejected proposals.

## Summary

- **Phases 0–5** of `IMPLEMENTATION_PLAN.md` are built and verified in Docker and replay. Their hardware and field checks are still open.
- **Release:** the release pipeline is ready; v0.1.0 is cut by the maintainer from the `release` workflow. `LICENSE` is MIT.
- **Field validation** is open: board audits, captured-wire evidence, resource use on the boards, the passive pilot and the 7-day active pilot (`TEST_PLAN.md`).

## By area

"Done" means implemented with automated tests in the dev container (and, where named, the Docker network and systemd suites). Nothing below is verified on hardware unless it says so.

| Area | Status | Notes | Validated by |
| --- | --- | --- | --- |
| Configuration (schema version 1, strict keys) | Done | Another version and unknown keys are errors | `TestSchemaVersionAndUnknownKeys`, `TestUnknownKeysReportedWithLines`, `TestValidation`, `TestDeployConfigs` |
| No identification traffic | Done | No payload probes, no UDP other than name queries | `TestActiveDiscovery` (no payload, no UDP on the wire, `make test-net`) |
| Passive capture and decoders (ARP, IPv4, IPv6/NDP, DHCPv4, mDNS, DNS, LLDP) | Done | Synthetic site capture only; no real site captures yet | `TestDecode`, `TestDecodeMalformed`, edge tests, `Fuzz*`, `test/net` capture tests, `TestCaptureScenario` |
| Typed evidence and presence | Done | Only frames, confirmed addresses and endpoint answers refresh presence | `TestEvidence`, `TestProbeResultsProvePresenceOnlyWhenAnswered`, `TestDHCPLeaseAndServerAddresses` |
| Independent addresses per MAC, conflicts, proxy ARP flag | Done | A MAC on a second interface is another host, linked by `MAC_ALSO_OBSERVED` | `TestIPv4MultipleAddressesAreIndependent`, `TestSameIPOnTwoInterfaces`, `TestMACOnTwoInterfacesIsAnotherHost`, `TestProxyARP*`, `TestScenarios` |
| TCP endpoints per address and port, exact results | Done | `LOCAL_ERROR` recorded; last definite answer kept | `TestEndpointsPerAddress`, `TestEndpointAttributionNeedsOneHolder`, `TestServices`, `TestClassify` |
| Name records (mDNS, DNS, DHCP, LLDP) | Done | Advertiser kept; proxies bind nothing | `TestNames`, `TestMDNSProxyAdvertisesAnotherDevice`, `TestMDNSAliasesGoodbyeAndCacheFlush`, `TestDNSNamesAttachWithoutPresence` |
| Correlator, golden reconstruction scenario | Done | Also passes after a simulated 7-day compaction | `TestScenarios`, `TestCLIReconstruction` |
| Store: schema, WAL writer, retention, compaction, integrity, quarantine | Done | One migration, `0001_init.sql`; an older binary refuses a newer schema | `TestMigrationsInOrderAndNewerSchemaRefused`, `TestOfflineRefusesOtherSchemaVersions`, `test/crash` (in `make check`), `TestCompactRetention`, `make soak` |
| API, CLI (online and `--offline`), metrics | Done | | `internal/{api,cli}` tests, `test/golden/cli_test.go` |
| Active discovery: ARP sweep, ICMP, bare TCP connect | Done in Docker | Off by default; start budgets measured on the wire | `TestActiveDiscovery` (`make test-net`) |
| Name queries (mDNS, unicast DNS) | Done in Docker | Off by default; on the wire only the mDNS group and the configured resolver are asked, within the names budget (no answering responder in the test network) | `TestMDNSQuery`, `TestDNSQueries`, `TestNameQueriesTakeTheBudget`, `TestNamePassAsksOnlyWhatIsKnown`, `TestNameQueriesOnTheWire` (`make test-net`) |
| TCP route and source checks (`RTM_GETROUTE`) | Done, fakes only | Not exercised in `make test-net` on a `/32` interface | `TestNoDirectRouteSendsNothing`, `TestPlanNoDirectRoute` |
| `scan plan` estimate (concurrency slots, phase barriers) | Done | | `TestComputePlan`, `TestSimulateMatchesPacing`, `TestActiveDiscovery` (plan within 10%) |
| Kill switch, `scan plan`, `scan run` | Done | | `TestKillSwitchPersistsAcrossRestarts`, `make test-net` |
| Config reload (`config reload`, SIGHUP) | Done | Concurrency caps reported as needing a restart | `internal/daemon` tests, `make test-systemd` |
| DHCP server monitoring | Done | Receive only, DHCPv4; configurations compared per client, not per server | `TestDHCPServer*`, `TestCaptureScenario`, `make test-net` |
| DHCP exchanges and per-client lease history | Done | Full, partial, relayed, INFORM, NAK, DECLINE, RELEASE, option 121 gateway | `TestDHCPLeaseHistory`, `TestDHCPExchangeOutcomes`, `TestDHCPExchangeLimit`, `TestCaptureScenario` |
| Host descriptions (`hosts set` / `hosts unset`) | Done | | `internal/{cli,api,daemon,correlate,store}` tests |
| Clock-sync marking of events | Done | Read-only `adjtimex` | `TestEventsRecordClockState`, `make test-systemd` |
| Collection gaps in history | Done | `OBSERVATION_GAP` when a passive collector stops and recovers (or is still down at shutdown), and for the daemon's own downtime from the last heartbeat | `TestCollectorGaps`, `TestDowntimeGap`, `TestLivePipeline` |
| Release pipeline (tarballs, checksums, manual `release` workflow) | Ready for v0.1.0 | Notes from `RELEASE.md` (a version without its section is refused); MIT `LICENSE` in the tarballs; the first release is v0.1.0 | CI, `make package` |
| Unit hardening, root with `CAP_NET_RAW` bounding set | Done in containers | Exposure 2.3 on Debian 11, 12 and 13; not yet audited on a board | `make test-systemd` |
| Board audits: RevPi Connect (CM4), Edgebox / amd64 edge box | **Pending (hardware)** | | `TEST_PLAN.md` A1–A4 |
| Neighbour-only run on a real test LAN | **Pending (hardware)** | | `TEST_PLAN.md` B1 |
| DHCP on a real network (switched port and mirror port) | **Pending (field)** | | `TEST_PLAN.md` B5, B8 |
| Real site pcap fixtures | **Pending (field)** | | `TEST_PLAN.md` G1 |
| Captured-wire evidence of ARP, TCP and name queries on a real network, and of no identification traffic | **Pending (field)** | Docker evidence only | `TEST_PLAN.md` D2, D4 |
| Resource use on the boards (128 MB memory ceiling, CPU quota, NFR-PERF-1) | **Pending (hardware)** | Docker and x86 figures do not establish board performance | `TEST_PLAN.md` E1 |
| Passive pilot, 7 days on 3 sites | **Pending (field)** | | `TEST_PLAN.md` F1 |
| 7-day representative active pilot (PLC, inverter, HMI) | **Pending (field)** | | `TEST_PLAN.md` H1 |
| Upgrade and rollback between releases on a board | **Pending (hardware)** | Needs a second release; runbook in `deploy/README.md` | `TEST_PLAN.md` C7 |
| Staged fleet rollout | **Pending (field)** | Runbook in `deploy/README.md` | — |

## Deferred: decided, do not build

| Item | Why | Recorded in |
| --- | --- | --- |
| IPv6 active probing (NDP solicitation) | v1 is IPv4-first; passive IPv6/NDP decoding stays | `REQUIREMENTS.md` §5 (IPv4-first), AGENTS.md rule 12 |
| DHCPv6; synthetic DHCP requests; leasequery | DHCPv6 needs its own decoder and identity model; requests would change server state | `REQUIREMENTS.md` §4, §5 (DHCP server monitoring, DHCP depth) |
| VLAN tagging, web UI, any data leaving the box | Out of scope | `REQUIREMENTS.md` §4, AGENTS.md rule 11 |

## Out of scope: do not build

Device identification of any kind (ADR 0011): payload probes (Modbus, HTTP, TLS, SNMP, banners), protocol-specific UDP probes, passive device, OS or vendor classification and confidence ranking.

## Blocked on the maintainer

- The hardware and field checks above. The maintainer runs `TEST_PLAN.md` on the boards; agents record the results they are given (`ARCHITECTURE.md` §8 table, plan checkboxes) and never tick these from Docker results.
- Cutting v0.1.0 from the `release` workflow.

## Open questions and rejected proposals

- **Throwaway mDNS names.** On a lab LAN (6 Oct 2026), browsers announced `<uuid>.local` names during WebRTC calls, and Android phones rotated `Android_XXXXXXXX` names.
  - Two proposals were rejected by the maintainer:
    - the mDNS decoder ignoring a name that is exactly a UUID plus `.local`;
    - leaving Android names alone and counting them first.
  - No approach has been decided, and heuristic UUID-name suppression is ruled out. Do not re-propose either. Ask before changing how names are handled.

## Known caveats

- On Debian 11 (systemd 247) the unit runs without `PrivateIPC=`, and the journal shows a warning. This is expected.
- On a switched port, capture does not see unicast DHCP replies or renewals, so leases are often partial and silence never proves there is no DHCP server or lease.
- Docker's kernel is not the target kernel, so the board audits stay mandatory.
- The unit's `CPUQuota=20%` stretches CPU-heavy start-up work. Parsing the text OUI registry took 6 s on a CM4 under it, which is why the table is precomputed (ADR 0010). Keep start-up work small.
- The simulated soak reaches 186.5 MB of the 200 MB budget after 90 days on a worst-case 50-host LAN (`make soak`, 17 Oct 2026); a busier site reaches `storage.retention.max_db_size` sooner, which then shortens retention (oldest roll-ups first).

## Next tasks

1. Run the full gate set on the release commit (`make mod-verify check`, `make fuzz`, `make check-all`, `make soak`, `make package`), then the maintainer cuts v0.1.0.
2. Maintainer: run `TEST_PLAN.md` on the RevPi Connect and the amd64 edge box, then the passive pilot (3 sites, 7 days) and the 7-day active pilot. Record the results in `ARCHITECTURE.md` §8 and the plan.
3. Close the partly covered acceptance scenarios listed in `TEST_PLAN.md` (Acceptance matrix).
4. Add real site captures to `test/fixtures` with decoder table tests (`TEST_PLAN.md` G1).
5. Run `make oui` before each release.
