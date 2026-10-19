# LAN Tracker — Hardware test plan

Two parts: the [acceptance matrix](#acceptance-matrix-and-automated-coverage), the product's acceptance scenarios mapped to the automated tests that cover them, with the scenarios no automated test covers; then the manual procedure for the boards.

A plan to follow on the two target boards, the **RevPi Connect** (`linux/arm64`) and the **amd64 edge box**, that closes the field validation items in `ROADMAP.md`. Run it in order: the audit first, the 7-day runs last. Record each result in the table of its section (one column per board) and send the filled plan back as feedback; failures and surprises matter more than passes.

Every command runs on the board as a user with `sudo` unless it says otherwise. `<iface>` is the board's monitored interface (`eth0`, `eth1`, `enp1s0`, ...).

## Safety rules for this plan

- Tests marked **LAB ONLY** disturb a network (a second DHCP server, a duplicate IP, a pulled cable). Run them on an isolated lab LAN with test devices only, never on a production OT network.
- Active discovery stays off until section D, and on a site only after the site's device mix has been reviewed (`deploy/README.md`, staged rollout). Keep `sudo lan-tracker active disable --reason "..."` at hand: it stops every probe at once.
- Take a database copy before every upgrade and before section C5.

## What you need

| Item | Notes |
| --- | --- |
| Release | The latest `vX.Y.Z` from the Releases page, `linux-arm64` for the RevPi, `linux-amd64` for the edge box. The first release is v0.1.0 (17 Oct 2026); to test a build from `main`, use the tarballs `make package` builds |
| Lab LAN | A switch with a few devices: a laptop, a phone, a printer or NAS, ideally a PLC or HMI |
| Two test laptops | For the IP-change and duplicate-IP tests (B4) |
| A spare device that can run `dnsmasq` | For the rogue DHCP server test (B5) |
| OT rig (sections D, H) | A real PLC, inverter and HMI the site team can watch for faults |
| Tools on the boards | `tcpdump`, `curl`; `jq` helps but is optional |

## Results summary

Fill in **PASS**, **FAIL** or **SKIP** (with the reason) and copy the per-test notes into the sections.

| Test | Covers (`ROADMAP.md` item; requirements) | RevPi Connect | amd64 edge box |
| --- | --- | --- | --- |
| A1 Board facts | Board audit | | |
| A2 Install from the release | Board audit (install) | | |
| A3 Sandbox and capabilities | Board audit; NFR-SEC-1 | | |
| A4 capcheck under the unit | Board audit; NFR-SEC-1 | | |
| B1 Neighbour table only | Neighbour-only discovery; FR-NL-1, FR-NL-2 | | |
| B2 Per-interface scoping | Neighbour-only discovery; FR-ID-2 | | |
| B3 Passive capture and names | Passive capture on hardware; FR-PA-1 to FR-PA-4 | | |
| B4 IP change and duplicate IP (LAB ONLY) | Reconstruction on hardware; FR-ID-5 | | |
| B5 DHCP server monitoring (LAB ONLY) | DHCP on a real network; FR-PA-7 | | |
| B6 Point-in-time queries | Reconstruction on hardware; FR-CLI-2 | | |
| B7 Host descriptions | FR-MD-1 on hardware | | |
| B8 DHCP exchanges and leases (LAB ONLY) | DHCP on a real network; FR-PA-8 to FR-PA-10 | | |
| C1 Reload | Robustness; FR-CFG-3 | | |
| C2 Restart and clean stop | Robustness; NFR-REL-1 | | |
| C3 Watchdog | Robustness; NFR-REL-1 | | |
| C4 Link down and up (LAB ONLY) | Robustness; NFR-REL-1, FR-NL-3 | | |
| C5 Power cut and clock | Robustness; NFR-REL-2, FR-ST-4 | | |
| C6 Corrupt database (LAB ONLY) | Robustness; FR-ST-4 | | |
| C7 Upgrade and rollback (needs a second release) | Upgrade and rollback | | |
| D1 Active discovery on the lab LAN | Active discovery on a lab LAN; FR-AC-1 to FR-AC-3 | | |
| D2 Budgets on the wire | Active discovery on a lab LAN; NFR-SAFE-1, NFR-SAFE-5, FR-AC-10 | | |
| D3 Kill switch | Active discovery on a lab LAN; FR-CFG-4 | | |
| D4 Name queries, route checks and absent identification traffic | Captured-wire evidence; FR-AC-5, FR-AC-12 | | |
| E1 Resource budget, 7 days | Resource use on the boards; NFR-PERF-1 | | |
| F1 Pilot sites, passive, 7 days | Passive pilot; staged fleet rollout | | |
| G1 Site captures | Real site captures | | |
| H1 OT soak with active discovery, 7 days | Representative active pilot | | |

None of these has been run yet (17 Oct 2026).

## Acceptance matrix and automated coverage

The acceptance scenarios, one by one, with the tests that cover them (17 Oct 2026). Tests run in the dev container with `make check` unless marked `test-net` (`make test-net`, network namespaces in Docker). **Covered** means the scenario's observable result is asserted; **partly** means some of it is; **open** means no automated test. What a partly covered or open scenario lacks is listed in `ROADMAP.md` (Known gaps and limitations). Docker results do not close the board and site checks in the sections after this one.

| Scenario | Tests | Coverage |
| --- | --- | --- |
| Quiet device with ICMP off, ARP on, TCP/502 open | `TestTCPRunsPerPort`, `TestOperatorScan` (TCP passes with ICMP not run), `TestActiveDiscovery` (test-net: ARP sweep, then TCP to known hosts) | Partly |
| Known MAC, unknown IP, device sends nothing | `TestExpiryAndPresence` (last-known record kept, presence decays), `TestSweepTargets`, `TestKnownTargets` (only configured networks; IPv6 never swept), `TestStatusShowsLastPass`, `TestLastPasses` (swept and replied counts) | Partly |
| One MAC with production and maintenance IP | `TestIPv4MultipleAddressesAreIndependent`, `TestEndpointsPerAddress` | Covered |
| MAC moves to a new IP while the old one is quiet | `TestExpiryAndPresence`, `TestIPv4MultipleAddressesAreIndependent`, `TestLateObservationDoesNotReopen` | Covered |
| Same IP on eth0 and eth1 | `TestSameIPOnTwoInterfaces`, `TestMACOnTwoInterfacesIsAnotherHost`, `TestDaemonFindsHosts` (test-net) | Covered |
| Same IP claimed by two MACs on one context | `TestScenarios` and `TestCLIReconstruction` (golden duplicate IP), `TestCaptureScenario`, `TestUnboundWhenHolderIsAmbiguous`, `TestEndpointAttributionNeedsOneHolder` | Covered |
| Gateway forwards public-IP traffic | `TestOnLinkCheck` | Covered |
| Proxy ARP answering many candidates | `TestProxyARPByOverlaps`, `TestProxyARPByThreshold`, `TestGratuitousARPIsNotProxy`, `TestProxyARPClaimsAge`, `TestProxyARPLateReplyDoesNotFlag` | Covered |
| mDNS proxy advertises another device | `TestMDNSProxyAdvertisesAnotherDevice` | Covered |
| mDNS aliases, cache flush, goodbye and TTL expiry | `TestMDNSAliasesGoodbyeAndCacheFlush`, `TestNamesOutliveNameExpiry`, `TestStaleNames`, `TestMDNSRecordLimit` | Covered |
| DNS PTR/A/AAAA/CNAME, negative answer, failed resolver | `TestDNSQueries` (PTR, CNAME followed at most twice, NXDOMAIN cached, timeout records nothing), `TestDNSNamesAttachWithoutPresence`, `TestReverseAddr` | Covered with fakes; the queries on the wire in Docker (`TestNameQueriesOnTheWire`) |
| LLDP chassis MAC differs from the Ethernet sender | `TestLLDPIdentifiers`, `TestDecode` (LLDP cases) | Covered |
| TCP OPEN, then TIMEOUT, then REFUSED | `TestServices` (distinct results, last definite answer kept), `TestClassify`, `TestTimeoutDoesNotBringBackAMissingHost`, `TestActiveDiscovery` (test-net: OPEN, REFUSED, UNREACHABLE) | Covered |
| TCP result after IP reassignment | `TestEndpointAttributionNeedsOneHolder` | Covered |
| Observer with only /32 field addresses | `TestNoDirectRouteSendsNothing`, `TestPlanNoDirectRoute`, `TestLinkSourceAndOwn` (ARP probe from 0.0.0.0 off subnet) | Partly |
| DHCP OFFER without ACK | `TestDHCPExchangeOutcomes` (`offered`, no lease), `TestCaptureScenario` (the rogue's offer) | Covered |
| DHCP ACK visible, no client traffic | `TestDHCPLeaseHistory`, `TestDHCPLeaseAndServerAddresses`, `TestCaptureScenario` | Covered |
| DHCP INFORM and config-only ACK | `TestDHCPExchangeOutcomes` (`inform_ack`, no lease), `TestDecode` ("dhcp ack to an inform"), `TestDHCPServerConfig` | Covered |
| Server identifier differs from the relay source | `TestDecode` (relayed offer), `TestDHCPExchangeOutcomes` (relayed lease: server and sender apart), `TestDHCPServerAllowlist` | Covered |
| Relayed DHCP on UDP 67/67, client on another link | `TestDecode` (request and ACK on 67 to 67), `TestDHCPExchangeOutcomes` (no host for the relayed client) | Covered |
| Two offers, one selected server | `TestDHCPLeaseHistory` (two offers, selected server), `TestDHCPServersWithoutAllowlist` (`unchecked`, no verdict) | Covered |
| Same server varies options across clients | `TestDHCPServerConfig` (no server-level event), `TestDHCPExchangeOutcomes` (per-client snapshots) | Partly |
| Options 15/81/119, overload and split options | `TestDecode` ("split options, overload, FQDN, search list and classless routes", "malformed options"), `TestDHCPGather`, `TestDHCPOptionValues` (compression pointers, FQDN forms), `FuzzDHCP` | Covered |
| Option 121 with a default route | `TestDHCPExchangeOutcomes` (gateway from option 121), `TestDHCPOptionValues` (249 sets no gateway) | Covered |
| Lease renewal and expiry with hidden unicast | `TestDHCPLeaseHistory` (ACK-only renewal, expiry per last ACK), `TestDHCPExchangeOutcomes` (unanswered REQUEST `incomplete`), `TestCaptureScenario` (every lease partial) | Covered |
| Collector goes down during an incident | `TestCollectorGaps`, `TestDowntimeGap`, `TestLivePipeline` (`OBSERVATION_GAP`), `TestLinkEvents` (`INTERFACE_DOWN`/`UP`), `TestCollectorRetriesWithBackoff`, `TestStatusAndMetrics`, `TestCaptureRefusesDownInterface` (test-net) | Covered |
| Broad manual scan overlaps scheduled probes | `TestPacingStaysWithinEveryOneSecondWindow`, `TestConcurrencyCaps`, `TestTargetSpacing`, `TestOperatorScanRefusedBusyAndAborted`, `TestSimulateMatchesPacing`, `TestActiveDiscovery` (test-net: plan within 10%) | Partly |
| Active disabled, interface removed, reload during a scan | `TestKillSwitchCancelsAndResumes`, `TestSweepStopsOnKillSwitch`, `TestReloadTogglesInterfaceActive`, `TestReloadChangesIntervalAndEnabled`, `TestConnectCancelledAndStopped`, `TestKillSwitchAfterShutdown`, `TestActiveDiscovery` (test-net: kill switch) | Partly |
| Packet loss and slow TCP listeners | `TestConnectClosesWithReset` (RST close, SYN retransmission cap), `TestActiveDiscovery` (test-net: kernel packets logged apart from starts) | Open |
| Raw observations compacted | `TestScenarios` (golden answers after a 7-day compaction), `TestCompactRetention` (open leases kept, ended leases, exchanges, endpoint results, name records pruned by retention) | Partly |
| Schema upgrade and rollback | `TestMigrationsInOrderAndNewerSchemaRefused`, `TestOfflineRefusesOtherSchemaVersions`; `make test-systemd` (install block) | Partly |
| Storage pressure, malformed input, restart mid-write | `TestCompactSizeCap`, `TestKillDuringWrites` (`test/crash`), `TestCorruptDatabaseIsQuarantined`, `TestCorruptDatabaseRecreated`, `Fuzz*`, `TestDecodeMalformed`, `TestDecoderBoundsItsTable`, `TestDHCPExchangeLimit`, `TestBus` (bounded bus, drop counter) | Covered |

The checks no automated test closes (captured-wire evidence, resource use on the boards, the pilots) are the field validation items in `ROADMAP.md`, run with the sections below.

---

## A. Install and audit

### A1 Board facts

```bash
cat /proc/device-tree/model 2>/dev/null || cat /sys/class/dmi/id/sys_vendor /sys/class/dmi/id/product_name
. /etc/os-release && echo "$PRETTY_NAME"
uname -r -m
systemctl --version | head -1
ip -br link
sysctl net.ipv4.ping_group_range
timedatectl | grep -E 'RTC|synchronized|NTP'
```

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Model | | |
| Debian release, kernel, systemd | | |
| Interfaces to monitor | | |
| `ping_group_range` | | |
| RTC present, NTP client | | |

### A2 Install from the release

Follow `deploy/README.md` step 1 (verify and unpack) and step 2 (install) exactly.

**Expect:** `sha256sum --check` OK; `config validate` valid; `systemctl status lan-tracker` active; the journal shows `active discovery: off` and `lan-tracker running` within a few seconds (start-up under the 20% CPU cap should take about a second); `sudo lan-tracker daemon status` shows the release version and `State: OK`.

Then edit `/etc/lan-tracker/config.yaml` for the board's interface(s), `sudo lan-tracker config validate`, `sudo lan-tracker config reload`.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result, start-up time (journal) | | |

### A3 Sandbox and capabilities

```bash
systemd-analyze security lan-tracker | tail -1
grep Cap /proc/$(systemctl show -p MainPID --value lan-tracker)/status
ls -l /run/lan-tracker/api.sock /data/lan-tracker/
```

**Expect:** exposure ≤ 2.5 (2.3 in the containers); `CapEff` and `CapBnd` `0000000000002000`, `CapAmb` 0; socket `srw-rw---- root root`. The journal has no `lan-tracker.service:` line about an unknown or invalid setting.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Exposure | | |
| Capabilities, socket | | |

### A4 capcheck under the unit

Run the board audit in `tools/capcheck/README.md` from the unpacked release, with the board's interface and a target on it (the gateway). Also run it once plainly, `./capcheck --interface <iface>`, without capabilities.

**Expect:** under the unit, the `privilege` line `CapEff=0x2000: euid 0 (root), CAP_NET_RAW` and every row PASS or SKIP (NDP SKIP: IPv6 probing is deferred), including `adjtimex read`. Without capabilities exactly the `CAP_NET_RAW` rows fail. Paste both outputs; they become the board's row in `ARCHITECTURE.md` §8.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result under the unit | | |
| Result without capabilities | | |

---

## B. Functional, on the lab LAN

### B1 Neighbour table only

Discovery from the kernel neighbour table alone. Turn capture off on the interface; passive settings need a restart:

```yaml
interfaces:
  - name: <iface>
    passive: { enabled: false }
    active:  { enabled: false }
```

```bash
sudo lan-tracker config validate && sudo systemctl restart lan-tracker
ping -c1 <a lab device>          # puts it in the neighbour table
sudo lan-tracker hosts list --interface <iface>
sudo lan-tracker observations list --source kernel_neighbor --limit 10
```

**Expect:** devices the board talks to appear (source `kernel_neighbor`) with their OUI registrant; `daemon status` shows `capture disabled`. Restore `passive: { enabled: true }` and restart afterwards.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Hosts found, registrants right | | |

### B2 Per-interface scoping

Needs two monitored interfaces on two networks (the RevPi Connect has two ports). Configure both, restart, then move one test laptop from the first network to the second.

```bash
sudo lan-tracker hosts list
sudo lan-tracker events list --type mac-also-observed
sudo lan-tracker hosts find <laptop MAC>
```

**Expect:** the laptop is two hosts, one per interface; `MAC_ALSO_OBSERVED` names both interfaces (a second sighting, not proof of movement); `hosts find` shows both, grouped by interface. The same IP used on both networks is two bindings, never a conflict.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result | | |

### B3 Passive capture and names

With capture on (the default), connect or reboot a few lab devices.

```bash
sudo lan-tracker watch --interface <iface>      # leave running while devices join; Ctrl-C to stop
sudo lan-tracker hosts list --interface <iface> --active
sudo lan-tracker hosts show <host-id>
```

**Expect:** `HOST_DISCOVERED`, `IP_ADDED` and `HOSTNAME_ADDED` (mDNS, DHCP, LLDP names) as devices join; a DHCP server's ACK alone neither creates a host nor binds an address; no journal line per packet; `daemon status` `capture running`, no growing drop counter. Compare `hosts list` with what you know is on the LAN: note anything missing or wrong.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Devices found / expected | | |
| Missing or wrong | | |

### B4 IP change and duplicate IP (LAB ONLY)

With two test laptops A and B and a spare address X on the lab LAN (never a production device's address):

1. Note the time, give laptop A address X (static), ping the board.
2. Ten minutes later give laptop A address Y.
3. Give laptop B address Y while A still holds it, ping from both; then take B off Y.

```bash
sudo lan-tracker events list --type ip-added,ip-removed,duplicate-ip,duplicate-ip-resolved --since 1h
sudo lan-tracker hosts find X --at "<time of step 1 + 5 min>"
sudo lan-tracker hosts history Y --since 1h
```

**Expect:** `IP_ADDED` Y for A, with X still open (a new address never closes another; X closes only after `identity.address_expiry` unconfirmed, as an inference, or on a takeover); `DUPLICATE_IP_DETECTED` for Y naming both laptops, then `DUPLICATE_IP_RESOLVED`; `hosts find X --at` returns laptop A; the history shows the whole sequence.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result | | |

### B5 DHCP server monitoring (LAB ONLY)

A second DHCP server disrupts every client on the LAN: lab LAN only.

```bash
sudo lan-tracker dhcp servers                 # after a client renews: the lab's server, status unchecked
```

Put the lab server's identifier in the allowlist (`interfaces[].dhcp.servers: [<server IP>]`), `config reload`, then start `dnsmasq` on the spare device for a few minutes and renew a client's lease.

```bash
sudo lan-tracker dhcp servers --status unexpected
sudo lan-tracker events list --type dhcp-server-unexpected,dhcp-server-discovered --since 1h
```

**Expect:** the lab server `allowed`; the spare device `DHCP_SERVER_DISCOVERED` and `DHCP_SERVER_UNEXPECTED`; no event about the server's configuration (configurations are compared per client, B8); on a plain switch port some replies may not be seen (unicast to the client), which the docs expect. Stop `dnsmasq` afterwards.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result, replies seen | | |

### B6 Point-in-time queries

```bash
sudo lan-tracker hosts find <IP> --at "<a time from B4>"
sudo lan-tracker --offline hosts find <IP> --at "<same time>"
sudo lan-tracker hosts history <MAC> --since 24h
```

**Expect:** online and `--offline` answers identical, also with the daemon running.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result | | |

### B7 Host descriptions

```bash
sudo lan-tracker hosts set <MAC of a known device> --description "Solar panel rooftop"
sudo lan-tracker hosts list --description solar
sudo lan-tracker hosts show <host-id>
sudo lan-tracker hosts unset <MAC> --description
sudo lan-tracker events list --type described
sudo lan-tracker hosts set <MAC> --description "x" --offline
```

**Expect:** the description in `hosts list` (last column) and `hosts show`; `HOST_DESCRIBED` for the set and the unset, with your user; setting the same text twice records one event; a MAC on two interfaces asks for `--interface`; `--offline` refuses (exit 64). The description survives `sudo systemctl restart lan-tracker`.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result | | |

### B8 DHCP exchanges and leases (LAB ONLY)

On the lab LAN, once on a plain switch port and, if the switch has one, once on a mirror port. Release and renew a lab client's lease (`dhclient -r` then `dhclient`, or unplug and replug it), change one option on the lab server (a DNS server), renew again, then wait for a renewal with nothing changed.

```bash
sudo lan-tracker dhcp exchanges --since 1h
sudo lan-tracker dhcp leases --mac <client MAC>
sudo lan-tracker hosts show <client MAC>
sudo lan-tracker events list --type dhcp-lease,dhcp-client-config-changed,dhcp-release,dhcp-nak,dhcp-decline --since 1h
```

**Expect:** each transaction one exchange, with offers, the selected server and the outcome; on the switch port the unicast replies and renewals missing and the leases marked `partial`, never filled in; on the mirror port complete exchanges. A renewal with nothing changed extends the lease row (`ACKS` grows) without an event; the changed DNS server is one `DHCP_CLIENT_CONFIG_CHANGED`; the release is `DHCP_LEASE_RELEASED`. `EXPIRES` is the expiry per the last ACK seen. The lease does not make the client present by itself.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Switch port: exchanges, partial leases | | |
| Mirror port: complete exchanges | | |

---

## C. Robustness

### C1 Reload

Change `logging.level` to `debug`, then add an unknown key, then put both back:

```bash
sudo lan-tracker config reload
```

**Expect:** the first reload lists `logging.level` as applied and the journal says `configuration reloaded; active discovery: ...`; the unknown key is rejected (exit 2, per-key error) and nothing changes; a restart-only change (e.g. `passive.ring_size`) is listed as needing a restart.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result | | |

### C2 Restart and clean stop

```bash
sudo systemctl restart lan-tracker
sudo journalctl -u lan-tracker -o short-precise -n 20 --no-pager
ls /data/lan-tracker/
```

**Expect:** `lan-tracker stopped` before the new start; no `-wal`/`-shm` left after a stop; no second `HOST_DISCOVERED` for known hosts after the restart.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result, stop and start times | | |

### C3 Watchdog

```bash
sudo kill -STOP $(systemctl show -p MainPID --value lan-tracker)
sudo journalctl -u lan-tracker -f       # wait about 60 s
```

**Expect:** systemd reports a watchdog timeout, kills and restarts the daemon (`Restart=on-failure`); `daemon status` OK afterwards.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result, time to restart | | |

### C4 Link down and up (LAB ONLY)

Pull the monitored cable for two minutes, plug it back.

**Expect:** `INTERFACE_DOWN` then `INTERFACE_UP`; capture shows `failed` while down and `running` again after; hosts are not all marked disappeared (presence thresholds are minutes to hours); no restart needed.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result | | |

### C5 Power cut and clock

Take a database copy (`deploy/README.md`, Upgrade). Cut the power while the daemon runs; if possible boot without network for a few minutes, then connect.

```bash
sudo lan-tracker daemon status
sudo lan-tracker events list --since 1h
sudo lan-tracker db check
```

**Expect:** the daemon starts, no `DATABASE_RECREATED`, `db check` ok; `Clock: unsynced` until chrony synchronises, events written before that marked `*` (unsynced); on a board with an RTC the clock may already be right at boot but still `unsynced` until NTP.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result, clock state at boot | | |

### C6 Corrupt database (LAB ONLY)

On a copy-backed test board only:

```bash
sudo systemctl stop lan-tracker
sudo cp -a /data/lan-tracker/hosts.db /root/hosts.db.keep
sudo dd if=/dev/urandom of=/data/lan-tracker/hosts.db bs=4096 seek=2 count=4 conv=notrunc
sudo systemctl start lan-tracker
sudo lan-tracker events list --type db-recreated
ls /data/lan-tracker/
```

**Expect:** the daemon starts on a new database, `DATABASE_RECREATED` is its first event, the corrupt file kept as `hosts.db.corrupt-<time>`. Put `/root/hosts.db.keep` back afterwards (stop, copy, start).

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result | | |

### C7 Upgrade and rollback

Once there are two releases: on a board that runs the previous one with some history (descriptions, a kill switch set once), follow `deploy/README.md` (Upgrade and rollback) step by step, then roll back, then upgrade again.

**Expect:** the new release starts on the database with the same host IDs, descriptions, bindings, events and kill switch; `daemon status` shows the new version; if the schema changed, the older binary refuses the migrated database (`database schema version N is newer than this binary supports`) and starts with the copy put back.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| From → to, rollback, result | | |

---

## D. Active discovery, lab LAN first

### D1 Active discovery on the lab LAN

Enable it on the lab interface, excluding the gateway and anything fragile:

```yaml
interfaces:
  - name: <iface>
    active: { enabled: true, networks: [<lab /24>], exclude: [<gateway>] }
```

```bash
sudo lan-tracker config validate          # shows the sweep size and duration
sudo lan-tracker config reload            # journal: active discovery: arp every 15m on <iface> (...)
sudo lan-tracker scan plan
sudo lan-tracker scan run
sudo lan-tracker daemon status            # after the first pass: arp ... last pass ...
```

**Expect:** `scan run` finds devices that never announce themselves; `daemon status` shows `arp running afpacket last pass ...: N swept, M replied`; periodic sweeps write nothing to the journal unless they find something new.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result, swept / replied | | |

### D2 Budgets on the wire

While `scan run` and a periodic sweep run:

```bash
sudo tcpdump -i <iface> -n -tttt -w /tmp/probes.pcap ether src $(cat /sys/class/net/<iface>/address) and arp
```

Count per second afterwards (`tcpdump -r /tmp/probes.pcap -tt | cut -d. -f1 | uniq -c | sort -n | tail`).

**Expect:** never more than 5 probe starts in any second (the defaults: 5 starts/s in all, ARP 5); ARP the kernel sends to resolve a probed host, handshake ACKs and resets are kernel packets, counted apart and not budgeted; no request to an excluded address or outside the configured network; the measured starts and duration within 10% of `scan plan`.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Peak per second, plan vs measured | | |

### D3 Kill switch

Start `scan run`, then from a second shell:

```bash
sudo lan-tracker active disable --reason "kill switch test"
sudo systemctl restart lan-tracker && sudo lan-tracker daemon status
sudo lan-tracker active enable
```

**Expect:** probing stops within a second (tcpdump), the scan is recorded as aborted, `ACTIVE_DISABLED` in the journal; after the restart `Active: DISABLED (kill switch test)` and the journal says `active discovery: stopped by the kill switch (...)`; `active enable` resumes.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Result, stop latency | | |

### D4 Name queries, route checks and absent identification traffic

The captured-wire evidence on a real network. Enable TCP checks (as in H1, step 3) and name queries (top-level `active: { names: { enabled: true, resolvers: [<lab DNS server>] } }`), reload, then capture everything the box sends for one full pass:

```bash
sudo tcpdump -i <iface> -n -tttt -w /tmp/wire.pcap ether src $(cat /sys/class/net/<iface>/address)
tcpdump -r /tmp/wire.pcap -n 'udp and not port 5353 and not port 53'        # expect nothing
tcpdump -r /tmp/wire.pcap -n 'tcp and (ip[2:2] - ((ip[0] & 0xf) << 2) - ((tcp[12] & 0xf0) >> 2)) != 0'   # expect nothing: no payload
sudo lan-tracker services list
```

On a board whose monitored interface has only a `/32` address, or for a known host behind the gateway, also run `sudo lan-tracker scan plan`.

**Expect:** mDNS queries only to 224.0.0.251 and unicast DNS only to the configured resolver, asking only about known names and addresses; TCP segments only SYN, the handshake's ACK and a reset; no UDP other than DNS and mDNS; a target without a direct route is not dialled and shows `LOCAL_ERROR` with the reason, and `scan plan` lists it. Name answers add name records and never make a host present.

| | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Name queries seen, other UDP or payload | | |
| Route check result | | |

---

## E. Resource budget

### E1 Resource budget, 7 days

Passive only (active off), on a LAN of at least 50 devices if possible. Take readings at start, day 1 and day 7:

```bash
systemctl show -p CPUUsageNSec,MemoryCurrent lan-tracker
ps -o etimes=,rss= -p $(systemctl show -p MainPID --value lan-tracker)
sudo grep write_bytes /proc/$(systemctl show -p MainPID --value lan-tracker)/io
sudo lan-tracker db info
journalctl -u lan-tracker --since -24h | wc -l
```

CPU % = (CPUUsageNSec difference in ns) / (seconds between readings × 10⁹) × 100.

**Expect (NFR-PERF-1):** average CPU < 5%, RSS < 50 MB and not growing (the unit's ceilings are `MemoryMax=128M` and `CPUQuota=20%`), database on track for < 200 MB after 90 days, SD-card writes modest (batched commits), journal lines only for events. For comparison, the simulation (`make soak`, 17 Oct 2026, a worst-case 50-host LAN with name records, TCP endpoints and DHCP exchanges) reached 186.5 MB after 90 days, with an RSS of 31.8 MB.

| Reading | RevPi Connect | amd64 edge box |
| --- | --- | --- |
| Hosts on the LAN | | |
| CPU % day 1 / day 7 | | |
| RSS day 1 / day 7 | | |
| DB size day 1 / day 7 | | |
| Bytes written per day | | |
| Journal lines per day | | |

---

## F. Pilot sites

### F1 Pilot sites, passive, 7 days

Three pilot sites, passive only, with the staged-rollout checks of `deploy/README.md`. At each site, after 7 days:

```bash
sudo lan-tracker daemon status
sudo lan-tracker hosts list
sudo lan-tracker dhcp servers
sudo lan-tracker events list --since 7d -o csv > /tmp/events.csv
```

**Expect:** `hosts list` matches what field support knows is there (note anything missing or unknown); CPU and RSS within E1's budget; no capture drops; the DHCP servers are the expected ones. B4's IP change and duplicate reconstruction is done on the lab LAN, not on a pilot site.

| Site | Board | Hosts found / expected | CPU, RSS | Notes |
| --- | --- | --- | --- | --- |
| 1 | | | | |
| 2 | | | | |
| 3 | | | | |

---

## G. Field captures

### G1 Site captures

At a pilot site (with permission), capture 30 minutes of discovery traffic on the monitored interface:

```bash
sudo tcpdump -i <iface> -w /tmp/site-<name>.pcap 'arp or (udp port 67 or udp port 68) or udp port 5353 or ether proto 0x88cc or icmp6'
```

Check what it gives by replaying it through a separate daemon (its own database and socket, the live one keeps running):

```yaml
# /tmp/replay.yaml
version: 1
interfaces:
  - name: eth9
    prefixes: [<site network>]
    replay: { file: /tmp/site-<name>.pcap }
replay: { exit_when_done: true }
storage: { path: /tmp/replay.db }
api: { socket: /tmp/replay.sock }
logging: { format: text, level: warn }
```

```bash
lan-tracker daemon run --config /tmp/replay.yaml
lan-tracker --db /tmp/replay.db --offline hosts list
```

Send the captures (they contain the site's addresses and names: handle them as site data) for the decoder fixtures.

| Site | Board | Capture size, devices in the replay | Notes |
| --- | --- | --- | --- |
| | | | |

---

## H. OT soak

### H1 OT soak with active discovery, 7 days

On the OT rig with a real PLC, inverter and HMI, with the site team watching the devices' diagnostics. Enable in steps, a day each, only after the previous step showed nothing:

1. ARP sweep of the rig's network (`interfaces[].active` as in D1; the sweep runs every 15 min by default).
2. ICMP echo to known hosts: top-level `active: { icmp: { enabled: true } }`.
3. TCP connect to the devices' Modbus port: top-level `active: { tcp: { enabled: true, targets: [{ port: 502, name: modbus, timeout: 750ms }] } }`, a plain connect, closed at once, no data.

This is the 7-day representative active pilot: run it after the passive observation of E1 or F1, and report the device and network load (D2's counts per step).

Apply each step with `sudo lan-tracker config validate && sudo lan-tracker config reload`; the journal line says what runs.

**Expect:** no device fault, alarm, diagnostic-buffer entry or dropped session on the PLC, inverter or HMI; budgets as in D2; `services list --port 502` shows OPEN for the devices listening on 502 (an open port, not proof of Modbus). Note anything the devices log about the probes.

| Step | Days | Device faults or log entries | Notes |
| --- | --- | --- | --- |
| ARP | | | |
| ICMP | | | |
| TCP 502 | | | |

---

## Feedback

Send back this file with the tables filled, plus:

- the outputs of A3 and A4 per board (they go into `ARCHITECTURE.md` §8);
- any failing command with its full output and `sudo journalctl -u lan-tracker -n 200 --no-pager`;
- wrong or missing hosts or names (B3, F1);
- the captures from G1 and D4;
- anything in this plan that was unclear or impossible on the hardware.

Each passed test closes its part of the matching item in `ROADMAP.md` (Field validation); each failure becomes a fix or a decision.
