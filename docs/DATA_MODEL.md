# LAN Tracker — Data Model

Current state and history live in separate tables: binding and record tables carry explicit intervals, `events` is append-only and kept long, the DHCP exchange and lease history and the TCP endpoint history are kept apart from the raw evidence, and `observations` is compacted.

LAN Tracker is a connectivity tracker with history, not an identification product ([ADR 0011](adr/0011-connectivity-tracker-scope.md)). Nothing in this model says what a device is: the MAC is a network attachment, the OUI is a registrant hint, an open TCP port is a completed connect, and the operator's description is the only statement about the equipment.

## 1. Concepts

| Concept | Meaning |
| --- | --- |
| Network context | Where something was seen. In v1 exactly one per monitored interface, stable for the life of that interface name (VLAN out of scope). Every host belongs to one context. Subnet changes do not create a new context; they are recorded in `context_prefixes`. |
| Host | An observed network attachment on one context, identified by exactly one MAC address. Internally keyed by a UUID `host_id`. Within a context, MAC ↔ host is one-to-one and permanent. A host is not an authenticated physical asset. |
| Address | An IP bound to a host over an interval. A host may hold several at once, each with its own evidence. |
| Name record | A name advertised on the context or answered by a resolver, with its record type, the address or value it describes, who advertised it, its TTL and lifecycle; linked to a host only by evidence (§5.4). |
| TCP endpoint | The results of connecting to one address and port on a context, with the MAC that alone held the address at each result (§5.5). |
| DHCP exchange, lease | A client's DHCP transaction as seen, and the timeline of addresses and configurations servers acknowledged for it (§5.6). The server's statement, not the client's presence. |
| DHCP server | A server identity (option 54) seen replying, through which relay and from which sender MAC (§5.6). |
| Observation | Raw evidence: "source S saw MAC X / IP Y / records Z at time T on interface I", with a typed evidence kind (§2). |
| Event | A state transition derived from observations or timers, with a self-contained evidence snapshot. |

### Identity decision

The MAC address is the only evidence that two observations come from the same host. Consequences:

- One host has exactly one MAC per context, for its whole life. A device with two NICs is two hosts. A NIC swap is a new host; the old one ages to MISSING.
- The same MAC seen on two interfaces is two hosts (one per context), linked by a `MAC_ALSO_OBSERVED` event. Two sightings do not establish that the device moved: a bridge, a mirror port and a duplicated MAC look the same.
- The engine never merges or splits hosts, and there are no manual merge/split operations. IPs, names, DHCP client identifiers, descriptions and ports are attributes or evidence, never merge keys.
- A host cannot exist without a MAC. Observations without a MAC only attach to an existing host (§5.1 rule 2). A DHCP lease names a client MAC but never creates its host (§5.6).
- Spoofed, duplicated and randomised MACs cannot be resolved by this model; they are recorded as what was observed.

`host_id` is still a UUID, not the MAC, so that references stay stable, contexts stay separate, and the key does not leak into API paths and metrics.

## 2. Observation

Every collector and probe emits this type and nothing else:

```go
type Observation struct {
    Time          time.Time
    Source        Source            // see below
    Interface     string            // network context key
    MAC           net.HardwareAddr  // the frame's sender; may be nil (e.g. TCP probe, DNS answer)
    IP            netip.Addr        // may be invalid (e.g. LLDP)
    Hostname      string            // a name the sender gave itself (DHCP option 12, LLDP system name)
    NameType      NameType          // mdns, dhcp, dns, dns_ptr, lldp
    Service       *ServiceResult    // tcp, port, OPEN/REFUSED/TIMEOUT/UNREACHABLE/LOCAL_ERROR
    NeighborState string            // kernel_neighbor only: NUD state
    Records       []Record          // name records (mDNS, DNS), §5.4
    DHCP          *DHCPMessage      // the decoded DHCPv4 message, §5.6
    Meta          map[string]string
}
```

### Evidence kinds

What an observation establishes about its MAC is classified by `Observation.Evidence()`. The kinds are kept apart because they prove different things:

| Kind | What it is | Sources |
| --- | --- | --- |
| Frame | A frame came from this Ethernet source MAC | `passive_ipv4`, `passive_ipv6`, `passive_dhcp`, `passive_dhcp_server`, `passive_mdns`, `passive_lldp`, and the ARP and NDP sources below (an ARP or NDP message is also a frame from its sender); any other observation that carries a MAC |
| Address confirmed | An address was claimed or confirmed at this MAC | `passive_arp`, `arp_scan`, `passive_ndp`, `ndp_probe` (with a MAC); `kernel_neighbor` in state REACHABLE (or without a state) |
| Endpoint answer | The endpoint at an IP answered just now | `tcp_connect` OPEN or REFUSED, `icmp_scan` echo reply (no MAC; the holder's only when exactly one MAC holds the IP and its binding was confirmed within `identity.address_expiry`) |
| Cached | A cache's statement about the MAC | `kernel_neighbor` STALE, DELAY or PROBE |
| None | Says nothing about any MAC's presence | `passive_dns`, `dns_query`, `mdns_query` (no MAC); `tcp_connect` TIMEOUT, UNREACHABLE or LOCAL_ERROR |

Only the first three refresh presence (§6). Each host keeps the time each of them last moved: `last_frame_seen`, `last_address_confirmed` and `last_endpoint_answer`; `hosts.last_seen` is the latest of the three and drives presence. A cached entry may record a MAC never seen before (the host is created with `last_seen` at the entry's time and none of the three set), or open an address binding the host never had, but never makes a known host present again or confirms a binding it already has. Silence, a timeout, a local probe failure and an expired lease are never evidence that a host is offline.

### JSONL encoding

Replay files and the golden scenarios store one observation per line, with empty fields omitted:

```json
{"time": "2026-10-01T12:58:00Z", "source": "kernel_neighbor", "interface": "eth1", "mac": "00:1b:1b:aa:bb:01", "ip": "192.168.110.51", "neighbor_state": "REACHABLE"}
{"time": "2026-10-01T14:00:00Z", "source": "tcp_connect", "interface": "eth1", "ip": "192.168.110.50", "service": {"proto": "tcp", "port": 502, "state": "OPEN"}}
```

Fields: `time` (RFC 3339), `source`, `interface`, `mac`, `ip`, `hostname`, `name_type`, `service` (`proto`, `port`, `state`), `neighbor_state`, `records` (each `name`, `type`, and optionally `addr`, `value`, `ttl`, `flush`), `dhcp` (the decoded message, §5.6), `meta`. Unknown fields and unknown sources are rejected, as is a line with neither MAC, IP, hostname nor records. In a replay file `interface` may be omitted (it defaults to the replay interface); naming a different interface is an error.

A `hostname` with a `name_type` (the short form used in hand-written replay files and the golden scenarios) stands for the record it describes: an A or AAAA record for an `mdns` or `dns` name with an address, a PTR for a `dns_ptr` name with an address, else a NAME the sender gave itself. A DHCP observation carries its message only in `dhcp`; one straight from the client also carries the client's hostname (option 12) as `hostname` with name type `dhcp`.

An observation's `time` is the time of the evidence, not of its delivery. For `kernel_neighbor` it is when the kernel last confirmed reachability (`nda_cacheinfo.ndm_confirmed`), so an entry that lingers in the table as STALE carries its old time and adds no fresh evidence.

Replayed observations (pcap or JSONL) keep their recorded source and time; there is no separate replay source, so the correlator treats them exactly as live ones.

The capture decoders and probes put protocol detail that is not identity evidence into `meta`:

| Source | `meta` keys |
| --- | --- |
| `passive_arp` | `arp`: `request`, `reply`, `gratuitous` (sender IP = target IP) or `probe` (sender IP 0.0.0.0, with `target`, the address probed) |
| `passive_ndp` | `ndp`: `router_solicitation`, `router_advertisement`, `neighbor_solicitation`, `neighbor_advertisement` (with `flags`: `router`, `solicited`, `override`) or `dad` (from `::`, with `target`) |
| `passive_lldp` | `chassis_id`, `port_id`, `port_description`, `system_description`, `capabilities`, `management_ip`; the system name is the observation's `hostname` (name type `lldp`). The attachment is the Ethernet sender (the switch port); the chassis ID is metadata |
| `tcp_connect` | `detail`: the reason of a `LOCAL_ERROR` (no permission, no source address, no direct route, a missing interface) |

DHCP observations carry the whole decoded message in `DHCP` instead of `meta` (§5.6); mDNS and DNS observations carry their records in `Records` (§5.4).

Which address an observation carries:

- ARP: the sender address (none for an ARP probe). NDP: the source address of the message, or the target of an advertisement (none for duplicate address detection).
- DHCP: a client message (`passive_dhcp`) sent straight from the client (Ethernet source is `chaddr`, no `giaddr`) carries the client's own claim, the `ciaddr` of a REQUEST or an INFORM (which after an INFORM may be static), and its hostname; a relayed or otherwise indirect request carries the frame's sender (the relay) and its source address, and the client is only in the message. A server reply (`passive_dhcp_server`) carries the frame's sender and IPv4 source: the server, or the relay that forwarded it. No DHCP observation binds an ACK's `yiaddr` to the client: that is lease history (§5.6).
- mDNS (`passive_mdns`): the sender's MAC and IP source address; the advertised addresses are records, never the sender's (§5.4). Unicast DNS answers (`passive_dns`) carry no MAC and no address, only records. Answers to LAN Tracker's own queries: `dns_query` carries records only; `mdns_query` carries the responder's address (the socket sees no frames) and no MAC.
- `passive_ipv4`/`passive_ipv6`: the IP header source. LLDP observations carry no address.
- `arp_scan`: MAC and IP of an ARP reply to a sweep request. `icmp_scan`: the IP of an echo reply. `tcp_connect`: the IP and the connect result.

Sources: `passive_arp`, `passive_ipv4`, `passive_ipv6`, `passive_ndp`, `passive_dhcp`, `passive_dhcp_server`, `passive_mdns`, `passive_dns`, `passive_lldp`, `kernel_neighbor`, `arp_scan`, `icmp_scan`, `ndp_probe`, `tcp_connect`, `dns_query`, `mdns_query`. `ndp_probe` is accepted (in a replay file, for example) and handled like `passive_ndp`, but no engine emits it: there is no NDP solicitation probe (AGENTS.md rule 12).

Events that are not caused by an observation use one of these internal causes instead of a source: `presence` (presence ticker), `expiry` (binding expiry), `iface_monitor` (interface manager), `operator` (kill switch, operator scans, host descriptions), `integrity_check` (start-up database check).

## 3. Time and interval semantics

All timestamps are UTC Unix milliseconds (`INTEGER`). Every binding table (`addresses`, `context_prefixes`) uses the same three columns:

| Column | Nullable | Meaning |
| --- | --- | --- |
| `first_seen` | no | Time of the first observation that opened the binding. Interval start, inclusive. |
| `last_seen` | no | Time of the most recent observation supporting the binding. Updated on every confirming observation. Never NULL. |
| `ended_at` | yes | `NULL` while the binding is open. Set exactly once when it is closed, to the time of the cause (§5.3). Interval end, exclusive. |

`name_records` uses the same three columns with `end_reason` and a TTL expiry (§5.4); `dhcp_leases` uses `first_ack`, `last_ack` and `ended_at` (§5.6).

A binding is **in effect at time T** when `first_seen <= T AND (ended_at IS NULL OR ended_at > T)`. Intervals are half-open, so a takeover that closes one binding and opens another at the same millisecond never overlaps.

A binding in effect at T with `T > last_seen` was **unconfirmed** at T: it was the last known state, but no evidence was received between `last_seen` and T. Queries return `last_seen` so the CLI can show this (§7). An end by expiry is an inference, not the moment the device changed its configuration; the event says so in its `reason`.

`hosts` has `first_seen` and `last_seen` (no `ended_at`: hosts are never closed, only their presence changes), plus the three evidence times of §2.

## 4. Schema (`migrations/0001_init.sql`)

| Table | Columns | Purpose |
| --- | --- | --- |
| `network_contexts` | id, interface, first_seen, last_seen | One row per monitored interface name |
| `context_prefixes` | id, context_id, prefix, first_seen, last_seen, ended_at | The interface's own addresses/subnets over time (CIDR text) |
| `hosts` | host_id (UUID), context_id, mac, oui_registrant, locally_administered, presence, preferred_name, description, proxy_arp, first_seen, last_seen, last_frame_seen, last_address_confirmed, last_endpoint_answer | One row per (context, MAC). `oui_registrant` is the MAC prefix's IEEE registrant (§5.7); `proxy_arp` the diagnostic flag (§5.2); `description` set by an operator (§5.8); the three evidence times of §2 |
| `addresses` | id, context_id, host_id, ip, family, conflict, first_seen, last_seen, ended_at | IP bindings over time |
| `address_sources` | address_id, source, first_seen, last_seen | Which sources confirmed a binding |
| `name_records` | id, context_id, name, name_type, record_type, address, value, source, advertiser_mac, advertiser_ip, host_id, link, ttl_seconds, first_seen, last_seen, expires_at, ended_at, end_reason | Every advertised or resolved name record (§5.4); `host_id` set only by `link` |
| `endpoints` | id, context_id, ip, proto, port, host_id, last_result, last_result_at, last_detail, last_open_at, last_definite_result, last_definite_at, first_seen | One row per TCP endpoint (§5.5); `host_id` the sole fresh holder of the address at the last result (§5.1 rule 2), NULL when none, several or only a stale one |
| `endpoint_results` | id, endpoint_id, ts, result, host_id, detail | Every change of an endpoint's result or of the MAC it was attributed to |
| `dhcp_servers` | id, context_id, server_id, relay, mac, ip, host_id, status, config_json, first_seen, last_seen | DHCP servers seen (§5.6): one row per server identifier (option 54, `''` when unknown), relay (`giaddr`, `''` when not relayed) and sender MAC; `ip` the sender's last address, `host_id` the sender's host; `status` `allowed`, `unexpected` or `unchecked` at the last reply; `config_json` the router, DNS servers, subnet mask, domain name and search list last offered or assigned |
| `dhcp_exchanges` | id, context_id, xid, chaddr, client_id, host_id, first_seen, last_seen, messages, requested_ip, offers_json, selected_server, outcome, visibility, relayed, config_json, lease_id | One row per DHCP transaction seen (§5.6) |
| `dhcp_leases` | id, context_id, chaddr, client_id, host_id, ip, server_id, sender_mac, sender_ip, relay, relay_circuit, relay_remote, first_ack, last_ack, acks, lease_seconds, expires_at, t1_seconds, t2_seconds, t_derived, config_json, partial, incomplete, ended_at, end_reason | A client's lease timeline: one row per address and acknowledged configuration (§5.6) |
| `observations` | id, ts, context_id, source, mac, ip, hostname, name_type, service_json, meta_json, host_id | Raw evidence, compacted; `host_id` NULL if unbound. Records and DHCP messages are not stored here: their lasting content is in `name_records`, `dhcp_exchanges` and `dhcp_leases` |
| `observation_rollups` | id, hour, context_id, host_id, source, mac, ip, count, first_ts, last_ts | Hourly roll-ups of observations |
| `events` | id, ts, type, severity, context_id, host_id, related_host_id, old_value, new_value, cause, observation_id, evidence_json, clock_sync | Permanent history |
| `scans` | id, context_id, kind, trigger, started_at, finished_at, targets, results_json | Operator scans, one row per interface: `kind` the probes (`arp,tcp/502`), `trigger` `operator` (periodic passes are not recorded; the schema also accepts `scheduled`, which nothing writes), `targets` the larger of the sweep and known-host counts, `results_json` `{counts: {probe: {result: n}}, responders, seconds, aborted?}`. A scan left unfinished by a crash is closed at the next start (`finished_at` = `started_at`, aborted, with `SCAN_COMPLETED`) |
| `runtime_state` | key, value, updated_at | State that survives restarts: the kill switch (§8) and the correlator's heartbeat (`observed_until`, §7) |
| `schema_migrations` | version, applied_at | Migration tracking (created by the store, not by a migration) |

MACs are stored as lowercase colon-separated text; IPs as canonical text from `netip.Addr.String()`; prefixes from `netip.Prefix.String()`.

### Context scoping

Every host row carries `context_id`. `address_sources` inherits its context through `host_id` and does not repeat it. `addresses` repeats `context_id` (always equal to `hosts.context_id` of its host; enforced by the writer and a test) so the point-in-time index works without a join. `name_records`, `endpoints`, `dhcp_servers`, `dhcp_exchanges` and `dhcp_leases` carry their own `context_id`, because their `host_id` may be NULL: a record, an endpoint or a lease exists whether or not a MAC is linked to it. `endpoint_results` inherits its context through its endpoint.

### Constraints

| Constraint | Rule |
| --- | --- |
| `network_contexts` | `UNIQUE(interface)` |
| `hosts` | `UNIQUE(context_id, mac)` — the identity rule; `CHECK(proxy_arp IN (0, 1))` |
| `addresses` (open) | `UNIQUE(host_id, ip) WHERE ended_at IS NULL` — one open binding per host and IP |
| `addresses` (closed) | `CHECK(ended_at IS NULL OR ended_at >= first_seen)`, `CHECK(last_seen >= first_seen)` |
| `addresses` (conflict) | Several open bindings for the same `(context_id, ip)` with different hosts are allowed only while a duplicate-IP conflict is active; each has `conflict = 1`. Otherwise at most one open binding per `(context_id, ip)` (enforced by the correlator, verified by tests). |
| `context_prefixes` (open) | `UNIQUE(context_id, prefix) WHERE ended_at IS NULL` |
| `name_records` (open) | Unique on `(context_id, record_type, name COLLATE NOCASE, address, value, source, host_id, advertiser_mac) WHERE ended_at IS NULL`; `CHECK`s on `name_type`, `record_type`, `link`, `last_seen >= first_seen`, `ended_at >= first_seen` |
| `endpoints` | `UNIQUE(context_id, ip, proto, port)`; `proto` is `tcp`; `port` 1–65535; `last_result` one of OPEN, REFUSED, TIMEOUT, UNREACHABLE, LOCAL_ERROR; `last_definite_result` OPEN or REFUSED |
| `address_sources` | `PRIMARY KEY(address_id, source)` |
| `dhcp_servers` | `UNIQUE(context_id, server_id, relay, mac)` |
| `dhcp_exchanges` | `CHECK`s on `outcome`, `visibility` (`complete`, `partial`), `last_seen >= first_seen` |
| `dhcp_leases` (open) | `UNIQUE(context_id, chaddr) WHERE ended_at IS NULL` — one open lease per client per interface; `CHECK(acks > 0)`, `CHECK(last_ack >= first_ack)`, `CHECK(ended_at IS NULL OR ended_at >= first_ack)` |

`observations.host_id`, `events.observation_id`, `dhcp_exchanges.lease_id` and `endpoint_results.host_id` are soft references without foreign keys, because the rows they point at may be pruned.

### Indexes

- `addresses(context_id, ip, first_seen)` — point-in-time lookup
- `addresses(host_id, first_seen)`
- `hosts(mac)` — cross-context MAC lookup (`MAC_ALSO_OBSERVED`, `hosts find <mac>`)
- `name_records(name COLLATE NOCASE)` — name queries are case-insensitive; `name_records(host_id, first_seen)` — every name of a host; `name_records(context_id, address)` — the names of an address
- `endpoints(host_id)`; `endpoint_results(endpoint_id, ts)` — an endpoint's history and `services list --at`
- `dhcp_exchanges(context_id, chaddr, first_seen)`, `dhcp_exchanges(last_seen)`; `dhcp_leases(context_id, chaddr, first_ack)`, `dhcp_leases(context_id, ip, first_ack)` — a client's history and an address's leases
- `events(ts)`, `events(host_id, ts)`, `events(related_host_id, ts)`, `events(type, ts)`, `events(context_id, ts)`, `events(context_id, type, ts)`
- `events(old_value)`, `events(new_value)`, `events(json_extract(evidence_json, '$.ip'))` — IP timelines
- `scans(started_at)` — the last scan (`daemon status`)
- `observations(ts)`, `observations(host_id, ts)`, `observation_rollups(hour)`, `observation_rollups(host_id, hour)`. Observations have no MAC or IP index (they are the high-volume table); `observations list --mac/--ip` walks the time index back from the latest until its limit is filled, so `--host` or `--since` keeps it cheap.

### Point-in-time query

"Who had 192.168.1.200 on eth1 at T?":

```sql
SELECT h.host_id, h.mac, h.preferred_name, h.description,
       a.first_seen, a.last_seen, a.ended_at, a.conflict
FROM addresses a
JOIN network_contexts c ON c.id = a.context_id
JOIN hosts h ON h.host_id = a.host_id
WHERE c.interface = :iface
  AND a.ip = :ip
  AND a.first_seen <= :t
  AND (a.ended_at IS NULL OR a.ended_at > :t)
ORDER BY a.last_seen DESC;
```

| Rows | Meaning |
| --- | --- |
| 0 | No known holder at T. The CLI also shows the previous binding (latest with `ended_at <= T`) and the next one (earliest with `first_seen > T`). |
| 1 | The holder. Flagged *unconfirmed* if `T > last_seen`. |
| ≥ 2 | A duplicate-IP conflict was active at T. All holders are shown, marked CONFLICT, with the `DUPLICATE_IP_DETECTED` event that started it. |

For a binding that ended, the answer also names `replaced_by` (the first binding of the address by another host that started when or after it ended; a claimant during the binding is a conflict, not a successor) and `next_address` (the host's next new address after the end: not proof that it moved). The host's names and leases in effect at T come with it, and its endpoints as they are now. The events for that host and IP around T then explain what changed and the evidence for each step.

## 5. Correlation rules

Applied by the correlator to each observation, in order, within the observation's context. Every rule is evaluated at the observation's `time`; timer-driven transitions (presence §6, expiry §5.3, idle DHCP exchanges §5.6) are stamped with the moment their threshold was crossed. Results therefore do not depend on when evaluation runs, and a replay produces the same database every time.

Before the rules: an observation without a time, or for an interface that has no network context (one never configured), is dropped. A MAC that is not a usable host MAC (multicast, broadcast, all-zero) and an IP that is neither a global nor a link-local unicast address (unspecified, multicast, broadcast, loopback) are treated as absent; an observation left with neither MAC, IP nor records is dropped.

### 5.1 Binding the observation to a host

1. **Observation has a MAC.** The host is the row with `(context_id, mac)`. If none exists, create it and emit `HOST_DISCOVERED`; if the same MAC already exists as a host on another context, also emit `MAC_ALSO_OBSERVED` on the new host with `related_host_id` set to the other host (the one seen most recently). A known host is refreshed (§2: its evidence time, `last_seen` and presence) only by presence evidence; a cached entry records a MAC never seen before but never refreshes a known one.
2. **Observation has no MAC but has an IP** (TCP and ICMP probe results, `mdns_query` answers). If exactly one open binding for that IP exists in the context and it was confirmed within `identity.address_expiry` (24 h) of the observation, attach to that host: a binding nobody confirmed for that long may belong to a device that left, and the observation is not a fresh, unambiguous mapping. If there are none, several (conflict) or only a stale one, the observation is stored with `host_id = NULL` and increments `lan_tracker_observations_unbound_total{source}`. A MAC-less observation never creates a host. It refreshes the host and confirms the binding only when it is an endpoint answer (§2): a `tcp_connect` OPEN or REFUSED, an `icmp_scan` reply. A TCP TIMEOUT, UNREACHABLE or LOCAL_ERROR is no answer: it records the endpoint result (§5.5) but neither refreshes the host nor confirms the binding, so it can neither keep a host ACTIVE nor bring a MISSING one back, and it never counts against the host either. An `mdns_query` answer only adds its records (§5.4).
3. **Observation has neither.** One that carries name records (a `passive_dns` or `dns_query` answer: a resolver's statement about other hosts) is stored unbound and counted the same way, and its records are applied (§5.4). One without records is dropped.

### 5.2 Which IPs are attributed

An IP from an observation is bound to the host only if the observation is presence or cached evidence (§2, not *none*) and the IP is on-link for the context:

- IPv4: inside one of the context's open `context_prefixes`, or the source is `passive_arp`, `arp_scan`, `passive_dhcp` or `kernel_neighbor` (L2 evidence; out-of-subnet ARP from misconfigured devices is still recorded). `passive_dhcp_server` and `passive_mdns` bind their source address like an IP source.
- IPv6: link-local, or inside an open prefix, or the source is `passive_ndp`, `ndp_probe` or `kernel_neighbor`.
- `passive_ipv4` / `passive_ipv6` source addresses outside these rules are ignored for binding, because the frame's source MAC is the router's.
- An address a DHCP server acknowledged (`yiaddr`) is never bound: it is lease history (§5.6). Addresses advertised in mDNS or DNS records are never bound either: they are name records (§5.4).

**Proxy ARP.** Only ARP answers count: `passive_arp` replies and `arp_scan` results. A MAC is flagged `proxy_arp` when its answers within `identity.address_expiry` (default 24 h) claim two or more distinct IPs that are held by other live hosts, or more than `identity.proxy_arp_threshold` (default 16) distinct IPs; older claims are forgotten, so a device that changes address now and then is not a proxy. A late answer (§5.3) is counted but never sets the flag. A single contested answer stays an ordinary duplicate-IP conflict (§5.3). Requests and gratuitous ARP announce the sender's own address: they never count towards proxy ARP, but they do raise duplicate-IP conflicts. The flag is a diagnostic, not an identification: it says the MAC's ARP replies prove nothing about whose addresses they are. When it is set:

- `hosts.proxy_arp` is set to 1 and `PROXY_ARP_DETECTED` records it, with the counts in `reason` and the contested addresses in the evidence;
- the host's bindings that only its ARP answers supported are closed at the observation's time (`IP_REMOVED`, followed by `DUPLICATE_IP_RESOLVED` where a conflict ends);
- from then on its ARP answers open, refresh or take over nothing, except to refresh addresses it holds on other evidence (its own ARP requests, IP traffic, DHCP); they are recorded as observations only.

The flag is kept across restarts (`hosts.proxy_arp`); the claim counts start again after a restart, and so does the knowledge of which bindings only ARP answers supported: bindings open at start-up count as confirmed, so a flag set later does not close them.

### 5.3 Address binding lifecycle

Let *H* be the bound host and *X* the attributed IP. A MAC's addresses are independent: a new address never closes another. Equipment with a production and a maintenance address, or one moving to a new address while the old one is merely quiet, keeps both until each ends on its own evidence.

| Case | Action | Event |
| --- | --- | --- |
| *H* already has an open binding to *X* | Update `last_seen`, upsert `address_sources` (not for cached evidence, which never confirms) | none |
| No open binding to *X* in the context | Open binding *(H, X)*; other bindings of *H* are untouched | `IP_ADDED` (or none if this observation also produced `HOST_DISCOVERED`) |
| **Takeover:** *X* is held open by another host *G* that is not live (presence STALE or MISSING) | Close *(G, X)* with `ended_at = obs.ts`; open *(H, X)* | `IP_REMOVED` on *G* with `related_host_id` = *H*; `IP_ADDED` on *H* (as above) |
| **Conflict:** *X* is held open by another host *G* that is live (ACTIVE or RECENT) | Open *(H, X)*; set `conflict = 1` on both bindings | `DUPLICATE_IP_DETECTED` with both host ids |
| **Conflict ends:** an expiry or a proxy-ARP close leaves exactly one open binding for *X* | Clear `conflict` on it | `DUPLICATE_IP_RESOLVED` |
| **Conflict ends by takeover:** a new MAC takes *X* over and closes every earlier holder, one of which was in conflict | The new binding has no conflict | `DUPLICATE_IP_RESOLVED` on the new holder, the closed conflicting holder as related host |
| **Expiry:** an open binding not confirmed for `identity.address_expiry` (default 24 h) while its host has been observed since | Close with `ended_at = last_seen + address_expiry`, the moment expiry applied | `IP_REMOVED` (cause `expiry`, `reason` `address not confirmed for …`) |

Expiry is an inference: the host kept being seen without confirming the address, so it is taken to have left it; `ended_at` is when the inference applied, not when the device changed its configuration. Bindings are **not** closed when a host becomes MISSING: the last known state remains in effect (and shows as unconfirmed). IPv4 and IPv6 follow the same rules; hosts commonly hold several IPv6 addresses at once. The network and broadcast addresses of the context's IPv4 prefixes (of /30 and shorter) are never attributed.

**Late observations.** An observation older than the last binding change of its host or of its IP (open or close) only refreshes an existing open binding; it never opens, closes or takes over a binding. This keeps evidence that arrives out of order (e.g. an old neighbour-table entry read after an IP change) from reopening history.

### 5.4 Names

Every name the context's traffic advertises or a resolver answers is a row of `name_records`, with its name type, record type, the address or value it describes, the source, the advertiser and the TTL.

| Field | Values |
| --- | --- |
| `name_type` | `mdns` (`passive_mdns`, `mdns_query`), `dns` (forward DNS: A, AAAA, CNAME), `dns_ptr` (reverse PTR), `dhcp` (the client's own hostname, option 12), `lldp` (system name) |
| `record_type` | `A`, `AAAA`, `PTR` (reverse: describes an address; or an mDNS service PTR naming an instance in `value`), `SRV` (`value` `host:port`), `TXT` (`value` the text), `CNAME` (`value` the target), `NAME` (a name a device gives itself outside DNS: its DHCP hostname, its LLDP system name) |
| `address`, `value` | The address an A, AAAA or reverse PTR record describes; the target, text or instance of the others |
| `advertiser_mac`, `advertiser_ip` | The frame's sender; none for a resolver's answer (`passive_dns`, `dns_query`); for `mdns_query` the responder's address only |
| `ttl_seconds`, `expires_at` | The record's TTL as last seen and `last_seen + TTL`; NULL when the source has none (NAME records) |

**Links.** A record is linked to a host (`host_id`) only by evidence, and `link` says which:

| `link` | When |
| --- | --- |
| `sender` | A NAME record, linked to the device that sent it (the DHCP client straight on the link, the LLDP sender) |
| `address` | An A, AAAA or reverse PTR record whose address one MAC alone held when it was seen, with its binding confirmed within `identity.address_expiry` |
| `none` | No MAC held the address, or several did (a conflict), or the record describes no address (SRV, TXT, CNAME, a service PTR) |

The advertiser is never assumed to own what it advertises: an mDNS proxy, reflector or host answering for others advertises addresses that are not its own, and its records bind nothing to it. Records for addresses no MAC holds stay unlinked; `hosts find <name>` shows them as advertised, beside any host the name is linked to.

**Lifecycle.** Several names per host and type are normal. A record seen again refreshes `last_seen`, its TTL, `expires_at` and the advertiser. A host's first open record of a name (`type:name`) emits `HOSTNAME_ADDED`, also when the same observation discovered the host. Records end only on evidence, with `end_reason`:

- `goodbye`: an mDNS record with TTL 0 ends the same record one second later (RFC 6762 §10.1);
- `cache_flush`: an mDNS record with the cache-flush bit ends the other open records of that name, type and source (another address or value) last seen more than a second earlier (RFC 6762 §10.2); they end after the new record is counted, so a name moving to a new address is not reported as removed and added again;
- `address now held by another MAC`: an address record seen again while another single MAC holds the address ends its link to the previous holder, and opens a record linked to the new one;
- `evicted`: beyond 16,384 open records the least recently seen are ended, without an event.

When a host's last open record of a name ends (other than by eviction), `HOSTNAME_REMOVED` gives the reason. Time alone never ends a record. A record whose TTL has run out is shown as *expired* when it is read (API, CLI), and one without a TTL not confirmed for `identity.name_expiry` (default 7 d) as *stale*; both stay in effect and nothing is written. On a switched port DHCP names are often seen only at boot and mDNS names only when someone queries, so not seeing a naming protocol again is not evidence that the name changed. Every read reports a record's freshness: `fresh`, `expired`, `stale` or `ended`.

**`preferred_name`** is a display choice, recomputed whenever a host's names change: of the host's open linked names of the first type listed in `identity.hostname_preference` (default `mdns`, `dhcp`, `dns`, `dns_ptr`, `lldp`) that it has, the one it has held longest (ties by name). Expired and stale names still count; types missing from the list are never preferred. It never changes the host's identity or its description.

### 5.5 TCP endpoints

A `tcp_connect` result is kept per endpoint, the key `(context, ip, proto, port)` in `endpoints`, so the same port on two addresses of one MAC never overwrite each other. The MAC is an attribute of each result, never part of the key: `host_id` is the host that alone held the address when the result came in, with its binding confirmed within `identity.address_expiry` (§5.1 rule 2), NULL when none, several or only a stale one did. An endpoint is recorded either way; a conflict never picks a holder.

- `last_result` and `last_result_at` are the latest result: OPEN (a completed connect), REFUSED (an active refusal), TIMEOUT, UNREACHABLE, or LOCAL_ERROR (the probe could not be made here: no permission, no usable source address, no direct route, a missing interface; `last_detail` says why). LOCAL_ERROR is no evidence about the target.
- `last_definite_result` and `last_definite_at` are the latest OPEN or REFUSED, and `last_open_at` the latest OPEN. They are kept apart from the last result, so a timeout never erases that the endpoint last answered OPEN.
- Every change of the result, of the attributed MAC or of the detail is a row of `endpoint_results`; repeated identical results only move `last_result_at`. A result older than the endpoint's last one changes nothing. `services list --at T` reads the result in effect at T from this history; an endpoint with no result recorded by T (tested later, or its history pruned) is left out, its result then unknown.
- `SERVICE_STATE_CHANGED` marks an endpoint entering or leaving OPEN, with the exact old and new results (e.g. `tcp/502 OPEN` → `tcp/502 TIMEOUT`); a result that is not an answer carries the `reason` `no answer is not a closed port: …`. Changes among the other results are history rows, not events.

Only an OPEN or REFUSED result refreshes the holder (§5.1). OPEN is a completed connect at that time, not proof of any protocol or device: TCP/502 OPEN does not mean Modbus or a PLC.

### 5.6 DHCP

The DHCPv4 decoder reads a whole message into one observation (`DHCPMessage`): the BOOTP header (op, xid, secs, flags, hops, ciaddr, yiaddr, siaddr, giaddr, chaddr when Ethernet) and options 1, 3, 6, 12, 15, 50, 51, 53, 54, 55, 58, 59, 60, 61, 81, 82 (circuit and remote ID), 119, 121 and 249, with options split over several instances joined (RFC 3396) and the `file` and `sname` fields read when option 52 overloads them. `Present` lists the options the message carried: one it did not carry is unknown, never empty. Malformed options are listed and the rest kept; a truncated options field is flagged. Direction comes from the BOOTP op agreeing with the message type, so relay-to-server traffic decodes too. Client identifier, hostname and FQDN are what the client said, never merge keys.

A client message is `passive_dhcp`; a server reply is `passive_dhcp_server`. Both carry the frame's sender (§2), which is the client only when the frame came straight from it. A message without an Ethernet `chaddr` joins no exchange or lease (a server reply still updates `dhcp_servers`).

**Servers.** Every server reply (OFFER, ACK, NAK) is evidence of its sender: the frame's source MAC and IP, which belong to the relay when the reply was relayed. It binds that host like any IP source, and updates `dhcp_servers`, keyed by (context, server identifier, relay, sender MAC):

- A server identifier (option 54) seen for the first time on the context emits `DHCP_SERVER_DISCOVERED`. A reply without a valid identifier has an unknown identity; such replies are told apart by sender MAC, and each new one is discovered.
- A known identifier replying through the same relay (or none) from another sender MAC opens a row for that sender, keeps the earlier one, and emits `DHCP_SERVER_MAC_CHANGED` (a replaced server, or another device using its address). Through another relay it is the same server reached another way: a row, no event. The server identifier is never bound to the sender's MAC.
- `status` is the allowlist verdict, recomputed at every reply. With `interfaces[].dhcp.servers` set, an identifier on the list is `allowed`; any other, and every unknown identity, is `unexpected`; an empty list expects no server at all. Without the key, servers are `unchecked`: reported, never declared authorised. A row becoming `unexpected` (a new one included) emits `DHCP_SERVER_UNEXPECTED`; leaving it emits nothing. A reload changes the allowlist at each server's next reply. The allowlist matches advertised identifiers and authenticates nothing: another device can claim an allowed address, which `DHCP_SERVER_MAC_CHANGED` exposes when it sends from another MAC.
- `config_json` shows the router (`router`, option 3), DNS servers (`dns`, option 6), subnet mask (`subnet_mask`, option 1), domain name (`domain`, option 15) and domain search list (`search`, option 119, comma-separated) last offered or assigned (not from a NAK or the ACK to an INFORM); an option a reply leaves out keeps its last value. It emits no events: a server may answer clients with different options, so configuration is compared per client in the lease history.
- An observation older than the row's `last_seen` changes nothing.

**Exchanges.** Every DISCOVER, OFFER, REQUEST, DECLINE, ACK, NAK, RELEASE and INFORM with an Ethernet `chaddr` joins its transaction in `dhcp_exchanges`, keyed by (context, xid, chaddr): the xid alone is not unique, and a reply without the client identifier its request had is the same exchange.

- `messages` lists the messages in order, each with its server identifier and offered or assigned address, a retransmission counted (`request×3`) rather than repeated; at most 16 distinct messages are kept. `offers_json` holds each server's offer (several offers are siblings in one exchange), `selected_server` the server a REQUEST named, `requested_ip` option 50 or, when renewing, `ciaddr`. `relayed` is set when any message came through a relay; `client_id` and `host_id` (the client's host, if one exists on the interface) are filled in when known. `config_json` is what an ACK to an INFORM advertised, in the same form as a lease snapshot; `lease_id` the lease an ACK opened or renewed.
- `outcome` is the last decisive message: `ack`, `inform_ack`, `nak`, `decline`, `release`, `offered`, or while waiting `discover`, `request` or `inform`. An exchange is closed once it has been idle for 60 s of observation time (checked as observations arrive); one closed while waiting becomes `incomplete`: the answer, if there was one, was not seen. At most 1,024 exchanges are open per interface; the least recently seen is closed first. Open exchanges are not reloaded at start-up: one a restart left waiting becomes `incomplete` when the correlator starts.
- `visibility` is `complete` when both the client's side (DISCOVER, REQUEST or INFORM) and a server's side (OFFER, ACK or NAK) were seen, or for a RELEASE or DECLINE, which expect no answer; otherwise `partial`. A REQUEST without its ACK, or an ACK without its REQUEST, is partial.

**Leases.** An ACK that assigns or renews an address (`yiaddr` set) is the server's statement about the client `chaddr`, recorded in `dhcp_leases`, one row per address and acknowledged configuration:

- The same address, server identifier and configuration as the client's open lease renews that row: `last_ack`, `acks`, `expires_at`, and the lease time and T1/T2 when the ACK carried them. Repeated renewals are counts, not events.
- A different address opens a new row and ends the old one `superseded`, emitting `DHCP_LEASE_ASSIGNED` (old and new address); a client's first lease emits it with no old address. The same address with a different configuration or server identifier also opens a new row, the old one `superseded`, and emits `DHCP_CLIENT_CONFIG_CHANGED` with the settings that differ (and `server_id=` when the server changed).
- Configurations are compared on the options both ACKs carried: an option one of them left out is unknown, not a change.
- A DECLINE (of option 50) or a RELEASE (of `ciaddr`) of the open lease's address ends it, `declined` or `released`. A NAK, a DECLINE and a RELEASE each emit their event (`DHCP_LEASE_NAK` once per exchange, `DHCP_LEASE_DECLINED`, `DHCP_LEASE_RELEASED`) whether or not a lease was open. None of them proves that a static address went away.
- An ACK older than the client's last one changes nothing.

`config_json` is the immutable snapshot of the ACK that opened the row. It holds only the options the ACK carried, under these keys:

| Key | From |
| --- | --- |
| `subnet_mask`, `prefix_length` | Option 1, and its length when it is a contiguous mask |
| `routers` | Option 3, comma-separated |
| `dns` | Option 6, comma-separated |
| `domain` | Option 15 |
| `search` | Option 119, comma-separated |
| `hostname` | Option 12 |
| `fqdn`, `fqdn_flags` | Option 81: the name and its flags (S, O, E, N as a number) |
| `classless_routes`, `ms_classless_routes` | Options 121 and 249, each route `destination via router`, comma-separated; present (possibly empty) when the option was |
| `gateway`, `gateway_source` | The default gateway the ACK implies: with option 121 present its default route (`option_121`; a client must then ignore option 3, RFC 3442, so `gateway` is absent when 121 has no default route), else the first router of option 3 (`option_3`) |
| `relay_circuit`, `relay_remote` | Option 82 sub-options, as hex |
| `malformed_options` | The option codes that did not parse |

The other columns: `client_id` (option 61, from the ACK or its exchange), `host_id` (the client's host if one exists on the interface), `server_id` (option 54, `''` when unknown), `sender_mac` and `sender_ip` (the server, or the relay that sent the ACK), `relay` (`giaddr`) with `relay_circuit` and `relay_remote`. `lease_seconds` is option 51 (`-1` infinite, NULL unknown). `expires_at` is the expiry according to the last observed ACK (`last_ack + lease_seconds`; NULL when the last ACK carried no lease time or an infinite one), never proof that the address was released: a renewal may have happened unseen. `t1_seconds` and `t2_seconds` are options 58 and 59 or, when the ACK carried neither for a finite lease, half and seven eighths of the lease, marked `t_derived`. `partial` marks a lease whose exchange showed no REQUEST; `incomplete` an ACK that was truncated or had malformed options.

A lease neither creates a host nor makes its client present, and it opens no address binding: DHCP's advertised settings are not proof of the client's applied ones. A relayed ACK describes a client that may sit on another link, so its `chaddr` need not be a host of this interface. The client's own traffic (`passive_dhcp` straight from it, including the `ciaddr` it claims in a REQUEST or INFORM) is evidence like any frame (§5.1, §5.2), and `address_sources` records which source confirmed a binding.

Visibility limits what this can prove. On a switched port the box sees broadcast messages and its own exchanges, but messages unicast to other clients (common for renewals) only on a mirror port; promiscuous mode cannot make a switch forward them. A REQUEST whose ACK was unicast stays a `partial` exchange and opens no lease, and not seeing a server is never evidence that there is none. Only DHCPv4 is decoded; LAN Tracker never sends DHCP messages.

### 5.7 OUI registrant

LAN Tracker does not identify equipment (ADR 0011). The OUI lookup (`internal/identify`, the embedded IEEE table) fills `hosts.oui_registrant` at discovery: the registrant of the MAC prefix. It is a hint about the network interface, never the equipment's maker, and no event is written for it. `hosts.locally_administered` records the MAC's U/L bit (set on randomised and locally assigned MACs). There is no device type, model, operating system or confidence anywhere in the model, and no probe or classifier that would produce one.

### 5.8 Host descriptions

An operator can describe a host with free text, such as "Solar panel rooftop" or "Mobile phone Bart": context the network cannot tell, and the only statement LAN Tracker holds about what a device is. `lan-tracker hosts set <host> --description TEXT` sets or replaces it, `hosts unset <host> --description` removes it. The text is trimmed and must be one line of at most 200 characters without control characters. A description belongs to a host, a MAC on an interface: it survives address and name changes, and a device that changes its MAC becomes a new host without it; it is never moved or copied to another MAC or interface. The CLI resolves the query to exactly one current host (with `--interface` or the host ID when several match), and the change goes through the API to the correlator as an operator action, like the kill switch, so the single writer holds: the correlator sets `hosts.description` (NULL when removed) and, when the text changes, emits `HOST_DESCRIBED` with the old and new text and the calling user (`SO_PEERCRED`). The same text again changes nothing. `preferred_name` never overwrites it. Descriptions are never pruned; they show in `hosts show`, `hosts find` and `hosts list` and are searched by `hosts list --description`.

## 6. Presence

| State | Default threshold (time since `hosts.last_seen`) |
| --- | --- |
| ACTIVE | < 5 min |
| RECENT | < 30 min |
| STALE | < 24 h |
| MISSING | ≥ 24 h |

`hosts.last_seen` moves only with presence evidence: a frame from the MAC, an address confirmed at it, or an answer from an endpoint it held alone (§2). Cached entries, DHCP leases, name records, TCP timeouts and local probe failures never move it. Thresholds are configurable (`presence:`). Transitions into MISSING emit `HOST_DISAPPEARED` (cause `presence`) stamped `last_seen + presence.stale`, with old value STALE; presence evidence newer than that brings a MISSING host back and emits `HOST_REAPPEARED`. Transitions between ACTIVE, RECENT and STALE update `hosts.presence` but do not produce events. MISSING means "not observed since", never "powered off". "Live" in §5.2 and §5.3 means ACTIVE or RECENT.

## 7. Event catalogue

Event types are public: the catalogue in `internal/events` lists exactly the types in the `events` CHECK of the newest migration (`TestCatalogueMatchesSchema`). Events are state transitions, never one per observation.

| Type | CLI name | Severity | Meaning |
| --- | --- | --- | --- |
| `HOST_DISCOVERED` | `discovered` | notice | First observation of a new (context, MAC) |
| `HOST_DISAPPEARED` | `disappeared` | notice | Presence became MISSING |
| `HOST_REAPPEARED` | `reappeared` | notice | A MISSING host was observed again |
| `IP_ADDED` | `ip-added` | notice | Host gained an address binding |
| `IP_REMOVED` | `ip-removed` | notice | An address binding was closed: takeover by another MAC, expiry, or a proxy-ARP answer (§5.2, §5.3) |
| `MAC_ALSO_OBSERVED` | `mac-also-observed` | notice | A MAC known on another interface was observed on this one (new host, `related_host_id` = the other) |
| `PROXY_ARP_DETECTED` | `proxy-arp` | warning | A MAC was flagged as answering ARP for other hosts' addresses (§5.2) |
| `HOSTNAME_ADDED` | `name-added` | notice | A host gained a linked name (§5.4) |
| `HOSTNAME_REMOVED` | `name-removed` | notice | A host's last open record of a name ended, with the reason (§5.4) |
| `SERVICE_STATE_CHANGED` | `service-changed` | notice | A TCP endpoint entered or left OPEN (§5.5) |
| `DUPLICATE_IP_DETECTED` | `duplicate-ip` | warning | Two live MACs claim the same IP |
| `DUPLICATE_IP_RESOLVED` | `duplicate-ip-resolved` | notice | Only one binding for a previously conflicting IP remains |
| `SCAN_STARTED` | `scan-started` | info | Scan run began |
| `SCAN_COMPLETED` | `scan-completed` | info | Scan run ended, with counts |
| `ACTIVE_DISABLED` | `active-disabled` | warning | Active discovery stopped by kill switch (API, CLI or env) |
| `ACTIVE_ENABLED` | `active-enabled` | notice | Kill switch cleared |
| `INTERFACE_UP` | `interface-up` | notice | Monitored interface came up |
| `INTERFACE_DOWN` | `interface-down` | warning | Monitored interface went down |
| `SUBNET_CHANGED` | `subnet-changed` | notice | A `context_prefixes` binding opened or closed |
| `DATABASE_RECREATED` | `db-recreated` | warning | The database failed the start-up integrity check; it was quarantined and this new one created (FR-ST-4) |
| `DHCP_SERVER_DISCOVERED` | `dhcp-server-discovered` | notice | A DHCP server identity replied on the interface for the first time (§5.6) |
| `DHCP_SERVER_UNEXPECTED` | `dhcp-server-unexpected` | warning | A DHCP server outside the interface's allowlist, or of unknown identity while an allowlist is set, replied |
| `DHCP_SERVER_MAC_CHANGED` | `dhcp-server-mac-changed` | warning | A known DHCP server identity replied from another sender MAC |
| `DHCP_LEASE_ASSIGNED` | `dhcp-lease` | notice | A server acknowledged a first or a different address for a client (§5.6) |
| `DHCP_CLIENT_CONFIG_CHANGED` | `dhcp-client-config-changed` | notice | A server acknowledged the same address for a client with a different configuration or from another server identifier |
| `DHCP_LEASE_NAK` | `dhcp-nak` | warning | A server refused a client's request |
| `DHCP_LEASE_DECLINED` | `dhcp-decline` | warning | A client declined an address |
| `DHCP_LEASE_RELEASED` | `dhcp-release` | notice | A client released an address |
| `HOST_DESCRIBED` | `described` | notice | An operator set, changed or removed a host's description (§5.8) |
| `OBSERVATION_GAP` | `observation-gap` | warning | Nothing was observed on an interface for a while: a passive collector (capture, neighbour) was not running, or the daemon was not. History inside the gap is unknown, not quiet |

### Values

| Type | `old_value` | `new_value` | Other columns |
| --- | --- | --- | --- |
| `HOST_DISCOVERED` | — | MAC | IP (if any) in evidence |
| `HOST_DISAPPEARED` | `STALE` | `MISSING` | evidence: last observation + `reason` |
| `HOST_REAPPEARED` | `MISSING` | presence after | |
| `IP_ADDED` / `IP_REMOVED` | — / IP | IP / — | `IP_REMOVED` on takeover: `related_host_id` = the new holder; on expiry or proxy ARP: `reason` in evidence |
| `MAC_ALSO_OBSERVED` | other interface | this interface | `related_host_id` = host on the other interface |
| `PROXY_ARP_DETECTED` | — | `proxy_arp` | evidence `reason` (how many addresses it answered for, how many held by other live hosts) and `contested` (those addresses) |
| `HOSTNAME_ADDED` / `HOSTNAME_REMOVED` | — / `type:name` | `type:name` / — | `HOSTNAME_REMOVED`: `reason` = the end reason (§5.4) |
| `SERVICE_STATE_CHANGED` | `proto/port result` before (— for a first result) | `proto/port result` now, e.g. `tcp/502 OPEN` | context of the endpoint; `host_id` = the sole fresh holder (§5.1), none when unattributed; IP in evidence; `reason` when the new result is not an answer |
| `DUPLICATE_IP_DETECTED` | — | IP | `host_id` = new claimer, `related_host_id` = existing holder |
| `DUPLICATE_IP_RESOLVED` | — | IP | `host_id` = remaining holder, `related_host_id` = the host that left |
| `SCAN_STARTED`, `SCAN_COMPLETED` | — | summary text (probes, targets; counts, duration, or why it was aborted) | context of the scanned interface, no host; evidence `{actor, interface}` |
| `ACTIVE_DISABLED`, `ACTIVE_ENABLED` | — | `active discovery disabled by <actor>: <reason>` (`enabled` for `ACTIVE_ENABLED`) | no host or context; evidence `{actor, reason}`; actor `env` when `LAN_TRACKER_ACTIVE_DISABLED=1` stopped it at start-up |
| `INTERFACE_*` | — | `up` / `down` | no host |
| `SUBNET_CHANGED` | prefix closed (— when one opened) | prefix opened (— when one closed) | one event per prefix; cause `iface_monitor`; no host |
| `OBSERVATION_GAP` | when the gap began (RFC 3339) | when it ended | context of the interface, no host; cause `capture` or `neighbor` (the collector failed and came back to `running`, or was still failed at shutdown; evidence `reason` = its last error) or `daemon` (the time from the previous run's last heartbeat to this start, on every configured interface, when at least 1 minute; evidence `reason`). The heartbeat is `runtime_state` `observed_until`, written every 15 s and at shutdown. A collector disabled by configuration or unsupported is no gap; replay records none |
| `DATABASE_RECREATED` | — | quarantined file (`<path>.corrupt-<UTC time>`) | no host or context; evidence `reason` = the integrity-check failure; always the new database's first event |
| `DHCP_SERVER_DISCOVERED` | — | server identity: identifier or `unknown`, plus ` via <relay>` when relayed | `host_id` = the sender (server or relay) |
| `DHCP_SERVER_UNEXPECTED` | previous status (`allowed`, `unchecked`; — for a new server) | server identity | `host_id` = the sender |
| `DHCP_SERVER_MAC_CHANGED` | previous sender MAC | new sender MAC | `host_id` = the new sender, `related_host_id` = the previous one |
| `DHCP_LEASE_ASSIGNED` | previous address (— for a first lease) | acknowledged address | `host_id` = the client's host, when one exists on the interface (also for the other DHCP lease events) |
| `DHCP_CLIENT_CONFIG_CHANGED` | the differing settings before (`dns=… gateway=…`, with `server_id=` first when the server changed) | the same settings now | |
| `DHCP_LEASE_NAK` | the requested address (— when unknown) | `nak from <server>` or `nak` | |
| `DHCP_LEASE_DECLINED` | the declined address (option 50; empty without it) | — | |
| `DHCP_LEASE_RELEASED` | the released address (`ciaddr`) | — | |
| `HOST_DESCRIBED` | the previous description (— for a first one) | the new description (— when removed) | cause `operator`; evidence `{actor, interface, mac}` |

### Provenance

Every event is self-contained, so pruning observations never weakens history:

| Column | Content |
| --- | --- |
| `old_value`, `new_value` | Text form of the changed attribute (IP, name, state, prefix, lease address or settings) |
| `cause` | The observation's `Source`, or an internal cause (`presence`, `expiry`, `iface_monitor`, `operator`, `integrity_check`) |
| `observation_id` | ID of the causing observation if any. May point at a pruned row; informational only. |
| `evidence_json` | Snapshot copied at write time: `{ts, source, interface, mac, ip, hostname, name_type, service: {proto, port, state}, neighbor_state}` of the causing observation, plus `reason` where the event is an inference or needs explaining, and `contested` for proxy ARP. For timer-driven events: the snapshot of the last supporting observation plus `reason` (e.g. `"no observation for 24h"`). For operator events: `{actor, reason}`. |
| `clock_sync` | The kernel clock's state when the event was written (NFR-REL-2): `synced`, `unsynced` (before NTP synchronisation, so `ts` may be off) or `unknown` (the state could not be read, or a replay, whose times come from the recording). Read with a read-only `adjtimex` (`STA_UNSYNC`), whatever the NTP client. |

## 8. Runtime state

`runtime_state` holds state that must survive restarts: `observed_until`, the correlator's heartbeat (§7, `OBSERVATION_GAP`), and `active_disabled` (`{"disabled": true, "reason": "...", "by": "<unix user>", "at": ...}`), which the correlator writes when the API sets the kill switch and deletes when it is cleared. A kill switch set through the API or CLI stays set across daemon restarts until explicitly cleared. `LAN_TRACKER_ACTIVE_DISABLED=1` forces active discovery off regardless of this row and is not written to it (the daemon records `ACTIVE_DISABLED` with actor `env` at start-up instead), and the API refuses to enable while it is set.

## 9. Retention and compaction

Configurable per site under `storage.retention`. Defaults: raw observations 7 days; hourly roll-ups (one row per interface, host, source, IP, MAC and hour, including unbound observations with `host_id` NULL) 90 days; events 2 years.

| Data | Pruned when older than | By |
| --- | --- | --- |
| `observations` | the observations' retention | Folded into `observation_rollups` (adding to an existing row for the same hour), then deleted |
| `observation_rollups` | the roll-ups' retention | `hour` |
| `events` | the events' retention | `ts` |
| `endpoint_results` | the events' retention | `ts` |
| `name_records` that ended | the events' retention | `ended_at` |
| `dhcp_leases` that ended | the events' retention | `ended_at` |
| `dhcp_exchanges` | the roll-ups' retention | `last_seen` |

Open name records and open leases are never pruned, however old; nor are the current-state and binding tables (`network_contexts`, `context_prefixes`, `hosts`, `addresses`, `address_sources`, `endpoints`, `dhcp_servers`, `scans`). An endpoint keeps its last and last definite results in `endpoints` after its history rows are pruned. Raw observations do not hold records or DHCP messages (§4), so the name and DHCP history lives in these tables and survives roll-up.

`max_db_size` (default 200 MB) then applies to the data size (pages minus free pages): while above it, the same steps run in the table's order regardless of age, oldest first: raw observations are rolled up, then roll-ups, exchanges and the history rows (endpoint results, ended name records, ended leases) are deleted, and events last, each table until it is empty before the next: the events, which carry their own evidence, outlive the history they summarise. Size pressure can therefore shorten the configured retention. Freed pages are returned to the file system (`auto_vacuum=INCREMENTAL`). Compaction runs at start-up and hourly in batches of 2,000 rows, each its own committed op, so it never blocks the writer; replay mode does not compact. Because every event carries its evidence (§7), answers and provenance are unchanged by compaction (§10).

## 10. Golden reconstruction scenario

The central correlator fixture, under `test/golden/reconstruction/` (observation stream in, expected bindings, events and query answers out), run by `make check` through the correlator (`correlate_test.go`) and the CLI (`cli_test.go`). Interface `eth1`, prefix `192.168.110.0/24`, `T0 = 2026-10-01T10:00:00Z`, default thresholds. Addresses X = .50, Y = .51, Z = .52.

| # | Time | Observation | Expected |
| --- | --- | --- | --- |
| 1 | T0 | `passive_arp` MAC A, IP X | Host A created; binding A–X open; `HOST_DISCOVERED` A |
| 2 | T0+1h | `passive_dhcp` MAC A, IP Y | A–Y open beside A–X; `IP_ADDED` A Y |
| 3 | T0+2h | `passive_arp` MAC B, IP X | Host B; A is not live (last seen T0+1h), so A–X is closed at T0+2h and B–X opens; `HOST_DISCOVERED` B, `IP_REMOVED` A X (related B) |
| 4 | T0+2h58m | `kernel_neighbor` REACHABLE MAC A, IP Y | A–Y `last_seen` updated; no event |
| 5 | T0+3h | `passive_arp` (gratuitous) MAC C, IP Y | Host C; A is live, so C–Y opens beside A–Y, both `conflict = 1`; `HOST_DISCOVERED` C, `DUPLICATE_IP_DETECTED` Y (C, related A) |
| 6 | T0+3h30m | `passive_dhcp` MAC A, IP Z | A–Z open; A–Y stays open, still in conflict with C–Y; `IP_ADDED` A Z |
| 7 | T0+4h | `tcp_connect` IP X, tcp/502 OPEN (no MAC) | B holds X alone: endpoint (eth1, X, tcp/502) attributed to B, B–X confirmed; `SERVICE_STATE_CHANGED` B `tcp/502 OPEN` |
| 8 | T0+4h | `tcp_connect` IP .99, tcp/502 REFUSED (no MAC) | No holder: unbound counter +1; endpoint recorded without a host; no event |

| Query | Expected answer |
| --- | --- |
| `find X --at T0+30m` | A, binding [T0, T0+2h), unconfirmed (last_seen T0) |
| `find Y --at T0+30m` | No holder; next A from T0+1h; exit 1 |
| `find X --at T0+150m` | B, from T0+2h, open |
| `find Y --at T0+3h10m` | A and C, CONFLICT, both unconfirmed |
| `find Y --at T0+3h40m` | A and C, still CONFLICT: A's new address Z did not end Y |
| `history X` | `HOST_DISCOVERED` A (X), `HOST_DISCOVERED` B (X), `IP_REMOVED` A X (related B), `SERVICE_STATE_CHANGED` B tcp/502 OPEN |
| `history Y` | `IP_ADDED` A Y, `HOST_DISCOVERED` C (Y), `DUPLICATE_IP_DETECTED` Y |
| After 7-day compaction | All answers and event evidence above unchanged (`correlate_test.go`) |
