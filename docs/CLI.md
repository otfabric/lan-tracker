# LAN Tracker — CLI

One cgo-free binary, `lan-tracker`, is the service, the troubleshooting client, the database inspector and the on-demand scanner.

```text
lan-tracker [global options] <command> <subcommand> [options]
```

## 1. How commands reach data

- **Online (default):** commands talk to the running daemon over `/run/lan-tracker/api.sock`. This gives consistent reads and is required for `scan run`, `hosts set|unset`, `active enable|disable`, `config reload`, `watch` and `daemon status`.
- **Offline (`--offline`):** the CLI opens the database directly, read-only. Read commands (`hosts`, `events`, `observations`, `services`, `dhcp`, `interfaces`, `db`, `scan plan`) still work when the daemon will not start. Commands that need the daemon refuse `--offline` with exit 64 and a clear message. `config show` and `config validate` read only files and the environment.

`lan-tracker daemon run` stays in the foreground; systemd owns the process. No double-fork daemonisation.

### Offline database access contract

| Situation | Behaviour |
| --- | --- |
| Daemon running (`hosts.db-wal` present) | Open `file:<path>?mode=ro` with `PRAGMA busy_timeout = 5000` and `PRAGMA query_only = 1`. Never `immutable=1`. Each command runs in a single read transaction, so it sees one consistent snapshot. Requires read access to the directory, `hosts.db`, `-wal` and `-shm` (root, see below). |
| No `-wal` file (daemon stopped cleanly; the last connection checkpoints and removes the WAL) | Open `file:<path>?mode=ro&immutable=1`, because a read-only user cannot create `-shm`. Data is complete. The CLI prints a notice to stderr. If `-wal` appears while the command runs (daemon started), results may be stale; re-run. |
| `-wal` present but not readable, or `-shm` missing and the directory not writable | Exit 2: `cannot open database read-only beside the WAL; run with sudo or copy the database`. |
| Copied database | Copy only after `systemctl stop lan-tracker`, or copy `hosts.db`, `-wal` and `-shm` together. A `hosts.db` copied alone while the daemon was running lacks every transaction since the last checkpoint; the CLI cannot detect this. |
| `SQLITE_BUSY` after the busy timeout | Exit 2 with the SQLite error. |
| Schema older or newer than the binary's | Exit 2: `database schema does not match this binary`. Only the daemon migrates; start it once to migrate an older database. |

The daemon runs as root with `UMask=0027` and the data directory is `0750 root:root` (`deploy/README.md`), so `--offline` needs `sudo`. Offline commands keep their read transaction short; a long-held reader stops WAL checkpoints.

## 2. Global options

| Option | Meaning |
| --- | --- |
| `--config PATH` | Config file (default `$LAN_TRACKER_CONFIG`, else `/etc/lan-tracker/config.yaml`) |
| `--db PATH` | Database path for `--offline` (default from config) |
| `--socket PATH` | API socket (default `api.socket` from the config, `/run/lan-tracker/api.sock`; a config file that does not load leaves the default) |
| `-o`, `--output table\|json\|jsonl\|csv` | Output format (default `table`) |
| `--no-color` | Accepted; the output has no colour |
| `--log-level trace\|debug\|info\|warn\|error` | Log level |
| `--timeout DURATION` | Client timeout (default 10 s) |
| `--offline` | Read the database directly, read-only |
| `--quiet` | Minimal output; rely on exit code |

## 3. v1 commands

| Command | Purpose | Key options |
| --- | --- | --- |
| `daemon run` | Run the service in the foreground | `--config`, `--log-level` |
| `daemon status` | Version, platform, PID, uptime, DB health, interfaces, host counts, kill-switch state, clock synchronisation, last scan, and per-interface collector state (`running`/`disabled`/`unsupported`/`failed`, backend or error); for each probe (`arp`, `icmp`, `tcp`, `names`) its last periodic pass, e.g. `arp running afpacket last pass 2m ago: 253 swept, 14 replied (65 s)` (`asked` for name queries; blocked probes, `cut short` and an overrun when they apply). Passes are shown here only, never logged | `-o json`; `--quiet` exit codes |
| `hosts list` | Inventory: MAC, IP, hostname, OUI registrant, interface, presence, last seen, description. `--active`: live hosts (ACTIVE or RECENT); `--stale`: the others (STALE or MISSING); `--port`: a TCP endpoint of the host on that port last answered OPEN; `--vendor`: substring of the OUI registrant; `--description`: substring of the description | `--interface`, `--active`, `--stale`, `--vendor`, `--description`, `--port`, `--seen-within` |
| `hosts show <query>` | Full record of one current host: addresses over time, names, TCP endpoints, DHCP leases, the description and the evidence kinds behind last seen. The query is the same as `hosts find` and must match exactly one current host | `--interface`, `--ip`/`--mac`/`--hostname`/`--id` |
| `hosts find <query>` | Current or point-in-time holder(s) with addresses, names and TCP endpoints and their sources; for a name, also the records no single MAC holds; exit 1 when no host matches | `--interface`, `--at <time>`, `--ip`/`--mac`/`--hostname`/`--id` |
| `hosts history <query>` | Event timeline for a host, MAC, IP or name | `--interface`, `--since`, `--until`, `--ip`/`--mac`/`--hostname`/`--id` |
| `hosts set <host> --description TEXT` | Set or replace a host's description (one line, at most 200 characters); the host resolved like `hosts find` to exactly one current host | `--interface`, `--ip`/`--mac`/`--hostname`/`--id`, `-o json` |
| `hosts unset <host> --description` | Remove a host's description | `--interface`, `--ip`/`--mac`/`--hostname`/`--id`, `-o json` |
| `hosts evidence <query>` | Why we believe what we believe about a host: per address, name and endpoint the sources, first/last seen and counts, plus the evidence snapshots of its events; every host that ever matched the query; exit 1 when none | `--interface`, `--since`, `--ip`/`--mac`/`--hostname`/`--id` |
| `observations list` | Raw observations within retention; hourly roll-ups with `--rollups` | `--host`, `--interface`, `--mac`, `--ip`, `--source`, `--unbound`, `--since`, `--until`, `--limit`, `--rollups` |
| `events list` | Events on this box, the latest `--limit` (default 1000) in time order | `--since`, `--until`, `--type`, `--interface`, `--mac`, `--ip`, `--limit` |
| `services list` | TCP endpoint results per interface, address and port: the last result, the last definite answer (OPEN or REFUSED) and the MAC that alone held the address | `--interface`, `--ip`, `--port`, `--state`, `--at` |
| `dhcp servers` | DHCP servers seen: identity (option 54), relay, sender MAC and IP, allowlist verdict, advertised router, DNS servers, mask and domain, last seen | `--interface`, `--status` |
| `dhcp leases` | Lease history: each address and configuration a server acknowledged for a client, newest first | `--interface`, `--mac`, `--ip`, `--open`, `--limit` |
| `dhcp exchanges` | DHCP transactions: each client's messages, the offers, the server it selected and the outcome, newest first | `--interface`, `--mac`, `--since`, `--limit` |
| `interfaces list` | State, MAC, current prefixes, passive/active per interface (`replay` for a replayed one), host count | |
| `watch` | Live event stream | `--interface`, `--type`, `--port` |
| `active disable` | Kill switch: stop all active probing now; persists across restarts | `--reason TEXT` (required) |
| `active enable` | Clear the kill switch | `--reason TEXT` |
| `scan plan` | Dry run: what a scan would do, sending nothing | same as `scan run` |
| `scan run` | Operator-triggered scan, bound by the same safety controls | `--interface`, `--network` (repeatable), `--arp`, `--icmp`, `--tcp <port>` (repeatable), `--profile`, `--allow-wide` |
| `config show` | Effective config after merging defaults, file, env and flags | `--sources` |
| `config validate` | Validate a config before rollout | `--config` |
| `config reload` | Make the running daemon re-read its configuration file and print what took effect and what needs a restart | `-o json` |
| `db info` | Path, schema version, journal mode, size, WAL size, row counts | |
| `db check` | SQLite integrity check | |
| `version` | Version, commit, commit date (release builds are reproducible), Go version, architecture; `--quiet` prints the version alone | |
| `completion` | Shell completion script (cobra's generator) | `bash`, `zsh`, `fish`, `powershell` |

There are no merge or split commands: within an interface a host is its MAC (`DATA_MODEL.md` §1).

## 4. Behaviour details

### Query auto-detection (`hosts find`, `hosts show`, `hosts history`, `hosts evidence`)

The argument is classified in this order: host UUID → MAC (any of `aa:bb:..`, `aa-bb-..`, `aabb.ccdd.eeff`) → IPv4 → IPv6 → hostname. Instead of the argument, one of `--ip`, `--mac`, `--hostname`, `--id` gives the query with an explicit kind (e.g. `--hostname cafe.babe.f00d` for a name that looks like a MAC); a value that is not of that kind is a usage error (exit 64). Hostnames match case-insensitively. Multiple matches (e.g. the same IP or MAC on two interfaces) are all shown, grouped by interface, unless `--interface` is given. `hosts show` (and `hosts set`) require exactly one current host: several matches list them with their host IDs and exit 64.

### `hosts show`

`hosts show <query>` prints the full record of one current host. The query is the same as `hosts find` and must match exactly one current host: several matches list them with their host IDs and exit 64, and `--interface` or `--id` picks one; no match is exit 1. `hosts list` does not print host IDs. The record holds:

- the MAC with its OUI registrant (and `locally administered` when the MAC says so), presence, the preferred name, the description, and a proxy-ARP note when the MAC answers ARP for addresses other hosts hold (its ARP replies then prove nothing, `DATA_MODEL.md` §5.2);
- first and last seen, with the evidence kinds behind last seen (`DATA_MODEL.md` §2): `frame` (a frame from the MAC), `address confirmed` (an ARP answer or a REACHABLE neighbour entry) and `endpoint answer` (an endpoint it alone held answered). Each is shown when known;
- every address binding, closed ones too, with its sources and `CONFLICT` (`DATA_MODEL.md` §3, §5.3). A MAC's addresses are independent: a new address does not close another;
- names: each record with its name type (`mdns`, `dns`, `dns_ptr`, `dhcp`, `lldp`), record type (A, AAAA, PTR, SRV, TXT, CNAME, NAME), the address or value it describes, its freshness (`fresh`, `expired` when its TTL ran out, `stale` without a TTL and not seen for `identity.name_expiry`, or `ended`) and when it was last seen (`DATA_MODEL.md` §5.4). An expired or stale name stays in effect; time alone never ends a name;
- TCP endpoints attributed to the host: the last result with its detail and, when it differs, the last definite answer (`DATA_MODEL.md` §5.5);
- DHCP leases the servers acknowledged for the MAC on its interface, newest first: address, server, interval or end reason, ACKs, lease time, expiry, gateway, relay (`dhcp leases`). They are the servers' statements, not proof the host holds the address.

`-o json` prints the `Host` object (`API.md`), including `names`, `endpoints` and `dhcp_leases`.

### `hosts find`

Without `--at`, shows hosts with an open binding for an IP, the host of a MAC, or the hosts that hold a name. A name query also lists, under `Advertised or resolved, but held by no single MAC`, the name records linked to no host (an mDNS reflector, a DNS answer for an address no MAC holds): they are records, not hosts, and do not change the exit code. With `--at T`, uses the point-in-time query in `DATA_MODEL.md` §4:

| Holders at T | Output | Exit |
| --- | --- | --- |
| 1 | The host, its binding interval with sources, `Replaced by:` the next binding for that IP (if any) and `Next new address of this MAC:` the first address the host gained after this binding ended (if any; its addresses are independent, so this is not proof it moved). If `T > last_seen` the binding is marked `unconfirmed since <last_seen>`. | 0 |
| ≥ 2 | Every holder, each binding marked `CONFLICT`, plus the `DUPLICATE_IP_DETECTED` event that covers T | 0 |
| 0 | `No holder of <ip> at T`, plus the previous binding (holder and end time) and the next binding (holder and start time) | 1 |

For a MAC or name query, `--at` shows the host and the addresses and names in effect at T. In JSON the next address is `next_address`.

### `hosts history`

- Host UUID or MAC: every event for that host, including those where it is the related host (the other side of a conflict, or of an address taken over); for a MAC, for each context it appears in.
- IP: every event whose old value, new value or evidence IP (`evidence_json.ip`) is that IP, across all hosts that ever held it, in time order. This is the IP's ownership timeline, including endpoint and conflict events seen on it.
- Name: every event for hosts that ever held that name.

### `hosts evidence`

For each attribute (each address, name and TCP endpoint) the sources that confirmed it, with first/last seen per source (from `address_sources` and roll-ups) and observation counts within roll-up retention. Names add their source, advertiser (when it is not the host itself), TTL and how the record is linked to the host (`sender`, `address`). The host's DHCP leases follow, then the observation counts, then the host's events with their `evidence_json` snapshots, which remain after raw observations are pruned.

### `observations list`

Columns: time, interface, source, MAC, IP, hostname, service, host ID. The latest `--limit` rows, in time order. `--unbound` shows observations that matched no host. Observations older than raw retention are only available with `--rollups` (one row per host, source, MAC, IP and hour, with count). Default `--limit` 1000.

### `services list`

One row per TCP endpoint checked: interface, address and port (`DATA_MODEL.md` §5.5). Columns: `IP`, `PORT`, `LAST RESULT` (`OPEN`, `REFUSED`, `TIMEOUT`, `UNREACHABLE` or `LOCAL_ERROR`, with its detail), `CHECKED` (when), `LAST DEFINITE` (the last `OPEN` or `REFUSED` and when; a timeout never erases it), `MAC` (the MAC that alone held the address at the last result; `-` when none or several did) and `IFACE`. `LOCAL_ERROR` is a failure on this box (no direct route, no source address, permission), not an answer, and nothing was dialled. A timeout is not a closed port, and `OPEN` is a completed connect, not proof of any protocol: tcp/502 `OPEN` is not Modbus. `--state` matches the last result; `--ip` and `--port` select endpoints; `--at T` shows the results in effect at T from the result history, leaving out endpoints with no result recorded by T (tested later, or their history pruned). `MAC` is shown only when that MAC's binding was confirmed within `identity.address_expiry` of the result. Works offline; all four output formats.

### `hosts set` / `hosts unset`

`hosts set <host> --description TEXT` sets or replaces a host's description; `hosts unset <host> --description` removes it (`DATA_MODEL.md` §5.8). The host is a query as for `hosts find` (host ID, MAC, IP or hostname, or an explicit `--ip`/`--mac`/`--hostname`/`--id`) that must match exactly one current host: when it matches several (the same MAC on two interfaces, a duplicate IP) the command lists them with their host IDs and exits 64, and `--interface` or `--id` picks one; no match is exit 1. The text is trimmed and must be one line of at most 200 characters (exit 64 otherwise; an empty `--description` is refused, use `unset`). The change goes through the daemon, which records `HOST_DESCRIBED` with the old and new text and the calling user and answers once it is committed; the same text again records nothing. Both need the daemon (exit 64 with `--offline`); `-o json` prints the host summary.

### `active disable` / `active enable`

`disable` stops all probe engines at once (every probe checks the switch right before it is sent, and running passes and scans are cancelled), writes the persisted kill switch (`DATA_MODEL.md` §8) and emits `ACTIVE_DISABLED` with the reason and the calling Unix user (from `SO_PEERCRED`); it returns once that is committed. `enable` clears it and emits `ACTIVE_ENABLED` (a no-op when it is not set); probing resumes only once that is committed. It fails with exit 2 if `LAN_TRACKER_ACTIVE_DISABLED=1` is set. Enabling the switch does not override per-interface `active.enabled: false`. Both need the daemon; `-o json` prints the resulting state.

### `scan plan`

Accepts the same options as `scan run`, sends nothing, and prints (`-o json` prints the `Plan`):

- interface, requested networks, profile and probes/ports
- targets after excludes: the addresses the ARP sweep covers (without the interface's own) and the known hosts (open IPv4 bindings in the networks) that ICMP and TCP probe, and the excludes that applied
- the route check for TCP: a known host the kernel has no direct route to on the interface (routed via a gateway, no source address on the interface, no route) is listed under `No route:` and left out of the estimate; a run does not dial it and records `LOCAL_ERROR`
- per-protocol rates in probe starts per second, the TCP concurrency caps and the global start cap
- the estimate: probes per protocol and probe starts (every ARP request, ICMP echo or TCP connect is one start; the kernel's TCP packets come on top and are not counted), and two durations, labelled: the **estimated typical duration** and the **estimated no-response duration**
- the assumptions behind the estimate: probes paced at the rates shown, at least the target spacing apart, and at most `active.max_concurrent_jobs` in flight, each holding its slot while it waits for a reply (so with 4 slots and a 1 s ARP timeout a sweep runs at about 4 requests/s, not 5), sending and answering taking no time; for the typical duration, that known hosts answer at once while most swept addresses are empty, so only the ARP sweep waits out its reply timeout; for the no-response duration, that nothing answers, so every phase waits out its reply timeout (named per probe); and that hosts that first answer the sweep are probed too, which adds to a run
- verdict: `ALLOWED` or `REFUSED` with every reason (kill switch set, no interface with active discovery, interface not configured, active discovery disabled on the interface, network not IPv4, outside the interface's configured networks or wider than `max_auto_scan_prefix_v4`, a sweep beyond `active.max_sweep_targets`, a sweep of more than 1,024 addresses without `--allow-wide`, unknown profile, TCP port out of range, no targets left)

With no probe given (no `--arp`, `--icmp`, `--tcp` and no profile) the scan is an ARP sweep; probes given are added to the profile's. A sweep of more than 1,024 addresses (possible only with the expert override `active.max_sweep_targets`) is refused until the plan is repeated with `--allow-wide`: the refused plan is the preview, with the target count and both durations. Without `--interface` every interface with active discovery is planned; with several, each gets its own estimate and duration. Exit 0 if allowed, 2 if refused. Online, the daemon computes the plan from its effective config, the kill switch, the live interface addresses, the kernel's routes and the known hosts. With `--offline`, the CLI computes it from the config file, the persisted kill switch and `LAN_TRACKER_ACTIVE_DISABLED`, and the known hosts in the database; the interfaces' own addresses and routes are unknown offline, so the own addresses count as sweep targets and no route check is made. A daemon in replay mode runs no probes and refuses `scan plan` and `scan run` (exit 2). The ARP responders of a run are probed as well, so a run can probe more hosts than its plan counts.

### `scan run`

Computes the same plan, prints it, and refuses (exit 2) if `scan plan` would. Otherwise it runs through the daemon's scheduler and budgets, interface by interface: the ARP sweep (the known hosts first), then ICMP and TCP port by port on the known hosts and the ARP responders. It prints the estimated typical duration while it runs and returns when the scan completes, with per interface the duration, how many hosts answered the sweep and the results per probe; exit 2 if the scan was aborted (the kill switch was set meanwhile), refused by the daemon, or done but not recorded. Only one operator scan runs at a time; interrupting `scan run` (Ctrl-C) aborts the scan, which is recorded as aborted. Each interface's scan is recorded as a `scans` row with `SCAN_STARTED` and `SCAN_COMPLETED` events; periodic passes are not. Needs the daemon.

### Time arguments

`--since`/`--seen-within` accept durations (`24h`, `7d`, `90m`) counted back from now, or timestamps. `--at`/`--until` accept `YYYY-MM-DD`, `YYYY-MM-DD HH:MM[:SS]` (also with `T` for the space; local time) or RFC 3339. Tables show local time.

### Output formats

Lists (`hosts list`, `hosts history`, `observations list`, `events list`, `services list`, `dhcp servers`, `dhcp leases`, `dhcp exchanges`, `interfaces list`) support all four formats; `json` is the API's objects (`API.md`), `csv` uses RFC 3339 UTC times and empty cells for missing values. Event tables flag the time of an event written before the system clock was synchronised with `*` and one written while that was unknown with `?`, with a legend below; event CSV has a `CLOCK_SYNC` column and JSON the `clock_sync` field. Hostnames and other text come from the network, so a CSV cell starting with `=`, `+`, `-`, `@`, tab or carriage return is prefixed with `'` to keep spreadsheets from running it as a formula. Records (`hosts show`, `hosts find`, `hosts set`, `hosts unset`, `daemon status`, `db info`, `db check`, `scan plan`, `scan run`, `config show`, `config validate`, `config reload`, `version`) support `table` and `json` (`config show` prints YAML as its table); `config show --sources` supports all four; `hosts evidence` also `jsonl`; `watch` prints a line per event or, with `-o jsonl`/`json`, the event objects. `--quiet` prints nothing and leaves the answer to the exit code.

### Online reads

Online, the daemon answers from what its single writer has committed, at most one 5-second batch behind; `watch` is live. Offline and online answers come from the same queries, and each command is one snapshot (`hosts evidence` included). `--mac` and `--ip` filters accept any written form. A socket the caller may not open is exit 2 with a hint to run with `sudo` (the daemon may be running); no daemon on the socket is exit 3.

### `dhcp servers`

One row per DHCP server identity, relay and sender seen in a reply on an interface (`DATA_MODEL.md` §5.6): `SERVER ID` is option 54 (`unknown` when a reply had none or an invalid one), `RELAY` the `giaddr` of relayed replies, `MAC` and `IP` the sender (the server, or the relay), `STATUS` the allowlist verdict at the last reply (`allowed`, `unexpected`, or `unchecked` when the interface has no `dhcp.servers`), then what the server last advertised (`ROUTER`, `DNS`, `MASK`, `DOMAIN`: option 15; JSON adds the domain search list, option 119, as `search`). Known identities come first, the latest sender of each first. The allowlist matches the advertised identifier and does not authenticate a server; two senders for one identity show as two rows (`DHCP_SERVER_MAC_CHANGED`). On a switched port many replies are unicast to the client and never seen, so an empty list does not prove there is no DHCP server. Works offline; all four output formats.

### `dhcp leases`

One row per address and acknowledged configuration a server asserted for a client (an ACK with an address), newest first, at most `--limit` (default 1000) (`DATA_MODEL.md` §5.6). Columns: `CLIENT` (chaddr), `IP`, `SERVER ID`, `RELAY`, `IFACE`, `FIRST ACK`, `LAST ACK`, `ACKS`, `LEASE` (the lease time, `infinite`, or `-` when unknown), `EXPIRES` (the expiry per the last ACK seen, never proof the address was given up), `GATEWAY` (the default route of option 121 when present, else the first router), `DNS` and `STATE`: `open`, or how the row ended (`superseded`, `released`, `declined`), with `partial` when the client's REQUEST was not seen (unicast on a switched port). A renewal with the same address, server and configuration extends its row; another address (`DHCP_LEASE_ASSIGNED`) or configuration (`DHCP_CLIENT_CONFIG_CHANGED`) starts a new one. `--open` keeps the leases in effect; `--mac` selects a client and `--ip` an address. A lease is the server's statement: it creates no host, does not make the client present and does not prove the client holds the address; a relayed ACK describes a client that may be on another link. JSON adds the sender, relay agent information, T1 and T2 (`t_derived` when they were not advertised) and the whole configuration snapshot. Works offline; all four output formats.

### `dhcp exchanges`

One row per DHCP transaction seen (interface, xid and client hardware address), newest first, at most `--limit` (default 1000). Columns: `CLIENT`, `XID`, `IFACE`, `FIRST SEEN`, `MESSAGES` (the messages in order, a server's with its identity and offered address, retransmissions counted), `OFFERS` (how many servers offered), `SELECTED` (the server the client requested), `OUTCOME` (`ack`, `inform_ack`, `nak`, `offered`, `decline`, `release`, or `incomplete` for one closed unanswered after a minute idle) and `VISIBILITY` (`partial` when only one side was seen: on a switched port unicast replies and renewals are not). `--since` selects exchanges seen from that time; `--mac` a client. JSON adds the requested address, each offer and the lease row an ACK produced. Works offline; all four output formats.

### Event type names

`--type` takes the kebab-case CLI names (or the type names themselves, case-insensitively) from `DATA_MODEL.md` §7. Repeatable.

| CLI name | Type |
| --- | --- |
| `discovered`, `disappeared`, `reappeared` | `HOST_DISCOVERED`, `HOST_DISAPPEARED`, `HOST_REAPPEARED` |
| `ip-added`, `ip-removed` | `IP_ADDED`, `IP_REMOVED` |
| `duplicate-ip`, `duplicate-ip-resolved` | `DUPLICATE_IP_DETECTED`, `DUPLICATE_IP_RESOLVED` |
| `mac-also-observed` | `MAC_ALSO_OBSERVED` (the same MAC on a second interface) |
| `proxy-arp` | `PROXY_ARP_DETECTED` |
| `name-added`, `name-removed` | `HOSTNAME_ADDED`, `HOSTNAME_REMOVED` |
| `service-changed` | `SERVICE_STATE_CHANGED` (an endpoint opening or leaving OPEN) |
| `dhcp-server-discovered`, `dhcp-server-unexpected`, `dhcp-server-mac-changed` | `DHCP_SERVER_DISCOVERED`, `DHCP_SERVER_UNEXPECTED`, `DHCP_SERVER_MAC_CHANGED` |
| `dhcp-lease`, `dhcp-client-config-changed` | `DHCP_LEASE_ASSIGNED`, `DHCP_CLIENT_CONFIG_CHANGED` |
| `dhcp-nak`, `dhcp-decline`, `dhcp-release` | `DHCP_LEASE_NAK`, `DHCP_LEASE_DECLINED`, `DHCP_LEASE_RELEASED` |
| `described` | `HOST_DESCRIBED` |
| `scan-started`, `scan-completed`, `active-disabled`, `active-enabled` | `SCAN_STARTED`, `SCAN_COMPLETED`, `ACTIVE_DISABLED`, `ACTIVE_ENABLED` |
| `interface-up`, `interface-down`, `subnet-changed` | `INTERFACE_UP`, `INTERFACE_DOWN`, `SUBNET_CHANGED` |
| `observation-gap`, `db-recreated` | `OBSERVATION_GAP`, `DATABASE_RECREATED` |

### Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Success / healthy |
| 1 | Degraded (`daemon status`, e.g. a configured collector not running), no results for `hosts find` or `hosts evidence`, or no host for `hosts show`, `hosts set` and `hosts unset` |
| 2 | Unhealthy (`daemon status`), command error or scan refused |
| 3 | Daemon unreachable (online mode) |
| 64 | Usage error |

### `config validate` output

Prints interfaces with passive/active mode, active scan networks and excludes with the number of addresses a sweep covers and how long one takes (paced at the ARP rate and limited by the concurrency slots, as `scan plan` estimates), and the DHCP server allowlist (`no allowlist`, `none expected`, or the identifiers); then the enabled probes and ports, the start budgets (global, ARP, ICMP, TCP with its per-interface and per-host caps, jobs in flight), the expert override `active.max_sweep_targets` when it is above 1,024, a warning for each sweep that cannot fit its interval, and the estimated maximum probe start rate (kernel TCP packets come on top). Exit 0 if valid, 2 with per-key errors otherwise. An unknown key or a schema version other than `version: 1` is an error.

### `config reload`

Makes the daemon re-read the configuration file it was started with, exactly as `systemctl reload lan-tracker` (SIGHUP) does, through `POST /v1/config/reload` (`API.md`); `--config` does not change which file. The reload is all or nothing: a file that does not load or validate changes nothing, and the command prints the per-key errors and exits 2. Otherwise it prints the keys whose new values took effect (key, old, new) and the changed keys that need a restart (key, running value, value in the file), which keep their running values; with neither, `No changes.` Exit 0 in both cases. What a reload applies and what needs a restart is FR-CFG-3: each interface's `active` settings (enabling or disabling active discovery, networks, excludes) and `dhcp` allowlist, the probes and their intervals, rates, presence thresholds, identity settings and the log level apply at once; adding, removing or renaming interfaces, their passive settings, `passive`, the concurrency caps (`active.max_concurrent_jobs`, `active.budgets.tcp.max_concurrent_per_interface`, `active.budgets.tcp.max_concurrent_per_host`), `storage`, `api`, `metrics`, `logging.format`, `replay`, `identity.oui_override` and `neighbor.resync_interval` need a restart. Keys are named as in `config show --sources`. The daemon logs the reload with the calling Unix user and the applied keys; its message says what active discovery now runs (`configuration reloaded; active discovery: arp every 15m on eth0 (192.168.0.0/24)`, or `off (replay)` in replay mode). Check the file first with `config validate`, and preview a newly enabled interface's sweep with `scan plan`. Needs the daemon; `-o json` prints `ReloadResult`.

### `config show --sources`

Prints every effective key with where its value came from (default, file path, `LAN_TRACKER_*` variable, or flag).

## 5. Examples

The outputs are real, from `daemon run` replaying two inputs in the dev container, read on 17 Oct 2026, so ages show `15d ago`; host IDs are shortened. The host, history, event, evidence, service, description, `watch` and `config reload` outputs are from the golden reconstruction scenario (`DATA_MODEL.md` §10); its events carry `?` because the replay's clock state is unknown. The name, DHCP, `interfaces list` and `daemon status` outputs are from the synthetic site capture (`test/fixtures`). `scan plan` ran `--offline` over the site capture's database with eth1 configured for active discovery (192.168.110.0/24, excluding .1) and a `modbus` profile (ARP and tcp/502). The `scan run` summary is the CLI tests' (a replay sends nothing).

```bash
$ lan-tracker hosts list
MAC                IP                   HOSTNAME  OUI REGISTRANT      IFACE  STATE   LAST SEEN  DESCRIPTION
00:0c:26:aa:bb:03  192.168.110.51       -         Weintek Labs. Inc.  eth1   STALE   15d ago
00:1b:1b:aa:bb:01  192.168.110.51 (+1)  -         Siemens AG          eth1   STALE   15d ago
00:1b:1b:aa:bb:02  192.168.110.50       -         Siemens AG          eth1   ACTIVE  15d ago
```

```bash
$ lan-tracker hosts find 192.168.110.50
Host ID:     01a14707-3cfd-7a6a-…
Interface:   eth1
MAC:         00:1b:1b:aa:bb:02  (OUI registrant: Siemens AG)
State:       ACTIVE
First seen:  2026-10-01 12:00:00
Last seen:   2026-10-01 14:00:00  (frame 15d ago, address confirmed 15d ago, endpoint answer 15d ago)

Addresses:
  192.168.110.50   2026-10-01 12:00:00 → open   sources: passive_arp, tcp_connect

TCP endpoints:
  192.168.110.50   tcp/502   OPEN        15d ago
```

```bash
$ lan-tracker hosts find 192.168.110.50 --interface eth1 --at "2026-10-01 10:30"
Host ID:     01a14707-3cfd-79b9-…
Interface:   eth1
MAC:         00:1b:1b:aa:bb:01  (OUI registrant: Siemens AG)
Binding:     192.168.110.50   2026-10-01 10:00:00 → 2026-10-01 12:00:00   sources: passive_arp   (unconfirmed since 2026-10-01 10:00:00)
Replaced by: 192.168.110.50 → 00:1b:1b:aa:bb:02 from 2026-10-01 12:00:00
Next new address of this MAC: 192.168.110.52 from 2026-10-01 13:30:00 (its addresses are independent; not proof it moved)
```

```bash
$ lan-tracker hosts find 192.168.110.51 --interface eth1 --at "2026-10-01 13:10"
Host ID:     01a14707-3cfd-7abf-…
Interface:   eth1
MAC:         00:0c:26:aa:bb:03  (OUI registrant: Weintek Labs. Inc.)
Binding:     192.168.110.51   2026-10-01 13:00:00 → open   sources: passive_arp   CONFLICT   (unconfirmed since 2026-10-01 13:00:00)
Conflict:    2026-10-01 13:00:00 DUPLICATE_IP_DETECTED 192.168.110.51 between 00:0c:26:aa:bb:03 and 00:1b:1b:aa:bb:01

Host ID:     01a14707-3cfd-79b9-…
Interface:   eth1
MAC:         00:1b:1b:aa:bb:01  (OUI registrant: Siemens AG)
Binding:     192.168.110.51   2026-10-01 11:00:00 → open   sources: passive_dhcp, kernel_neighbor   CONFLICT   (unconfirmed since 2026-10-01 12:58:00)
Conflict:    2026-10-01 13:00:00 DUPLICATE_IP_DETECTED 192.168.110.51 between 00:0c:26:aa:bb:03 and 00:1b:1b:aa:bb:01
```

```bash
$ lan-tracker hosts find 192.168.110.51 --interface eth1 --at "2026-10-01 10:30"; echo $?
No holder of 192.168.110.51 at 2026-10-01 10:30:00.
Next:        192.168.110.51 held by 00:1b:1b:aa:bb:01 on eth1 from 2026-10-01 11:00:00
1
```

```bash
$ lan-tracker hosts history 192.168.110.50 --interface eth1
TIME                  TYPE                   IFACE  MAC                VALUE           RELATED            CAUSE
2026-10-01 10:00:00?  HOST_DISCOVERED        eth1   00:1b:1b:aa:bb:01  192.168.110.50  -                  passive_arp
2026-10-01 12:00:00?  HOST_DISCOVERED        eth1   00:1b:1b:aa:bb:02  192.168.110.50  -                  passive_arp
2026-10-01 12:00:00?  IP_REMOVED             eth1   00:1b:1b:aa:bb:01  192.168.110.50  00:1b:1b:aa:bb:02  passive_arp
2026-10-01 14:00:00?  SERVICE_STATE_CHANGED  eth1   00:1b:1b:aa:bb:02  tcp/502 OPEN    -                  tcp_connect
? written while the clock's synchronisation was unknown
```

```bash
$ lan-tracker events list --type duplicate-ip
TIME                  TYPE                   IFACE  MAC                VALUE           RELATED            CAUSE
2026-10-01 13:00:00?  DUPLICATE_IP_DETECTED  eth1   00:0c:26:aa:bb:03  192.168.110.51  00:1b:1b:aa:bb:01  passive_arp
? written while the clock's synchronisation was unknown
```

```bash
$ lan-tracker hosts evidence 00:0c:26:aa:bb:03
Host 01a14707-3cfd-7abf-…  00:0c:26:aa:bb:03  on eth1

Addresses:
  192.168.110.51   2026-10-01 13:00:00 → open   CONFLICT
      passive_arp      first 2026-10-01 13:00:00  last 2026-10-01 13:00:00

Observations (raw and roll-ups):
  passive_arp      192.168.110.51        1  first 2026-10-01 13:00:00  last 2026-10-01 13:00:00

Events:
  2026-10-01 13:00:00  HOST_DISCOVERED        192.168.110.51  (passive_arp)
      evidence {"interface":"eth1","ip":"192.168.110.51","mac":"00:0c:26:aa:bb:03","source":"passive_arp","ts":"2026-10-01T13:00:00Z"}
  2026-10-01 13:00:00  DUPLICATE_IP_DETECTED  192.168.110.51  (passive_arp)
      evidence {"interface":"eth1","ip":"192.168.110.51","mac":"00:0c:26:aa:bb:03","source":"passive_arp","ts":"2026-10-01T13:00:00Z"}
```

```bash
$ lan-tracker services list
IP              PORT     LAST RESULT  CHECKED  LAST DEFINITE    MAC                IFACE
192.168.110.50  502/tcp  OPEN         15d ago  OPEN 15d ago     00:1b:1b:aa:bb:02  eth1
192.168.110.99  502/tcp  REFUSED      15d ago  REFUSED 15d ago  -                  eth1
```

```bash
$ lan-tracker hosts show 00:1b:1b:aa:bb:01
Host ID:     01a14707-a908-7554-…
Interface:   eth1
MAC:         00:1b:1b:aa:bb:01  (OUI registrant: Siemens AG)
State:       STALE
Name:        plc-a.local
First seen:  2026-10-01 10:00:00
Last seen:   2026-10-01 12:50:00  (frame 15d ago, address confirmed 15d ago)

Addresses:
  192.168.110.50   2026-10-01 10:00:00 → 2026-10-01 12:00:00   sources: passive_arp, passive_mdns
  192.168.110.51   2026-10-01 12:50:00 → open   sources: passive_arp   CONFLICT

Names:
  plc-a.local              mdns    A     192.168.110.50   expired 15d ago

DHCP leases (the servers' statements, not proof the host holds the address):
  192.168.110.52   from 192.168.110.2   2026-10-01 13:30:00 → open  acks 1  lease 1h0m0s  expires 2026-10-01 14:30:00  gateway 192.168.110.1
  192.168.110.51   from 192.168.110.2   2026-10-01 11:00:00 → superseded 2026-10-01 13:30:00  acks 1  lease 1h0m0s  gateway 192.168.110.1
```

```bash
$ lan-tracker dhcp leases
CLIENT             IP              SERVER ID      RELAY  IFACE  FIRST ACK            LAST ACK             ACKS  LEASE   EXPIRES              GATEWAY        DNS                          STATE
00:1b:1b:aa:bb:01  192.168.110.52  192.168.110.2  -      eth1   2026-10-01 13:30:00  2026-10-01 13:30:00  1     1h0m0s  2026-10-01 14:30:00  192.168.110.1  192.168.110.2,192.168.110.4  open,partial
00:1b:1b:aa:bb:01  192.168.110.51  192.168.110.2  -      eth1   2026-10-01 11:00:00  2026-10-01 11:00:00  1     1h0m0s  2026-10-01 12:00:00  192.168.110.1  192.168.110.2                superseded,partial
00:0e:8c:11:22:33  192.168.110.20  192.168.110.2  -      eth1   2026-10-01 10:10:02  2026-10-01 10:10:02  1     1h0m0s  2026-10-01 11:10:02  192.168.110.1  192.168.110.2                open,partial
```

```bash
$ lan-tracker dhcp exchanges --interface eth1
CLIENT             XID         IFACE  FIRST SEEN           MESSAGES                                    OFFERS  SELECTED  OUTCOME  VISIBILITY
00:1b:1b:aa:bb:01  0x00005e5e  eth1   2026-10-01 13:30:00  ack(192.168.110.2 192.168.110.52)           0       -         ack      partial
00:0e:8c:11:22:33  0x00005e5e  eth1   2026-10-01 12:30:00  offer(192.168.110.66 192.168.110.200)       1       -         offered  partial
00:1b:1b:aa:bb:01  0x00005e5e  eth1   2026-10-01 11:00:00  ack(192.168.110.2 192.168.110.51)           0       -         ack      partial
00:0e:8c:11:22:33  0x00005e5e  eth1   2026-10-01 10:10:01  discover ack(192.168.110.2 192.168.110.20)  0       -         ack      complete
```

```bash
$ lan-tracker scan plan --interface eth1 --profile modbus --offline
Interface:  eth1    Profile: modbus    Networks: 192.168.110.0/24
Targets:    253 to sweep (excluded: 192.168.110.1), 5 known hosts
Probes:     arp; tcp/502
Rates:      arp 5 starts/s, tcp 1 connects/s (≤2 per interface, ≤1 per host); global cap 5 probe starts/s
Estimate:   253 ARP requests, 5 TCP connects: 258 probe starts (kernel TCP packets come on top)
Duration:   estimated typical ~68 s, estimated no-response ~69 s
Assumes:    probes start paced at the rates above, at most 5 starts/s in all, at least 5s apart per target and at most 4 in flight, each holding its slot while it waits for a reply; sending, connecting and answering take no time; the kernel's TCP handshake, retransmission and close packets are not counted
            typical: known hosts answer at once; most swept addresses are empty, so the ARP sweep waits its 1s reply timeout once, after its last request
            no-response: nothing answers, so every phase waits out its reply timeout once (ARP 1s, tcp/502 750ms)
            ICMP and TCP counts use the hosts known now; hosts that first answer the sweep are probed too, which adds to a run
Verdict:    ALLOWED
```

```bash
$ lan-tracker scan plan --interface eth1 --network 10.0.0.0/16 --offline; echo $?
Interface:  eth1
Probes:     arp
Rates:      arp 5 starts/s; global cap 5 probe starts/s
Estimate:   nothing to send
Verdict:    REFUSED
  - 10.0.0.0/16 is wider than max_auto_scan_prefix_v4 /24
  - 10.0.0.0/16 is outside the configured networks of eth1
  - eth1: none of the requested networks is configured on the interface
2
```

```bash
$ lan-tracker scan run --profile modbus
...                                   (the plan, as above)
Scanning (estimated typical duration ~65 s) ...
eth1: done in 27 s; 3 hosts answered ARP
  arp      250 no_reply, 3 reply
  tcp/502  2 OPEN, 3 REFUSED
```

```bash
$ lan-tracker active disable --reason "PLC fault on line 3, investigating"
Active discovery disabled (persists across restarts). Clear with: lan-tracker active enable
```

```bash
$ lan-tracker hosts set 00:1b:1b:aa:bb:02 --description "Line 3 PLC"
Description of 00:1b:1b:aa:bb:02 on eth1: Line 3 PLC
$ lan-tracker hosts unset 00:1b:1b:aa:bb:02 --description
Description of 00:1b:1b:aa:bb:02 on eth1 removed.
```

```bash
$ lan-tracker watch --interface eth1        # while the description above was set
14:00:00  HOST_DESCRIBED         eth1   Line 3 PLC  00:1b:1b:aa:bb:02
```

The time is the replay's simulated clock; a live daemon stamps the wall clock.

```bash
$ lan-tracker interfaces list
NAME  STATE    MAC  PREFIXES          PASSIVE  ACTIVE  HOSTS
eth1  unknown  -    192.168.110.0/24  replay   off     7
```

```bash
$ lan-tracker daemon status
Version:    dev (linux/arm64)   PID 208   up 0s   replay
Database:   /tmp/B/hosts.db  ok  507 KB
Active:     enabled
Clock:      unknown
Last scan:  never
Interfaces:
  eth1   unknown 192.168.110.0/24
         hosts      1 active, 6 stale
         replay     running     file
State:      OK
```

On a live interface the collector rows are `capture`, `neighbor`, `interface` and each enabled probe, as `<collector> <state> <backend or error>`, a probe with its last pass (`arp running afpacket last pass 4m ago: 253 swept, 14 replied (65 s)`); a configured collector that is not running makes the state `DEGRADED`, with each problem listed below it, and exit 1. `--quiet` prints nothing and leaves the answer to the exit code.

```bash
$ lan-tracker config reload               # after changing logging.level and storage.path
Configuration /tmp/A/c.yaml reloaded.

Applied:
  KEY            OLD   NEW
  logging.level  info  debug

Not applied, needs a restart (sudo systemctl restart lan-tracker):
  KEY           RUNNING          FILE
  storage.path  /tmp/A/hosts.db  /tmp/A/other.db
```

```bash
lan-tracker scan run --interface eth1 --profile modbus
lan-tracker observations list --host 3db0ce66-38c2-4f50-a1ce-a593cb872f88 --since 1h
lan-tracker hosts evidence 192.168.110.200 --interface eth1
lan-tracker hosts history 192.168.1.200 --offline --since 24h
lan-tracker hosts find 192.168.1.200 --interface eth1 --at "2026-10-04 10:15"
lan-tracker services list --interface eth1 --ip 192.168.110.50 --at "2026-10-01 13:00"
lan-tracker dhcp leases --interface eth1 --mac 00:1b:1b:aa:bb:01
lan-tracker events list --interface eth1 --since 24h --output jsonl
```
