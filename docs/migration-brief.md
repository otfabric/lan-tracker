# OT Fabric LAN Tracker — migration, refactoring and hardening brief

Prepared 16 October 2026. Intended recipient: Claude Code implementing the migration from `boeboe/lan-sentinel` to OT Fabric.

## 1. Decision and product boundary

**Recommended product:** OT Fabric LAN Tracker.  
**Repository and binary:** `otfabric/lan-tracker`, `lan-tracker`.  
**Go module:** `github.com/otfabric/lan-tracker`.

**Purpose:** Keep an evidence-based history of which MAC addresses, IP addresses, names and TCP endpoints are visible on each local OT network, so operators can find equipment, troubleshoot connectivity and reconstruct incidents.

The product should answer:

- Where is this MAC now, and which IPs did it use before?
- Which MAC or MACs claimed this IP at a particular time?
- Which mDNS, DHCP and DNS names are associated with those addresses, and how fresh is each association?
- Did TCP/502 or the configured web port answer? Was it refused, unanswered, locally unroutable or never tested?
- What DHCP configuration was offered or acknowledged to this client: address, mask, gateway, DNS, domain, search list and lease timing?
- Did an address conflict, DHCP configuration change, interface outage or naming change coincide with a connectivity incident?
- What did the collector actually see, and where are the gaps in its visibility?

**This is a connectivity tracker with history.** It does not determine equipment models, operating systems, firmware, serial numbers or physical asset identity. An operator supplies a description attached to the MAC within its network context. No fingerprinting subsystem is required.

### Naming alternatives

| Name | Fit | Decision |
|---|---|---|
| `lan-tracker` | Covers discovery, changing addresses and continuing history; a concrete application name | Recommended |
| `lan-ledger` | Emphasises durable history | Good alternative, less clear about active discovery |
| `ot-inventory` | Clearly OT-focused | Suggests a broader asset-management product than this brief calls for |
| `lan-sentinel` | Familiar existing name | Suggests security monitoring; does not communicate the narrower connectivity purpose as clearly |

OT Fabric uses `go-*` for libraries, `*ctl` for protocol CLIs and descriptive names such as `sunspec-explorer` for applications. Keep this as one application repository; do not create `go-lan-tracker`, a separate `lanctl`, a plugin SDK or a general discovery framework. These names are recommendations, not a trademark clearance.

## 2. Review baseline and findings from the existing code

Reviewed source: [`boeboe/lan-sentinel`](https://github.com/boeboe/lan-sentinel), commit [`a437a228c8e58fbf0bb10de509381ea2336448f2`](https://github.com/boeboe/lan-sentinel/tree/a437a228c8e58fbf0bb10de509381ea2336448f2), dated 8 October 2026. Latest published release returned during the review: **v0.0.9**. `docs/STATUS.md` still reports v0.0.6, so it is useful context but not an authoritative release inventory.

This is a source and design review. The existing test suites and hardware checks were **not executed for this proposal**. Test and field-validation claims in the repository remain the repository's own claims until rerun. Claude must refresh the source SHA and release baseline before implementing, and record any differences.

The reviewed tree contains approximately 25,200 lines in 114 non-test Go files and 18,339 lines in 85 Go test files, including tooling and test support. The active identification package alone has 1,614 non-test lines; passive identification has another 466. Removing those packages helps, but the larger simplification is removing their cross-cutting state, configuration, commands, events and documentation.

### What is already worth keeping

- Pure Go, static Linux binaries for amd64/arm64, without libpcap or a runtime SQLite dependency.
- Separation of packet/kernel access from pure decoders and correlation logic.
- Collectors and probes emitting observations; one correlator deciding state and events.
- One SQLite writer, WAL, historical bindings, event evidence and read-only offline queries.
- Context-aware MAC identity rather than treating an IP or hostname as the device key.
- ARP sweeps, kernel-neighbour collection and configurable TCP connects.
- Shared probe budgets, exclusions, a persisted kill switch, on-demand scan planning and cancellation.
- Packet/observation replay, a simulated clock, decoder fuzzing, golden reconstruction tests and network/systemd harnesses.
- Existing human descriptions: `hosts set/unset --description` already exist. This is reuse, not a new asset database.

### Findings to address during refactoring

| Finding in reviewed source | Operational consequence | Required treatment |
|---|---|---|
| `internal/probe/idprobe/*` sends Modbus identification, HTTP, TLS, SNMP, SSH-banner, Telnet and FTP exchanges; `internal/identify/plugins.go` infers device type/OS from passive data | Unnecessary scope and protocol-specific maintenance | Remove the active identification runtime and passive classification rules, including confidence/stickiness machinery |
| `decoders/dhcp.go` decodes options 1, 3, 6, 12, 51, 53, 54, 55, 60, 61 and presence of 82, but not domain/search/FQDN/T1/T2/requested-IP data | Incomplete DHCP post-mortem evidence | Add focused DHCPv4 configuration and transaction/lease decoding |
| The schema has a DHCP-server summary but no dedicated structured lease history | Lease-related facts remain scattered in generic observations and can disappear in roll-ups | Add queryable lease/configuration records that survive raw-observation compaction |
| DHCP server configuration is one latest map per server/relay/sender; comparisons consider mask/router/DNS | Different client policies or scopes can appear as a global server configuration change | Preserve per-client ACK snapshots; compare like-for-like scopes and clients |
| `Observation.ProvesPresence()` treats any MAC-bearing observation as presence; correlation calls `seen()` for MAC-bearing DHCP lease observations | A server ACK can make an unobserved client appear directly seen | Separate evidence about a client from frames actually sent by that client |
| `correlate/rules.go:service()` emits `SERVICE_CLOSED` whenever an OPEN result becomes any other result, including TIMEOUT | Post-mortem history overstates what was established | Distinguish REFUSED, TIMEOUT, network errors and local failures; retain last definite result |
| `services` is unique by `(host_id, proto, port)` | Results for two IPs of the same MAC overwrite each other | Key service endpoints by context/IP/transport/port, with time-scoped MAC association |
| `correlate/rules.go:attribute()` can close older IPv4 bindings after a five-minute overlap when another address is learned | Multi-address equipment can look like it keeps changing IP | Preserve simultaneous addresses; label aging as an inference, not a confirmed removal |
| `correlate/names.go` allows one open name per source type | Legitimate mDNS/DNS aliases can become rename churn | Preserve multiple name records with provenance and expiry; derive a display name separately |
| `decoders/dns.go` attaches every mDNS A/AAAA record to the frame sender's MAC and does not retain RR TTL/cache-flush/goodbye semantics | Proxy/reflected responses can misattribute devices; stale aliases persist | Separate advertised name/address facts from independently established MAC ownership |
| Unicast DNS decoding handles reverse PTR only | Forward A/AAAA and CNAME evidence is absent | Add bounded record extraction and targeted resolution, without inferring MAC from the resolver |
| LLDP decoder substitutes the chassis MAC for Ethernet source MAC | A chassis identity can create a synthetic attachment instead of describing the observed port | Keep Ethernet sender MAC as attachment identity; retain chassis/port identifiers as advertised metadata |
| Proxy-ARP safeguards are stored through the identification machinery (`proxyarp.go`) | Deleting classification blindly could remove a connectivity safeguard | Preserve the bounded heuristic as a diagnostic flag, decoupled from device identification |
| TCP is charged three packet tokens; `SO_BINDTODEVICE`, `TCP_SYNCNT` and immediate reset close are implemented | Useful controls, but token accounting does not govern every kernel-generated packet | Make rate guarantees describe probe starts; measure actual traffic under loss and server-initiated data |
| `store/compact.go` eventually deletes oldest events to meet the size target; binding tables are not pruned by that cap | A nominal database cap is not a guaranteed physical cap or history guarantee | Expose retention limits, actual retained coverage, WAL size and storage pressure |

Source paths above are relative to the pinned commit. They identify behaviours found by inspection, not a claim that every possible field failure has been reproduced.

## 3. Scope: retain, remove and deliberately defer

| Capability | Decision | Exact boundary |
|---|---|---|
| Passive ARP and gratuitous ARP | Keep | Sender claims, zero-source probes, candidate targets and conflicts; never treat an ARP request's target as proof of ownership |
| Kernel neighbour dump/watch/resync | Keep | Record neighbour state and its age; a STALE or FAILED cache entry is not a fresh device reply |
| Passive IPv4 source observation | Keep | Ethernet sender is a directly observed attachment; source-IP ownership requires on-link evidence, not merely seeing a routed frame |
| Passive IPv6/NDP already present | Preserve narrowly | No new IPv6 discovery suite; no address-space sweeps; retain scoped IPv6 facts without inferring MAC from IPv6 address bits |
| Passive mDNS and DNS-SD records | Keep and correct | Names, addresses, advertised service names/ports and bounded TXT facts; no device-class rules |
| Targeted active mDNS and DNS | Add narrowly | Queries for known names/addresses plus bounded DNS-SD service-type discovery on configured interfaces |
| DHCPv4 | Strengthen | Passive transaction/configuration analysis and lease timeline; no fake clients or DHCP server role |
| Passive LLDP | Keep as optional facts | System name, sender, chassis/port IDs and advertised management address; no physical-topology engine or device-type classifier |
| ARP active discovery | Keep | Explicit candidate networks, including additional IP subnets sharing the same L2 segment |
| ICMP echo | Keep optional | Additional positive reachability signal; never a prerequisite for TCP checks |
| TCP connection checks | Keep | Configurable ports, connect only, no application payload, immediate bounded close |
| OUI lookup | Optional existing metadata | Label it MAC-prefix registrant/vendor hint, never confirmed equipment manufacturer; no new classifier, live lookups or event stream |
| Human description | Keep | One editable description per context/MAC; audit changes; never replace it with inferred values |
| Active identification probes | Remove | Delete Modbus FC43/14, HTTP GET, TLS/certificate/SNI, SNMP, SSH-banner, Telnet, FTP and their runtime configuration |
| UDP NTP / EtherNet/IP discovery | Remove | Outside the narrowed ARP/name/DHCP/TCP connectivity scope |
| Passive device/OS fingerprints | Remove | Delete hostname, DHCP-option and service-name inference rules and confidence ranking |
| General UDP scanner, SYN scanner, register scanner | Exclude | No payload probing added under another name |
| UI, remote API, cloud/fleet sync, alerts engine | Defer | Local Unix-socket API, CLI and existing low-cardinality operational metrics suffice |
| Automatic address/route/DNS/firewall repair | Exclude | Observe and report; never reconfigure the LAN or observer automatically |
| Continuous PCAP archive, full TCP stream decoding | Exclude | Keep fixture/replay support and structured evidence; no packet-recorder product |
| DHCPv6, broad VLAN/SPAN infrastructure, physical asset graph | Defer | Separate requirements and validation would be needed |

Port 502 may be labelled `modbus` as an operator-configured convention. **TCP/502 OPEN is not proof of Modbus, a PLC or an inverter.** Protocol-specific investigation can be performed manually with OT Fabric's existing tools, including `modbusctl`, `sunspecctl` and `otprobe`. Do not add `go-otfp` as a daemon dependency or invoke these tools automatically.

DHCP, DNS and mDNS are allowed configuration/name exchanges. They are not an exception allowing arbitrary application identification traffic.

## 4. Identity, evidence and time

### Identity contract

1. The stable tracked attachment is **`(network_context, MAC)`**. Internally retain an immutable UUID for references.
2. For this migration, retain the existing per-configured-interface context model. A stable configured interface context persists across daemon restarts; kernel ifindex alone is not a durable identity.
3. IPs, names, DHCP identifiers, descriptions and ports are attributes/evidence, never merge keys. A MAC can have several IPs and names simultaneously. The same IP can be claimed by several MACs.
4. Identical MACs seen on two configured interfaces remain separate attachment records. Do not infer movement or bridge topology from this alone. Historical `MAC_MOVED` events remain readable; new reporting should say “also observed on interface X” unless actual movement is established.
5. A MAC identifies an observed network attachment, not an authenticated physical asset. Two NICs on one machine are separate records; spoofing, duplicated MACs and address randomisation cannot be solved by this identity model.
6. Descriptions attach to context/MAC and survive IP/name changes. MAC-based writes require interface selection when ambiguous. Resolution of an IP/name for a write must resolve exactly one current attachment before committing.
7. Do not automatically move descriptions to another MAC or globally copy them to another context.

### Evidence needs a few explicit distinctions

Use small typed source/relationship fields, not a new probability or scoring system:

- **Direct sender observed:** a frame came from this Ethernet source MAC; record observation time and interface.
- **Address claimed/reachable at MAC:** ARP sender, NDP evidence or a suitably validated kernel-neighbour relation. Proxy ARP remains a possible explanation, so preserve provenance.
- **Server asserted:** DHCP server allocated/renewed this address for the client's chaddr/client identifier. This does not prove the client accepted it or sent traffic.
- **Advertised name/address:** mDNS/DNS/LLDP data describes an address or name; keep advertiser separate from described endpoint.
- **Endpoint answered:** TCP OPEN, TCP REFUSED or ICMP echo reply. Associate with a MAC only when a fresh unambiguous mapping supports it.
- **Last-known/cache-only:** a historical binding or STALE neighbour entry is useful evidence, but not a new sighting.

A gateway frame from a remote IP does not establish that remote IP as the gateway's own address. An IP inside a configured prefix is also insufficient by itself where host routes, relays or proxies exist. Preserve the existing on-link checks, audit them against the routing table, and retain uncertain claims without promoting them to confirmed ownership.

If two MACs claim the probed IP, retain TCP results for the endpoint but do not arbitrarily choose a MAC. Scheduled and queued TCP work must revalidate the binding when it executes and when its result is attributed. An IP can be reassigned between those moments.

### Time and availability

- Preserve observed timestamps, ingestion ordering, event sequence IDs, boot/run identity and existing clock-sync status. Use monotonic time for live pacing/deadlines; simulated time for replay tests.
- Keep a distinction between `last_frame_seen`, `last_address_confirmation`, `last_endpoint_answer` and DHCP/name assertions. A single `last_seen` cannot safely mean all four.
- Retain useful freshness states, with CLI wording such as “not observed since …”. Never translate silence, lease expiry, a failed ping or a collector outage into confirmed power-off.
- Track collector/interface downtime, capture drops, neighbour resync and retention boundaries. Post-mortem output must expose that evidence is incomplete.
- Multiple IPs stay independent. Aging can make a binding stale; an inferred end has `reason`/provenance and must not be presented as an exact remote reconfiguration time. Use definitive close/replacement only with suitable evidence.
- Multiple names are normal. Retain source, record type, address relation, first/last observation and applicable TTL. `preferred_name` is a display choice. It cannot mutate identity or overwrite a human description.
- Do not introduce UUID-name or Android-name rejection heuristics; the original repository records rejected proposals in this area. Fix record semantics and bounded storage instead.

## 5. Passive and active discovery design

### Passive collectors

Retain AF_PACKET capture and netlink behind `internal/platform`. Keep pure bounded decoders and a single normalised observation path shared by live capture and replay. Drop the observer's own outgoing frames as discovery evidence; retain probe provenance through the probe engine.

Capture protocol metadata needed for ARP/DHCP/naming and source learning, rather than arbitrary application payloads. Bound capture lengths, record counts, text sizes and parser work. Oversized or malformed frames must increment operational counters without flooding journald.

An ordinary switched port sees broadcasts, relevant multicast and traffic delivered to the observer. It does **not** see every device's unicast DHCP renewals, DNS responses or application sessions. Promiscuous mode does not turn a switch into a mirror. Document this limitation prominently; completeness requires suitable observation placement or existing SPAN infrastructure configured by an operator.

Use traffic observations to learn candidate addresses. Do not automatically authorise new scan scopes from DHCP, ARP, DNS or mDNS packets.

### Active discovery: ARP first, TCP independent of ping

1. Periodically ARP-sweep explicitly configured candidate networks on the selected interface.
2. Merge replies and passive/neighbour evidence through the same correlation path.
3. Schedule configured TCP endpoints for known or explicitly supplied IP targets, even when ICMP does not answer.
4. Require direct-neighbour attribution before attaching an explicitly targeted IP-only result to a MAC. Keep unresolved results queryable rather than fabricating an attachment.
5. Optional ICMP contributes extra positive evidence; it is never a host-up gate for TCP.
6. Retry with bounded backoff and jitter; successful evidence can restore the normal cadence. Avoid repeated parallel passes and queues of obsolete work.

Default TCP port suggestions: **80, 443, 8080, 502**. Operators can add 22, 102, 4840, 2404, 1883 or any other TCP endpoint they need. Do not activate a large protocol-port catalogue by default. Port labels are configuration metadata.

### Multiple subnets on the same L2 network

Support explicit candidate lists such as `192.168.110.0/24`, `192.168.120.0/24` and `192.168.1.0/24` on one interface. These are candidate IP ranges on one Ethernet segment, not automatically different L2 contexts.

Raw ARP can ask about candidate IPv4 addresses outside the observer's configured subnet. Preserve the existing zero-source ARP-probe fallback where no suitable source address exists, recording that method and testing it on field devices. Do not silently add aliases or routes. Devices may still fail to respond, and a reply does not guarantee usable TCP connectivity.

For TCP, `SO_BINDTODEVICE` selects an interface but does not make a destination directly connected. Check the actual route, selected source address and gateway before execution. In this L2-focused product, scheduled checks require a direct route on the intended interface; a gateway-routed endpoint is out of the scheduled L2 scope. Report missing route/source support explicitly. Even a valid local route does not prove the remote device has a return route.

This preserves usefulness on RevPi-style `/32` field interfaces without taking over their configuration. Operators remain responsible for any necessary direct routes and local source addresses.

### Naming discovery

- Keep mDNS A/AAAA/PTR/SRV and bounded TXT facts with source interface, advertiser, RR TTL and cache-flush/goodbye handling. Implement RFC 6762 grace/cache rules rather than treating every flush bit or TTL zero as an immediate unrelated-record deletion.
- Store an advertised `name -> IP` relation separately from `IP -> MAC`. A proxy/reflected announcement cannot merge attachments. Unresolved advertised addresses remain name records until independently mapped.
- Keep DNS-SD service-instance name distinct from host name; an advertised service and a successfully tested TCP port are different facts.
- Add paced queries for known `.local` names and explicitly configured DNS-SD types. A bounded `_services._dns-sd._udp.local` query may discover service types; cap follow-up work and avoid querying every returned type automatically.
- Add targeted PTR for known IPs and A/AAAA/CNAME for known DNS names through explicitly configured resolvers. Do not send every IP/name observed in OT traffic to public resolvers, and do not rely on a global resolver path that hides egress policy.
- DNS replies describe records, not the resolver's MAC ownership of every answer. Respect positive and negative TTLs, bound CNAME chasing, and do not treat a failed lookup as a rename or device disappearance.
- `.local` queries use the mDNS path. No DNS brute forcing, hostname wordlists, service-payload interrogation, LLM identification or vendor databases.

## 6. Load controls and result semantics

### Precise, modest rate contract

The old packet-token budget is useful machinery but overstates control over kernel traffic. Make the public contract **application-initiated probe starts per second**, with separate TCP connection limits. One raw ARP request, ICMP request, DNS/mDNS query send or TCP connect initiation consumes one shared start token. Application retries consume new tokens. A TCP attempt also consumes its TCP allowance and concurrency slot.

Kernel ARP, handshake ACKs, retransmissions, close packets and acknowledgements of unsolicited server data are not individually paced by the application. Bound timeouts/retries/concurrency and test the observed traffic envelope; do not promise a strict total Ethernet PPS limit. Do not add firewall/tc configuration to manufacture such a guarantee.

Suggested initial operational profile; these are proposed defaults to validate, not measured safety guarantees for every OT device:

| Setting | Initial recommendation |
|---|---|
| Packaged active discovery | Disabled until interfaces/scopes are configured |
| Deployment profile for this use case | Explicitly enable ARP + selected TCP checks; enable name queries as needed |
| Global probe starts | 5/s shared across interfaces, engines, background and CLI-triggered work; no accumulated burst allowance |
| ARP requests | At most 5/s within that shared limit |
| TCP connects | At most 1/s globally; 2 concurrent per interface, 1 per target IP |
| Name-query sends | At most 1/s within the shared limit; reuse TTL cache |
| Total concurrent active jobs | 4 initially; include resolver work |
| Minimum spacing to one target | 5 seconds across direct ARP/ICMP/TCP checks; name questions paced and deduplicated separately |
| ARP sweep cadence | 15 minutes, jittered |
| TCP cadence | 15 minutes per endpoint, jittered; newly discovered hosts enter the same queue |
| TCP connect timeout | 750 ms initially; explicitly configurable for slower links |
| Application timeout retries | None immediately; use next scheduled check/backoff |
| Candidate-scope default | `/24` or narrower; cap the deduplicated union at 1,024 addresses per interface |
| Wider manual scan | Explicit scope/size override, same pacing, exclusions and kill switch; never raises rates implicitly |

ARP and TCP both operate in the first release; “active disabled by default” is an install safeguard, not a reason to defer active functionality. Ship one concise example that enables the intended use case.

The scheduler must not starve known endpoints behind a wide sweep. Deduplicate work by context/target/probe, expire stale jobs, and coalesce an overdue periodic pass rather than queueing catch-up floods. CLI scans share the daemon's scheduler and may not open a separate unmetered scanner.

`scan plan` must show unique target count, exclusions, ports, missing direct routes, estimated duration and whether the requested cadence is achievable. Lower bounds illustrate why this matters: a /24 has approximately 254 candidates, at least 51 seconds at 5 ARP requests/s; 50 hosts × 4 ports = 200 connects, at least 200 seconds at 1 connect/s. A /16 at 5 requests/s takes over 3.6 hours before additional waiting. Actual completion may be slower because of spacing, concurrency, shared budgets and timeouts.

Never widen a scan automatically when a MAC is not found. There is no reliable universal reverse-ARP discovery method for ordinary Ethernet devices; Habets-style MAC-targeted broadcast ICMP is optional external troubleshooting, not a dependency or fallback that unlocks arbitrary scanning.

### TCP result contract

| Observation | What may be concluded | What must not be concluded |
|---|---|---|
| Connection completed | Endpoint accepted a TCP connection at that time | Application protocol or device type identified |
| Immediate connection refusal/reset during connect | REFUSED at that time; endpoint/path actively rejected the connection | Guaranteed rejection by the physical target; a firewall can synthesize rejection |
| Timeout | No connect completed within the configured time | Port closed, host offline or device gone |
| Network/host unreachable | A network-stack or path error was reported | Remote device powered off |
| Local permissions, no usable source, missing interface or resource failure | Probe could not be completed locally | Evidence against remote availability |

Keep `last_result`, `last_result_at`, `last_open_at` and `last_definite_result` independently. A timeout must not erase the fact that the endpoint last definitely answered OPEN. Record transitions without logging every repeated timeout. Prefer `SERVICE_STATE_CHANGED` with exact old/new result; if the public contract retains `SERVICE_CLOSED`, emit it only for a definite REFUSED result and document its meaning.

A bare connection is closed immediately without sending data. Retain the bounded reset-close implementation initially to minimise unrelated changes, but measure it against devices with very small connection tables. A connect still consumes a connection slot and may trigger logging; pace it accordingly. Do not read banners, complete TLS handshakes or send HTTP/Modbus payloads.

## 7. DHCP analysis contract

**DHCP is the area to deepen because it explains connectivity changes, not because it fingerprints equipment.** The first release is DHCPv4, Ethernet clients and existing passive capture. Do not become a DHCP client, server, leasequery client or packet injector.

### Required decoded facts

| Group | Fields/options |
|---|---|
| Header and exchange | BOOTP op/htype/hlen, transaction ID, secs, flags, ciaddr, yiaddr, siaddr, giaddr, chaddr, message type 53 |
| Client identity as observed | chaddr and client identifier 61 separately; hostname 12; client FQDN 81 with its flags; never use these to merge MACs |
| Address/lease | Requested address 50, subnet mask 1, lease time 51, renewal T1 58, rebinding T2 59 |
| Routing | Router list 3, classless static routes 121; retain option 249 when present as a separately labelled compatibility option |
| Naming | DNS server list 6, domain 15, compressed domain-search list 119 |
| Server/relay | Server identifier 54, packet source IP/MAC, giaddr, bounded parsed option-82 circuit/remote identifiers where valid |
| Supporting raw facts | Requested parameter list 55 and vendor class 60 may remain as bounded evidence; no OS/vendor-class classifier |

Recognise DISCOVER, OFFER, REQUEST, DECLINE, ACK, NAK, RELEASE and INFORM. An OFFER is a proposal. A REQUEST is client intent. Only an ACK with an assigned/renewed yiaddr creates the server-asserted lease fact. An INFORM/config-only ACK is not an allocation. A RELEASE/DECLINE/NAK has its own meaning; none automatically proves that an observed static address disappeared.

Support direct client/server exchanges on UDP 67/68 and observed relay/server exchanges on UDP 67/67. Decode direction from validated BOOTP/DHCP fields, not just a 67-to-68 port pattern. A relayed message can describe a client on another link: preserve it as a lease assertion with relay/context provenance, not proof that the client MAC is attached to the collector's Ethernet segment. Keep unknown or non-Ethernet client identities as unbound DHCP evidence rather than fabricating an Ethernet MAC.

Support option overload 52 and RFC 3396 concatenation, including the relevant overloaded fields, rather than only reading the main option area. Bound input size, option aggregates, address lists, route counts and compression-pointer traversal. A truncated packet must not become a complete asserted configuration. Retain a parse/incomplete indicator when salvageable evidence is stored.

Preserve absence versus an empty list versus malformed data. Missing router/DNS/domain options in a partial reply are **unknown**, not an instruction to clear earlier evidence.

### Transaction and lease history

Use bounded pending exchange state keyed by network context, xid and client hardware identity, refined by available client identifier and server/relay evidence. Xid alone is not unique. Do not split a partial reply into a different client merely because an optional client identifier was absent on that packet. Multiple server OFFERs are siblings in one exchange, not separate MAC identities.

Expire pending state and retain summaries; an orphan ACK or REQUEST is still useful and must be marked partial. Deduplicate retransmissions within an exchange without losing evidence of repeated REQUEST/NAK cycles. DHCP packets with different xid/message stage/options must not be suppressed just because the MAC/IP is unchanged.

Store a structured lease/configuration timeline with:

- Context, chaddr/MAC where valid, client identifier, assigned/requested IP and source exchange.
- Server identifier, actual Ethernet sender, relay information and whether any were unknown.
- Observed OFFER/ACK time, lease seconds or infinite/unknown, and calculated expiry **according to the last observed ACK**.
- T1/T2 as advertised, with derived defaults separately labelled if calculated; no negative/overflowed timestamps.
- Mask/prefix, router list, DNS list, domain/search/FQDN and classless routes as an immutable ACK snapshot.
- Release/decline/NAK/renewal evidence and whether the exchange was fully or partly visible.

Lease expiry is a timer based on observed server data. A renewal may have occurred invisibly; show “expiry per last observed ACK”, not “IP definitely released”. Lease records do not extend client direct-presence timestamps.

DHCP's advertised settings are not proof of the client's applied settings. The daemon's own routes/resolver state are yet another data source; keep them distinct. If option 121 is present, interpret its route/default-route semantics under RFC 3442 rather than presenting option 3 as necessarily the effective default gateway. Preserve the raw advertised facts either way. A peer's actual route/DNS configuration is generally unknown without access to that peer.

### DHCP diagnostics, kept focused

- Track observed servers by option 54 with sender and relay kept separate. The Ethernet sender may be a relay, so do not assign the server identifier IP to the relay's MAC without independent address evidence.
- Retain the explicit expected-server allowlist. No configured list means `unchecked`; membership is an operator policy match, not authentication.
- Show competing offers and configuration variants. Multiple servers can be intentional; report observed multiplicity rather than declaring every second server rogue.
- Emit durable changes for per-client acknowledged IP/options and observed NAK/DECLINE outcomes. Repeated identical renewals can be queryable facts without a journal event for each renewal.
- Compare acknowledged configurations for the same client/scope. A server may intentionally send different options to different clients; do not report a global server configuration change merely because their requested option sets differ.
- Keep requested/advertised options distinct from the observer's configuration and from independently confirmed active addresses.

Acceptance includes a broadcast-only capture where REQUEST is visible but a unicast ACK/renewal is not. The output must admit the missing lease evidence. “In-depth” means accurate interpretation of what is visible; it cannot guarantee every lease on a switched LAN.

## 8. Historical model, events and retention

Retain SQLite and the existing single-writer observation/correlation structure. This is a limited evolution of the current model, not a replacement database, message bus or general event-sourcing platform.

### Logical records

| Record | Required role |
|---|---|
| Network context | Durable configured interface context, interface/prefix history; latest local link/route facts |
| Attachment | Immutable host UUID, context/MAC, description and direct-presence times; optional OUI registrant hint |
| Address binding/claim | Context, IP, MAC association, provenance, first/last evidence, freshness and end reason; simultaneous claims allowed |
| Name record | Context, record owner/name/type/value, advertised IP/service relation, source/advertiser, TTL/expiry and observation times |
| TCP endpoint result | Context, IP, TCP port, exact last result and history; time-scoped attachment relation when established |
| DHCP exchange/lease snapshot | Facts from §7, queryable independently of raw observations |
| Observation | Normalised bounded evidence; duplicate suppression where safe |
| Event | Durable material transition with self-contained old/new and causal evidence |
| Collector/scan status | Coverage, dropped data, errors, scheduled work, kill switch and scan summaries |

Keep current and historical queries on shared `store.Reader` methods for both API and `--offline`. Names/leases/endpoints need as-of semantics where the underlying evidence supports them. Do not claim exact state at an arbitrary instant between observations.

### Event policy

Keep useful existing event types and their historical readability. Add only focused types needed to represent exact endpoint changes, DHCP lease/configuration transitions, name-record expiry and observation gaps. Existing `VENDOR_IDENTIFIED` and identification events remain legacy history; the new daemon stops emitting classification events.

Each new event needs event ID/sequence, observed/effective time, context, MAC/host when known, relevant IP/name/port, source, old/new values, reason and enough evidence to understand it after raw pruning. Include the calling local identity for operator changes. Preserve the existing unsynchronised/unknown clock information.

Do not emit one journald entry per packet, renewal, TCP attempt or repeated timeout. Emit state changes; keep repetitive observations in the structured store with bounded refresh and counters. Exact probe-error histories can be retained as observations/summaries within configured retention.

### Retention and storage

- Keep a short raw-evidence window, a longer compacted summary window and longer material-event history. Existing defaults are 7 days raw, 90 days roll-ups, 2 years events and a 200 MB logical database target; retain these as starting values, then measure the added name/DHCP data.
- Preserve DHCP configuration snapshots, material name/address transitions and event evidence beyond raw roll-up. A count-only roll-up is insufficient for reconstructing a gateway/domain change.
- Expose the actual oldest retained raw observation, event and relevant history. Size-driven pruning may shorten configured retention; report this as a coverage limitation.
- Track physical DB, WAL and free disk, not only used SQLite pages. Closed historical bindings/records can grow independently of raw-observation retention; include their growth in the policy and soak test.
- Under pressure, compact/prune according to policy, report dropped history and avoid an uncontrolled write/error loop. If current-state/history data cannot fit the configured cap, report that the cap is unsatisfied rather than claiming it was enforced.
- Events are append-only during normal operation, with explicitly documented retention pruning. No promise of an unlimited or tamper-proof forensic log.

## 9. CLI and configuration contract

Preserve the existing command groups and output formats where useful. Use the same binary as daemon and CLI; keep the local Unix socket. Online writes go through the daemon/correlator; offline access stays read-only.

The desired command surface is:

| Command family | Retain or narrow to |
|---|---|
| `daemon run`, `version`, completion | Existing lifecycle and version information |
| `hosts list/show/find/history/evidence` | MAC/IP/name lookup, description, bindings, naming/lease/endpoint evidence and timeline |
| `hosts set/unset --description` | Human description, exact identity resolution and audited change |
| `events list`, `watch`, `observations list` | Filter by MAC/IP/interface/time/type; stream/export JSONL; raw retention caveat |
| `services list` | Per-IP TCP endpoint results and last definite answer; no protocol/model detection |
| `dhcp servers` | Existing server/relay/config evidence, expected-server policy |
| `dhcp leases` | New structured lease/configuration timeline with client/interface/time filters |
| `interfaces list`, status | Local prefixes/routes, collector coverage and probe route/source problems |
| `scan plan/run`, `active enable/disable` | Shared-budget ARP/ICMP/TCP/name work, explicit scope, cancellable scans |
| `config show/validate/reload` and database tools | Strict configuration, honest restart requirements, integrity/backup support |
| `identify ...` | Remove; no compatibility shim that still probes |

Avoid adding a separate diagnosis engine, report-generator subsystem or a large new command hierarchy. Expose names in host views and structured output rather than requiring a separate naming product.

Illustrative **target CLI**, not assertions that every filter already exists:

```bash
lan-tracker hosts list --interface eth1
lan-tracker hosts show 24:97:45:b7:60:46 --interface eth0
lan-tracker hosts history 24:97:45:b7:60:46 --interface eth0 --since 7d
lan-tracker hosts set 24:97:45:b7:60:46 --interface eth0 \
  --description "Huawei inverter — north building"
lan-tracker services list --interface eth0 --ip 192.168.1.10
lan-tracker dhcp leases --interface eth0 --mac 24:97:45:b7:60:46
lan-tracker events list --interface eth0 --since 24h --output jsonl
lan-tracker active disable --reason "Commissioning in progress"
```

Keep existing `--output table|json|jsonl|csv` and UTC machine timestamps rather than introducing needless flag churn. If a list command adds filters, implement them in `store.Reader` and share the behaviour online/offline. Result names and JSON fields should distinguish `advertised`, `last_known`, `directly_observed` and `tested` facts.

### Configuration changes

- Introduce a documented schema version 2 for the narrowed configuration and changed rate semantics.
- Retain explicit interfaces, passive toggles, candidate networks, exclusions, TCP targets, intervals, freshness/retention settings, expected DHCP servers and the kill switch.
- Remove identification settings, SNI/SNMP/unit-ID options, inference toggles, UDP identification profiles and obsolete counters/settings. Unknown/removed keys are errors in schema 2.
- Preserve existing YAML/env/flag layering and strict validation. Use `LAN_TRACKER_*`; do not silently mix old/new environment prefixes.
- Replace the misleading global packet-rate contract with the start-rate limits from §6. An old packet value is not numerically equivalent to a new connect-start value. The config migration must choose conservative defaults and call out the semantic change rather than copying values mechanically.
- Add only narrow naming-query configuration: enabled interfaces, explicit resolvers, permitted service types, intervals and bounded queries. Reuse the central scheduler/budget rather than creating independent timers/dialers that bypass it.
- Provide one read-only legacy config conversion command or a small documented conversion tool. Its report lists removed functionality, new defaults and fields needing operator attention. It never activates probing or modifies routes.
- Example schema-2 configuration must be tested against the real loader, including an enabled ARP+TCP profile using the four suggested ports. Do not put speculative unimplemented keys into production documentation.
- Reload is transactional. Concurrency semaphore sizes and similar settings that cannot change safely must report `restart_required`; do not accept them while retaining old values without disclosure. The present budget implementation keeps concurrency sizes until restart.

Runtime target paths:

| Item | Target |
|---|---|
| Binary/unit | `/usr/local/bin/lan-tracker`, `lan-tracker.service` |
| Config | `/etc/lan-tracker/config.yaml` |
| Database | `/data/lan-tracker/hosts.db` by default, overridable |
| Socket | `/run/lan-tracker/api.sock` |
| Metrics/environment | `lan_tracker_*`, `LAN_TRACKER_*` |

Keep Linux amd64/arm64, one static binary and the tarball distribution. Do not add packages, an installer service, macOS/Windows daemon support or a second database.

## 10. Code porting map and dependency policy

| Existing paths | Migration action |
|---|---|
| `cmd/lan-sentinel`, module/import paths, build/version strings | Rename to target module/binary and update generated package/release names |
| `internal/platform`, `iface`, `clock`, `service` | Reuse; audit route/source selection, capture lifecycle and cancellation; keep kernel access behind interfaces |
| `internal/collect/capture/{capture,filter,decoder}.go` | Reuse; make DHCP/name dedup semantics explicit; bound parser input and expose drop/coverage data |
| `internal/collect/capture/decoders/{arp,ip,ndp}.go` | Preserve with stronger evidence tests; no target-address ownership assumptions |
| `decoders/{dhcp,dns,lldp}.go` | Focused changes from §§4–7, especially DHCP typed snapshots and mDNS advertiser separation |
| `internal/collect/{neighbor,replay}` | Preserve; neighbour-cache age is not presence; replay all new record semantics |
| `internal/probe/{arp,icmp,tcp}` | Preserve; refine TCP result attribution and bounded execution |
| `internal/probe/{budget,policy,plan,engine}.go`, `probe/scheduler` | Simplify to retained engines plus narrow name queries; one shared budget, route checks and unique target cap |
| `internal/probe/udp` | Remove NTP/EtherNet/IP engines and their config/tests/runtime paths |
| `internal/probe/idprobe` | Remove entire runtime package and its tests/options/CLI/API/scheduler wiring |
| `internal/identify/plugins.go`, `correlate/identify.go` | Remove inference/confidence rules and new classification writes |
| `internal/identify/oui.go`, `ouitable`, `data/oui` | Retain only if providing the optional prefix-registrant hint; move generic MAC validation to a small neutral package if `identify` disappears |
| `internal/correlate/{rules,names,dhcp,state,db,probes}.go` | Preserve ownership of state/events; support multi-address/names, endpoint identity and lease snapshots |
| `internal/correlate/proxyarp.go` | Retain diagnostic safeguards; decouple persistence from `identifications` |
| `internal/store`, `migrations` | Append migrations; preserve old rows and queries while adding focused records/indexes |
| `internal/cli/identify.go`, `api` identify control, SNI helpers | Delete runtime surface; legacy historical records remain readable as legacy evidence |
| `internal/cli/{hosts,lists,describe,active}.go`, `api/client.go`, reader backends | Reuse; add DHCP lease reads and endpoint/name fields without parallel query implementations |
| `internal/config/*`, `deploy/config.yaml` | Schema-2 simplification and explicit legacy conversion; tested examples |
| `internal/events`, `metrics`, `logging` | Keep operational reporting, remove classification output, preserve legacy catalogue readability |
| `test/golden`, `test/frames`, `test/fixtures`, `test/net`, `test/crash`, `test/soak`, `test/systemd` | Adapt and extend behavioural scenarios; do not discard useful safety/replay tests with the removed features |
| `AGENTS.md`, `CLAUDE.md`, `.cursor/rules`, copilot instructions and all docs | Replace the product contract; archive/supersede identification ADR 0011 so agents do not rebuild the removed scope |

Continue using the already chosen libraries: `github.com/gopacket/gopacket`, `github.com/vishvananda/netlink`, `modernc.org/sqlite`, Cobra, `go.yaml.in/yaml/v3`, `x/net`, `x/sys` and systemd integration as needed. No cgo/libpcap dependency, shelling out to nmap/arping/tcpdump, OS fingerprint database or protocol-library dependency is required.

Do not claim dependency reduction solely from deleting probes: much identification code uses the standard library. Remove genuinely unused dependencies with the normal module tooling, then validate. Use the Go version in the source module as the starting point. Check actual current stable dependency/action releases at implementation time; do not downgrade the already updated GitHub Actions majors or paste old versions from a generic template.

## 11. Repository and data migration strategy

### Repository ownership

Preferred maintainer operation: **transfer and rename the existing GitHub repository to `otfabric/lan-tracker`**, preserving history, issues and tags. Do local refactoring in a branch first; a repository transfer is a separate maintainer-controlled action, not something this proposal performs.

If the original repository must remain independently available, create the new repository with preserved Git history and an explicit lineage note, then mark the original as the legacy product. Do not copy only the current files and erase provenance.

Preserve copyright and third-party notices. No top-level LICENSE was present in the reviewed source tree. OT Fabric's stated default is MIT, but public source is not automatically MIT-licensed. Establish the maintainer's licensing authority and put the chosen licence in the migrated repository before public distribution; do not fabricate copyright ownership or permission for contributed code/data.

### Released schema and legacy data

1. **Never edit migrations 0001–0009** from the reviewed baseline. Append after the actual current highest number; a new organisation/module path does not make deployed schemas disposable.
2. Build fixtures from databases produced by each relevant legacy release/schema, especially the latest one. Run migrations on a copy and verify foreign keys, indexes, row counts and historical queries.
3. Keep legacy identification tables/rows readable as historical evidence, but stop adding to them. Do not keep a fingerprint engine merely for compatibility. Physical removal can be a later storage migration with an explicit data policy.
4. Rebuilding an events CHECK constraint must retain all legacy values and indexes; reuse the repository's existing migration pattern and catalogue test.
5. Existing service rows have no IP key. **Do not invent historical endpoint IPs.** Preserve them as legacy host-level summaries unless timestamped observations support an unambiguous attribution. New endpoint-specific history starts when supported evidence exists.
6. Existing names lack full DNS RR TTL/advertiser semantics. Retain them as last-known legacy facts, with TTL unknown. Do not invent original expiration times.
7. Where retained legacy DHCP observations have sufficient fields, backfill lease facts with provenance. Fields not captured by the old decoder, or lost to roll-ups, stay unknown. Do not reconstruct domain/T1/T2 from guesses.
8. Descriptions, host UUIDs, contexts, binding history, event IDs and kill-switch state must survive. Schema migration itself performs no network activity.
9. Keep old events with their original type/wording and mark them as produced by legacy semantics; do not rewrite past `SERVICE_CLOSED` events into supposed proven closures.

### Deployment cutover and rollback

- Stage the new binary, schema-2 config and unit before stopping the old service. Validate config and paths without starting probes.
- Stop the old daemon, checkpoint/close SQLite correctly, and create a consistent backup using SQLite's backup facilities or a cleanly stopped/checkpointed database. Do not copy a live `.db` while ignoring its WAL.
- Prefer migrating a **copy** into the new data path for the first cutover. Keep the original database/config/binary intact for rollback. Record the migration time and retained-data coverage.
- Ensure only one daemon owns each working database and avoid concurrent active scans by old/new daemons. Parallel passive pilots require separate databases and explicit resource planning.
- Start the new daemon with active discovery disabled, verify historical reads/descriptions/leases/status, then enable the reviewed ARP+TCP profile through the normal kill-switch/config mechanism.
- Rollback restores the old binary, unit, config and original compatible database; an old binary must not open the upgraded database. New observations after cutover are not automatically available to the old product; document this rollback boundary.
- Publish operator-visible changes: binary/path/env/metric names, removed commands/options, endpoint semantics, retained legacy data and required script updates. Do not maintain two permanent product names.

Proposed first OT Fabric release: **v0.1.0**, with explicit migration notes from v0.0.9 or the refreshed baseline. Keep legacy tags if transferring the repository and avoid tag collisions.

## 12. Implementation phases and exit gates

Implement in small reviewable changes. Do not combine every dependency update with the semantic refactor. Do not publish, transfer ownership or deploy to devices as part of drafting this plan.

### Phase 0 — Baseline and contract

- Refresh SHA/releases; read repository instructions and capture relevant differences from this brief.
- Run existing `make mod-verify check`, then `make test-net` / `make test-systemd` when available. Record baseline failures honestly.
- Write the narrowed purpose, scope, identity/evidence contract and schema-2 migration rules into the authoritative docs and agent instructions. Supersede identification ADRs and obsolete fixed names.
- Record the licensing/provenance status and maintainer repository-transfer plan.

**Exit:** a small recorded contract in the repository and an explicit baseline report. Existing agent rules are updated so they do not block the authorised removal or instruct another agent to restore it.

### Phase 1 — Rename and remove identification

- Rename module/binary/runtime strings and build/release packaging.
- Remove `idprobe`, passive class/OS inference, UDP NTP/ENIP, identification scheduling/commands/config/SNI/community/unit-ID surfaces and current classification output.
- Preserve description/OUI hint policy, proxy-ARP diagnostic logic, legacy schema readers and shared safety controls.
- Add schema-2 validation and a bounded legacy config conversion report; active remains disabled after conversion.

**Exit:** retained ARP/ICMP/TCP discovery still works; packet tests show no HTTP/TLS/Modbus/SNMP/banner/NTP/ENIP traffic; removed config keys are rejected and legacy history still opens.

### Phase 2 — Correct the connectivity model

- Introduce endpoint-specific result storage and exact result semantics.
- Preserve multi-IP bindings and multi-name records; separate display preference from record identity.
- Separate direct presence, asserted leases, name advertisements and stale cache evidence.
- Correct mDNS advertiser attribution, DNS record/TTL handling and LLDP chassis identity treatment.
- Add focused read-model fields and as-of queries; preserve unknown legacy facts explicitly.

**Exit:** golden scenarios for two IPs on one MAC, same IP across interfaces, duplicate IPs, gateway/proxy advertisements and timeout-versus-refusal reconstruct correctly.

### Phase 3 — Complete bounded discovery and naming

- Put all retained probes and DNS/mDNS query work behind the shared scheduler and start-rate policy.
- Implement route/source checks, deduplicated scan scope, the 1,024-address default cap and explicit wider-scan handling.
- Add targeted DNS/mDNS queries without classifier/payload probing.
- Make scan planning match execution and expose stale/overloaded cadence rather than catch-up queues.

**Exit:** a device that ignores ping is found through ARP and gets TCP checks; off-subnet ARP is possible without aliases; invalid TCP routes are reported; naming traffic and manual scans cannot bypass budgets.

### Phase 4 — DHCP depth and historical queries

- Implement options/header/overload/concatenation parsing and bounded transaction state.
- Add lease/configuration snapshots with ACK/INFORM/relay/client provenance and incomplete-visibility flags.
- Implement `dhcp leases` and the necessary host-history/JSON output.
- Refine server configuration comparisons to avoid false changes across legitimate client policies.

**Exit:** complete and partial exchanges, competing offers, renewals, NAK/DECLINE/RELEASE, changed gateway/domain/search options, classless routes and relay cases replay correctly and survive compaction.

### Phase 5 — Migration, hardening and field validation

- Complete upgrade/copy/rollback tooling and legacy fixtures; adapt systemd/installation documentation.
- Run the required check, fuzz, network, crash, soak, packaging and systemd gates.
- Exercise read-only local route diagnostics, data-pressure handling, clock changes, capture loss and daemon cancellation.
- Conduct passive and active pilots on representative Edgebox/RevPi devices; only actual board/site results close hardware tasks.

**Exit:** executable migration/rollback runbook, measured traffic/resource results, no lost descriptions or invented legacy evidence, and field acceptance for the intended active profile.

## 13. Acceptance matrix

Tests below are behavioural acceptance criteria. Prefer fixtures and the existing network-namespace/container harness; do not write tests that merely mirror implementation details.

| Scenario | Required observable result |
|---|---|
| Quiet device with ICMP disabled, ARP enabled, TCP/502 open | Discovered by ARP; TCP/502 tested without waiting for ping success |
| Known MAC, unknown IP and device sends nothing | Tracker reports the last-known record and candidate-sweep coverage; cannot fabricate an IP or automatically scan IPv4 space |
| One MAC with production IP plus maintenance IP | Both bindings remain visible independently; one TCP port's states on the two IPs do not overwrite each other |
| MAC moves to a new IP while old alias is merely quiet | Evidence/freshness is preserved; no claim that every older address was removed at the new observation time |
| Same IP on eth0 and eth1 | Separate contexts, no false cross-interface conflict |
| Same IP claimed by two MACs on one context | Conflict contains both MACs and evidence; endpoint replies are not arbitrarily assigned to one |
| Gateway forwards public-IP traffic | Public addresses are not recorded as the gateway's own addresses |
| Proxy ARP answering many candidates | Diagnostic evidence retained; no confident manufacturer/role classification; ARP ownership ambiguity remains visible |
| mDNS proxy advertises another device | Name/address records retained with advertiser; no binding of every advertised IP to proxy MAC |
| mDNS aliases, cache flush, goodbye and TTL expiry | Correct record lifecycle with RFC timing; stable attachment identity; no heuristic UUID-name suppression |
| DNS PTR/A/AAAA/CNAME, negative answer and failed resolver | Bounded records/chasing/TTL; no identity merge; failure does not erase device presence |
| LLDP chassis MAC differs from Ethernet sender | Sender remains attachment identity; chassis/management address are metadata |
| TCP OPEN then TIMEOUT then REFUSED | Exact distinct states/times retained; timeout is not `closed`; last definite answer remains queryable |
| TCP result arrives after IP reassignment | Endpoint fact retained; no stale automatic attribution to previous MAC |
| Observer has only /32 field addresses | Raw ARP can search explicit candidates; TCP requires a direct route and source; no configuration mutation |
| DHCP OFFER without ACK | Offered address/config visible; no asserted active lease |
| DHCP ACK visible but no client traffic | Server-asserted lease recorded; client direct-presence not refreshed |
| DHCP INFORM/config-only ACK | Configuration evidence retained; no fabricated allocation or renewal |
| DHCP server identifier differs from relay source | Server and relay remain distinct; no false IP/MAC ownership |
| Relayed DHCP on UDP 67/67, including a client on another link | Exchange is decoded; client lease assertion does not establish local Ethernet attachment or direct presence |
| Two offers, one selected server | Exchange shows both offers and client selection; two servers alone is not a rogue-server verdict |
| Same DHCP server varies options across clients | No false global change; snapshots and per-client changes remain queryable |
| Options 15/81/119, option overload and split options | Correct bounded domain/FQDN/search evidence with malformed/compression tests |
| Option 121 includes default route | Raw options retained; interpreted gateway follows supported classless-route semantics |
| Lease renewal/expiry with hidden unicast packets | Expiry is labelled according to last ACK seen, not proven release/offline |
| Collector goes down during an incident | Gap visible in status/history; no claim of uninterrupted observation |
| Broad manual scan overlaps scheduled probes | Shared rates/concurrency/exclusions; no double-scan burst; planned/actual target counts agree |
| Active disabled, interface removed, config reloaded during a scan | New work stops, in-flight work cancels promptly, no unbounded workers or further payloads |
| Packet loss and slow TCP listeners | Measured retransmission/close envelope; no promise that initiation tokens equal physical packet count |
| Raw observations compacted | Event evidence, relevant DHCP snapshots and name/address transitions still explain the incident |
| Legacy DB upgrade and rollback | Preserves IDs/descriptions/history/kill switch; no invented endpoint IP/TTL/domain; old DB remains usable for rollback |
| Storage pressure, corrupted/malformed input, restart mid-write | Bounded memory/work, honest degraded status, consistent DB and no journald flood |

### Verification gates

- Continue the repository's Linux dev-container workflow; all Go build/test/module commands go through the maintained Makefile targets.
- Run `make mod-verify check` after implementation changes, including race, lint/vulnerability checks and the existing 90% coverage requirement unless the maintainer explicitly changes the policy. Do not lower thresholds to accommodate feature deletion.
- Decoder changes require fuzzing; correlation/storage changes require golden replay, compaction/restart checks and growth measurement.
- Wire/platform/unit changes require `make check-all` or its maintained equivalents (`test-net` and Debian 11/12/13 systemd tests).
- Run crash/soak tests and build static linux/amd64 and linux/arm64 packages. Check version strings, archive contents, checksums and required capabilities.
- Produce captured-wire test evidence for enabled ARP/TCP/name operation and for the absence of removed identification traffic. Account for kernel overhead separately from start rates.
- Retain the 128 MB systemd memory ceiling and constrained CPU deployment as initial envelopes; measure on actual CM4/RevPi hardware and report whether they are met. Synthetic x86/Docker tests do not establish board performance.
- Do not mark pending field checks as complete from replay or container tests. Add a seven-day representative active pilot after passive observation and report device/network load.

## 14. Claude Code execution instructions

Implement this migration and refactor as the scoped product defined here. Treat the current user's narrower requirements as the controlling product direction. The source repository's older identification requirements are being superseded, not accidentally violated.

Work in this order:

1. Inspect current code/instructions and record source SHA and baseline gates. Refresh the findings in this brief against the current checkout.
2. Update the authoritative requirements, ADRs and agent instructions to the new scope. Keep the documentation concise; remove obsolete duplicated specifications rather than multiplying them.
3. Follow phases 1–5 with small coherent changes and matching tests. Preserve the working foundations; do not rewrite the daemon or store wholesale.
4. Keep a short change log with what was removed, what evidence semantics changed, how legacy data/config is handled and which gates passed or could not run.
5. Deliver the refactored source, tested schema/config migrations, example active profile, upgrade/rollback notes and an honest validation report.

Constraints:

- MAC within its network context remains the identity anchor. No merging by hostname/IP/DHCP ID or classifier output.
- No identification probes, passive OS/device classifiers, automatic external tool execution, new payload plugins, hidden runtime compatibility scans or automatic network changes.
- Do not build a UI, cloud service, remote API, asset ontology, distributed store or new public Go framework.
- Keep existing history and descriptions. Use append-only schema migrations, unknown values where evidence is absent and explicit legacy provenance.
- All active work shares one policy, scheduler, cancellation path and configured limits. No ping gate for TCP. No flood or catch-up queues.
- Assertions about leases, advertised names, port results and physical availability must stay distinct.
- Local implementation choices within this contract may be resolved directly. Only escalate an actual product conflict, missing licensing authority, destructive data decision or unavailable hardware validation; do not stop on routine file/module names already decided here.
- Do not publish releases, transfer repositories, push branches or deploy to production devices without the maintainer's instruction. Prepare those actions as reviewable results.

Definition of done: the daemon and CLI can locate and track quiet and chatty OT attachments, show address/name/lease/TCP history with provenance, survive restart and data retention, migrate existing installations safely, and explain uncertainty. Equipment fingerprinting is absent from the runtime.

## 15. Primary sources and review references

Repository observations are from the pinned source above, particularly:

- [Repository instructions](https://github.com/boeboe/lan-sentinel/blob/a437a228c8e58fbf0bb10de509381ea2336448f2/AGENTS.md)
- [DHCP decoder](https://github.com/boeboe/lan-sentinel/blob/a437a228c8e58fbf0bb10de509381ea2336448f2/internal/collect/capture/decoders/dhcp.go)
- [DNS/mDNS decoder](https://github.com/boeboe/lan-sentinel/blob/a437a228c8e58fbf0bb10de509381ea2336448f2/internal/collect/capture/decoders/dns.go)
- [Correlation/address/service rules](https://github.com/boeboe/lan-sentinel/blob/a437a228c8e58fbf0bb10de509381ea2336448f2/internal/correlate/rules.go)
- [Name correlation](https://github.com/boeboe/lan-sentinel/blob/a437a228c8e58fbf0bb10de509381ea2336448f2/internal/correlate/names.go)
- [DHCP server correlation](https://github.com/boeboe/lan-sentinel/blob/a437a228c8e58fbf0bb10de509381ea2336448f2/internal/correlate/dhcp.go)
- [TCP checks](https://github.com/boeboe/lan-sentinel/blob/a437a228c8e58fbf0bb10de509381ea2336448f2/internal/probe/tcp/tcp.go) and [socket implementation](https://github.com/boeboe/lan-sentinel/blob/a437a228c8e58fbf0bb10de509381ea2336448f2/internal/platform/transmit.go)
- [Storage compaction](https://github.com/boeboe/lan-sentinel/blob/a437a228c8e58fbf0bb10de509381ea2336448f2/internal/store/compact.go), [initial schema](https://github.com/boeboe/lan-sentinel/blob/a437a228c8e58fbf0bb10de509381ea2336448f2/migrations/0001_init.sql) and [DHCP schema](https://github.com/boeboe/lan-sentinel/blob/a437a228c8e58fbf0bb10de509381ea2336448f2/migrations/0005_dhcp_servers.sql)
- [OT Fabric principles and repository conventions](https://github.com/otfabric/.github/blob/main/profile/README.md) and the separate [go-otfp/otprobe scope](https://github.com/otfabric/go-otfp)

Protocol interpretation references:

- [RFC 2131 — DHCPv4](https://www.rfc-editor.org/rfc/rfc2131.html): messages, allocation/renewal, relays and client behaviour.
- [RFC 2132 — DHCP options](https://www.rfc-editor.org/rfc/rfc2132.html): mask/router/DNS/domain/lease/T1/T2/overload options.
- [RFC 3396 — Long DHCP options](https://www.rfc-editor.org/rfc/rfc3396.html): concatenation and option-field handling.
- [RFC 3397 — Domain search option](https://www.rfc-editor.org/rfc/rfc3397.html): option 119 and compressed name handling.
- [RFC 3442 — Classless static routes](https://www.rfc-editor.org/rfc/rfc3442.html): route/default-route semantics and interaction with router option.
- [RFC 4702 — Client FQDN](https://www.rfc-editor.org/rfc/rfc4702.html): option 81 and associated flags.
- [RFC 6762 — Multicast DNS](https://www.rfc-editor.org/rfc/rfc6762.html): record lifetimes, goodbye/cache-flush handling, link/interface scope.
- [RFC 6763 — DNS-based service discovery](https://www.rfc-editor.org/rfc/rfc6763.html): service enumeration and record relationships.
- [RFC 5227 — IPv4 address conflict detection](https://www.rfc-editor.org/rfc/rfc5227.html): ARP probes/announcements and conflict semantics.

All rates, retention targets, names, phases and new schema/CLI choices in this brief are proposed product decisions. They are not claims that those exact behaviours exist in the reviewed release or have already passed field testing.
