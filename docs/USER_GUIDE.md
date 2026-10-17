# LAN Tracker user guide

This guide shows how to answer everyday questions with `lan-tracker`: who is on the network, who held an address at a given time, what DHCP told a device, whether its ports answered, what happened during an incident, and how to read all of it from the journal. Install it first ([README](../README.md#install-on-linux)); the full command reference is [`CLI.md`](CLI.md).

Every output below is real. The examples come from two sources:

- **A simulated day at a plant** (16 Oct 2026, interface `eth1`, network `192.168.110.0/24`), replayed through the daemon. Its devices: a gateway (`.1`), a SCADA server that is also the DHCP server (`.2`), a switch announcing itself over LLDP, a PLC (`.50`), an inverter with Modbus on tcp/502 (`.60`), and an HMI that gets `.120` by DHCP. During the day a service laptop arrives, a DHCP server running on it answers too, the laptop is given the inverter's address by hand, the inverter's tcp/502 stops answering for an hour, and at its lease renewal the HMI is handed a second DNS server. A replay shows `replay` in `daemon status` and marks event times with `?` (the clock's state is unknown for recorded data).
- **A small test network** in Docker (`172.30.99.0/24`) for active discovery, with a device listening on tcp/502 and two that refuse it.

Ages such as `3h ago` were relative to when the commands ran; host IDs, ages and times will differ on your system.

## 1. Before you start

- Run `lan-tracker` commands with `sudo`: the API socket (`/run/lan-tracker/api.sock`) and the database belong to root.
- Commands talk to the running daemon. When it is down, add `--offline` to read the database directly (section 12).
- A host is **one MAC on one interface**. The same device seen on two interfaces is two hosts; a device that changes its network card is a new host. Addresses, names, DHCP leases and TCP results are attributes of a host, each with a first-seen and last-seen time.
- Times you type (`--at`, `--since`, `--until`) are local time: `2026-10-16 10:15`, `2026-10-16`, or RFC 3339 (`2026-10-16T08:15:00Z`); `--since` also takes a duration back from now (`90m`, `24h`, `7d`).
- `OUI REGISTRANT` is who registered the MAC prefix with the IEEE: a hint about the network card, never the maker of the equipment. LAN Tracker does not identify devices; your description (section 9) is the only statement about what a device is.

## 2. Is it running?

```console
$ sudo lan-tracker daemon status
Version:    dev (linux/arm64)   PID 82   up 1s   replay
Database:   /tmp/guide/hosts.db  ok  2 MB
Active:     enabled
Clock:      unknown
Last scan:  never
Interfaces:
  eth1   unknown 192.168.110.0/24
         hosts      5 active, 1 recent, 2 stale
         replay     running     file
State:      OK
```

On a live system each interface lists its collectors (`capture`, `neighbor`, `interface`, and the probes `arp`, `icmp`, `tcp`, `names`) with their state; this is from the active-discovery test network after the kill switch was set:

```console
$ sudo lan-tracker daemon status
Version:    dev (linux/arm64)   PID 8   up 16s
Database:   /tmp/lt/hosts.db  ok  651 KB
Active:     DISABLED (PLC firmware update)
Clock:      unsynced (events are marked until the clock is synchronised)
Last scan:  2026-10-17 09:29:13  eth0  arp,icmp,tcp/502 (operator)
Interfaces:
  eth0   up      172.30.99.0/24
         hosts      3 active
         arp        disabled    active discovery is disabled by the kill switch
         capture    running     afpacket
         icmp       disabled    ping socket
         interface  running     netlink
         names      disabled    socket
         neighbor   running     netlink
         tcp        disabled    socket
State:      OK
```

`State` is `OK` (exit 0), `DEGRADED` (exit 1, e.g. a configured collector not running) or `UNHEALTHY` (exit 2). `Clock: unsynced` means the system clock is not yet synchronised (chrony, systemd-timesyncd): events written meanwhile are marked `*` in tables and `clock_sync: unsynced` in JSON, so you know their times may be off.

```console
$ sudo lan-tracker interfaces list
NAME  STATE    MAC  PREFIXES          PASSIVE  ACTIVE  HOSTS
eth1  unknown  -    192.168.110.0/24  replay   off     8
```

## 3. Who is on the network?

```console
$ sudo lan-tracker hosts list
MAC                IP                   HOSTNAME     OUI REGISTRANT            IFACE  STATE   LAST SEEN  DESCRIPTION
00:00:5e:00:01:01  192.168.110.1        -            ICANN, IANA Department    eth1   ACTIVE  3h ago     
00:0e:8c:11:22:33  192.168.110.120      HMI-LINE3    Siemens AG                eth1   ACTIVE  3h ago     
00:15:5d:10:00:02  192.168.110.2        -            Microsoft Corporation     eth1   ACTIVE  3h ago     
00:1b:1b:aa:bb:01  192.168.110.50       plc-a.local  Siemens AG                eth1   ACTIVE  3h ago     
00:1e:c9:00:00:17  -                    plant-sw-01  Dell Inc.                 eth1   RECENT  3h ago     
00:30:de:0a:00:07  192.168.110.60       -            WAGO Kontakttechnik GmbH  eth1   ACTIVE  3h ago     
00:50:56:c0:00:08  -                    -            VMware, Inc.              eth1   STALE   23h ago    
3c:52:82:aa:00:10  192.168.110.60 (+1)  SVC-LAPTOP   Hewlett Packard           eth1   STALE   22h ago    
```

`STATE` is how recently the host was seen: `ACTIVE` (within 5 minutes of the latest evidence), `RECENT`, `STALE`, `MISSING` (not seen for 24 hours). Only evidence that the device itself was there counts: a frame it sent, an address confirmed at its MAC (an ARP answer, a reachable neighbour entry), or one of your probes it answered. A DHCP server's statement, a DNS answer or a timeout never make a device present, and silence never makes it "offline": it is only not seen. `(+1)` means the host holds more addresses; the laptop holds `.121` and the inverter's `.60`.

Narrow the list with `--interface`, `--active` (ACTIVE or RECENT), `--stale`, `--seen-within 2h`, `--vendor wago` (the OUI registrant), `--description "line 3"` and `--port 502` (a TCP endpoint of the host on that port last answered OPEN).

One device in full, by MAC, IP, name or host ID:

```console
$ sudo lan-tracker hosts show 00:1b:1b:aa:bb:01
Host ID:     01a1492f-ea3f-7e0b-afa7-7ef896f65346
Interface:   eth1
MAC:         00:1b:1b:aa:bb:01  (OUI registrant: Siemens AG)
State:       ACTIVE
Name:        plc-a.local
First seen:  2026-10-16 06:00:11
Last seen:   2026-10-17 05:55:11  (frame 3h ago, address confirmed 3h ago, endpoint answer 3h ago)

Addresses:
  192.168.110.50   2026-10-16 06:00:11 → open   sources: passive_arp, passive_mdns, tcp_connect

Names:
  plc-a.local              mdns    A     192.168.110.50   expired 3h ago

TCP endpoints:
  192.168.110.50   tcp/502   OPEN        3h ago
```

`Last seen` lists the three kinds of presence evidence apart. A name is `fresh`, `expired` (its TTL ran out since it was last advertised) or `stale`; expired names stay on record, because time alone never ends a name.

## 4. Who has, and who had, this address?

Now:

```console
$ sudo lan-tracker hosts find 192.168.110.60
Host ID:     01a14930-59ec-7952-93ba-f266824495cf
Interface:   eth1
MAC:         00:30:de:0a:00:07  (OUI registrant: WAGO Kontakttechnik GmbH)
State:       ACTIVE
First seen:  2026-10-16 06:00:13
Last seen:   2026-10-17 05:55:13  (frame 3h ago, endpoint answer 23h ago)

Addresses:
  192.168.110.60   2026-10-16 06:00:13 → open   sources: passive_ipv4, tcp_connect   CONFLICT

Host ID:     01a14930-59f4-7566-9ec4-4683960c7c68
Interface:   eth1
MAC:         3c:52:82:aa:00:10  (OUI registrant: Hewlett Packard)
State:       STALE
Name:        SVC-LAPTOP
First seen:  2026-10-16 09:30:00
Last seen:   2026-10-16 10:31:00  (frame 22h ago, address confirmed 23h ago)

Addresses:
  192.168.110.121  2026-10-16 09:31:00 → open   sources: passive_ipv4
  192.168.110.60   2026-10-16 10:20:00 → open   sources: passive_arp   CONFLICT
...
```

Two MACs hold `.60`, so both bindings are marked `CONFLICT`. The laptop left at 10:31, but its claim stays open: a binding ends only on evidence (another MAC taking the address over while the holder is no longer live, or the holder seen again without the address for 24 hours), never because a device went quiet.

At a point in time, with `--at`:

```console
$ sudo lan-tracker hosts find 192.168.110.60 --at "2026-10-16 10:00"
Host ID:     01a14930-59ec-7952-93ba-f266824495cf
Interface:   eth1
MAC:         00:30:de:0a:00:07  (OUI registrant: WAGO Kontakttechnik GmbH)
Binding:     192.168.110.60   2026-10-16 06:00:13 → open   sources: passive_ipv4, tcp_connect

$ sudo lan-tracker hosts find 192.168.110.60 --at "2026-10-16 10:30"
Host ID:     01a14930-59ec-7952-93ba-f266824495cf
Interface:   eth1
MAC:         00:30:de:0a:00:07  (OUI registrant: WAGO Kontakttechnik GmbH)
Binding:     192.168.110.60   2026-10-16 06:00:13 → open   sources: passive_ipv4, tcp_connect   CONFLICT
Conflict:    2026-10-16 10:20:00 DUPLICATE_IP_DETECTED 192.168.110.60 between 3c:52:82:aa:00:10 and 00:30:de:0a:00:07

Host ID:     01a14930-59f4-7566-9ec4-4683960c7c68
Interface:   eth1
MAC:         3c:52:82:aa:00:10  (OUI registrant: Hewlett Packard)
Name:        SVC-LAPTOP
Binding:     192.168.110.60   2026-10-16 10:20:00 → open   sources: passive_arp   CONFLICT   (unconfirmed since 2026-10-16 10:25:00)
Conflict:    2026-10-16 10:20:00 DUPLICATE_IP_DETECTED 192.168.110.60 between 3c:52:82:aa:00:10 and 00:30:de:0a:00:07
```

`(unconfirmed since …)` says when the binding last had evidence before the time asked about. When nobody held the address, `find --at` prints the previous and the next holder and exits 1.

The whole story of an address, or of a device, is `hosts history`:

```console
$ sudo lan-tracker hosts history 192.168.110.60
TIME                  TYPE                   IFACE  MAC                VALUE                            RELATED            CAUSE
2026-10-16 06:00:13?  HOST_DISCOVERED        eth1   00:30:de:0a:00:07  192.168.110.60                   -                  passive_ipv4
2026-10-16 06:00:29?  SERVICE_STATE_CHANGED  eth1   00:30:de:0a:00:07  tcp/502 OPEN                     -                  tcp_connect
2026-10-16 10:20:00?  IP_ADDED               eth1   3c:52:82:aa:00:10  192.168.110.60                   -                  passive_arp
2026-10-16 10:20:00?  DUPLICATE_IP_DETECTED  eth1   3c:52:82:aa:00:10  192.168.110.60                   00:30:de:0a:00:07  passive_arp
2026-10-16 14:15:29?  SERVICE_STATE_CHANGED  eth1   -                  tcp/502 OPEN -> tcp/502 TIMEOUT  -                  tcp_connect
2026-10-16 15:15:29?  SERVICE_STATE_CHANGED  eth1   -                  tcp/502 REFUSED -> tcp/502 OPEN  -                  tcp_connect
? written while the clock's synchronisation was unknown

$ sudo lan-tracker hosts history --mac 3c:52:82:aa:00:10
TIME                  TYPE                   IFACE  MAC                VALUE            RELATED            CAUSE
2026-10-16 09:30:00?  HOST_DISCOVERED        eth1   3c:52:82:aa:00:10  -                -                  passive_dhcp
2026-10-16 09:30:00?  HOSTNAME_ADDED         eth1   3c:52:82:aa:00:10  dhcp:SVC-LAPTOP  -                  passive_dhcp
2026-10-16 09:30:01?  DHCP_LEASE_ASSIGNED    eth1   3c:52:82:aa:00:10  192.168.110.121  -                  passive_dhcp_server
2026-10-16 09:31:00?  IP_ADDED               eth1   3c:52:82:aa:00:10  192.168.110.121  -                  passive_ipv4
2026-10-16 10:20:00?  IP_ADDED               eth1   3c:52:82:aa:00:10  192.168.110.60   -                  passive_arp
2026-10-16 10:20:00?  DUPLICATE_IP_DETECTED  eth1   3c:52:82:aa:00:10  192.168.110.60   00:30:de:0a:00:07  passive_arp
? written while the clock's synchronisation was unknown
```

`CAUSE` is the evidence behind each event: `passive_arp` (an ARP frame seen), `passive_ipv4` (an IPv4 packet from that MAC), `passive_dhcp` (a client's own DHCP message), `passive_dhcp_server` (a server's reply), `kernel_neighbor`, `tcp_connect`, `arp_scan` and so on.

## 5. Reconstructing an incident

*"The inverter dropped off the SCADA around 14:15 yesterday. What happened?"*

Start from what changed in the window:

```console
$ sudo lan-tracker events list --since 48h
TIME                  TYPE                        IFACE  MAC                VALUE                                                 RELATED            CAUSE
...
2026-10-16 09:30:00?  HOST_DISCOVERED             eth1   3c:52:82:aa:00:10  -                                                     -                  passive_dhcp
2026-10-16 09:30:00?  HOSTNAME_ADDED              eth1   3c:52:82:aa:00:10  dhcp:SVC-LAPTOP                                       -                  passive_dhcp
2026-10-16 09:30:00?  HOST_DISCOVERED             eth1   00:50:56:c0:00:08  10.0.0.1                                              -                  passive_dhcp_server
2026-10-16 09:30:00?  DHCP_SERVER_DISCOVERED      eth1   00:50:56:c0:00:08  10.0.0.1                                              -                  passive_dhcp_server
2026-10-16 09:30:00?  DHCP_SERVER_UNEXPECTED      eth1   00:50:56:c0:00:08  10.0.0.1                                              -                  passive_dhcp_server
2026-10-16 09:30:01?  DHCP_LEASE_ASSIGNED         eth1   3c:52:82:aa:00:10  192.168.110.121                                       -                  passive_dhcp_server
2026-10-16 09:31:00?  IP_ADDED                    eth1   3c:52:82:aa:00:10  192.168.110.121                                       -                  passive_ipv4
2026-10-16 10:20:00?  IP_ADDED                    eth1   3c:52:82:aa:00:10  192.168.110.60                                        -                  passive_arp
2026-10-16 10:20:00?  DUPLICATE_IP_DETECTED       eth1   3c:52:82:aa:00:10  192.168.110.60                                        00:30:de:0a:00:07  passive_arp
2026-10-16 14:15:29?  SERVICE_STATE_CHANGED       eth1   -                  tcp/502 OPEN -> tcp/502 TIMEOUT                       -                  tcp_connect
2026-10-16 15:15:29?  SERVICE_STATE_CHANGED       eth1   -                  tcp/502 REFUSED -> tcp/502 OPEN                       -                  tcp_connect
2026-10-16 16:00:01?  DHCP_CLIENT_CONFIG_CHANGED  eth1   00:0e:8c:11:22:33  dns=192.168.110.2 -> dns=192.168.110.2,192.168.110.4  -                  passive_dhcp_server
? written while the clock's synchronisation was unknown
```

Only the warnings, by type (names as in `CLI.md`, "Event type names"; repeat `--type` or separate with commas):

```console
$ sudo lan-tracker events list --type duplicate-ip,dhcp-server-unexpected
TIME                  TYPE                    IFACE  MAC                VALUE           RELATED            CAUSE
2026-10-16 09:30:00?  DHCP_SERVER_UNEXPECTED  eth1   00:50:56:c0:00:08  10.0.0.1        -                  passive_dhcp_server
2026-10-16 10:20:00?  DUPLICATE_IP_DETECTED   eth1   3c:52:82:aa:00:10  192.168.110.60  00:30:de:0a:00:07  passive_arp
```

So: at 09:30 a service laptop joined, and a DHCP server outside the allowlist (`10.0.0.1`, a virtual machine on the laptop, by its OUI) answered its request. At 10:20 the laptop claimed the inverter's address `.60` with a gratuitous ARP. From then on two MACs hold `.60`. The tcp/502 checks against `.60` stopped answering at 14:15 (TIMEOUT), were refused from 14:45 and answered again at 15:15; the `MAC` column is empty because, with two holders, a result for `.60` cannot be attributed to either device.

The evidence behind a host, with the raw observation counts and every event's evidence snapshot:

```console
$ sudo lan-tracker hosts evidence 3c:52:82:aa:00:10 --since 7d
Host 01a14930-59f4-7566-9ec4-4683960c7c68  3c:52:82:aa:00:10  on eth1

Addresses:
  192.168.110.121  2026-10-16 09:31:00 → open
      passive_ipv4     first 2026-10-16 09:31:00  last 2026-10-16 10:31:00
  192.168.110.60   2026-10-16 10:20:00 → open   CONFLICT
      passive_arp      first 2026-10-16 10:20:00  last 2026-10-16 10:25:00

Names:
  SVC-LAPTOP               dhcp    NAME  -                fresh 23h ago  passive_dhcp link sender

DHCP leases (the servers' statements, not proof the host holds the address):
  192.168.110.121  from 192.168.110.2   2026-10-16 09:30:01 → open  acks 1  lease 12h0m0s  expires 2026-10-16 21:30:01  gateway 192.168.110.1

Observations (raw and roll-ups):
  passive_arp      192.168.110.60        2  first 2026-10-16 10:20:00  last 2026-10-16 10:25:00
  passive_dhcp     -                     2  first 2026-10-16 09:30:00  last 2026-10-16 09:30:01
  passive_ipv4     192.168.110.121      13  first 2026-10-16 09:31:00  last 2026-10-16 10:31:00

Events:
  2026-10-16 09:30:00  HOST_DISCOVERED        -  (passive_dhcp)
      evidence {"hostname":"SVC-LAPTOP","interface":"eth1","mac":"3c:52:82:aa:00:10","name_type":"dhcp","source":"passive_dhcp","ts":"2026-10-16T09:30:00Z"}
  ...
  2026-10-16 10:20:00  DUPLICATE_IP_DETECTED  192.168.110.60  (passive_arp)
      evidence {"interface":"eth1","ip":"192.168.110.60","mac":"3c:52:82:aa:00:10","source":"passive_arp","ts":"2026-10-16T10:20:00Z"}
```

Raw observations are kept 7 days and hourly roll-ups 90 days; events keep their evidence for 2 years, so an incident can be explained long after the raw data is gone.

## 6. DHCP: servers, exchanges and leases

LAN Tracker only listens to DHCP; it never sends a DHCP message. On a switched port it sees broadcasts (DISCOVER, OFFER and REQUEST are usually broadcast) but often not unicast replies or renewals, so DHCP history there is often partial; on a mirror port it sees everything.

The servers that answered, with the allowlist verdict (`interfaces[].dhcp.servers` in the configuration; without a list every server is `unchecked`, never "trusted"):

```console
$ sudo lan-tracker dhcp servers
SERVER ID      RELAY  MAC                IP             IFACE  STATUS      ROUTER         DNS                          MASK           DOMAIN         LAST SEEN
10.0.0.1       -      00:50:56:c0:00:08  10.0.0.1       eth1   unexpected  10.0.0.1       -                            255.255.255.0  -              23h ago
192.168.110.2  -      00:15:5d:10:00:02  192.168.110.2  eth1   allowed     192.168.110.1  192.168.110.2,192.168.110.4  255.255.255.0  plant.example  17h ago
```

`DOMAIN` is the domain name (option 15) the server last handed out; `-o json` also shows its domain search list (option 119) as `search`. A server that starts handing out another domain shows it here at its next offer or ACK, and each client that receives it gets a `DHCP_CLIENT_CONFIG_CHANGED` event in its lease history.

Each transaction a client went through, with every message, the competing offers and the server the client chose:

```console
$ sudo lan-tracker dhcp exchanges --mac 3c:52:82:aa:00:10
CLIENT             XID         IFACE  FIRST SEEN           MESSAGES                                                                                                                           OFFERS  SELECTED       OUTCOME  VISIBILITY
3c:52:82:aa:00:10  0x51c0ffee  eth1   2026-10-16 09:30:00  discover offer(192.168.110.2 192.168.110.121) offer(10.0.0.1 10.0.0.50) request(192.168.110.2) ack(192.168.110.2 192.168.110.121)  2       192.168.110.2  ack      complete
```

The laptop received two offers, chose the site's server and got `.121`; the rogue server's offer of `10.0.0.50` was not taken. `OUTCOME` is the last decisive message (`ack`, `inform_ack`, `nak`, `offered`, `decline`, `release`, or `incomplete` when the exchange ended unanswered as far as the tracker saw); `VISIBILITY` is `partial` when only one side was seen.

The lease history: one row per address and configuration a server acknowledged for a client. A renewal with the same address and configuration only counts `ACKS`; a change starts a new row:

```console
$ sudo lan-tracker dhcp leases
CLIENT             IP               SERVER ID      RELAY  IFACE  FIRST ACK            LAST ACK             ACKS  LEASE    EXPIRES              GATEWAY        DNS                          STATE
00:0e:8c:11:22:33  192.168.110.120  192.168.110.2  -      eth1   2026-10-16 16:00:01  2026-10-16 16:00:01  1     12h0m0s  2026-10-17 04:00:01  192.168.110.1  192.168.110.2,192.168.110.4  open
3c:52:82:aa:00:10  192.168.110.121  192.168.110.2  -      eth1   2026-10-16 09:30:01  2026-10-16 09:30:01  1     12h0m0s  2026-10-16 21:30:01  192.168.110.1  192.168.110.2                open
00:0e:8c:11:22:33  192.168.110.120  192.168.110.2  -      eth1   2026-10-16 07:05:02  2026-10-16 07:05:02  1     12h0m0s  2026-10-16 19:05:02  192.168.110.1  192.168.110.2                superseded
```

At its 16:00 renewal the HMI kept `.120` but was given a second DNS server: the old row is `superseded` and the event says what changed:

```console
$ sudo lan-tracker -o json events list --type dhcp-client-config-changed
[
  {
    "id": 25,
    "ts": "2026-10-16T16:00:01Z",
    "type": "DHCP_CLIENT_CONFIG_CHANGED",
    "severity": "notice",
    "interface": "eth1",
    "host_id": "01a1492f-ea40-72f3-ad61-c10ac7ca84c9",
    "mac": "00:0e:8c:11:22:33",
    "old": "dns=192.168.110.2",
    "new": "dns=192.168.110.2,192.168.110.4",
    "cause": "passive_dhcp_server",
    "observation_id": 662,
    "evidence": {
      "ts": "2026-10-16T16:00:01Z",
      "source": "passive_dhcp_server",
      "interface": "eth1",
      "mac": "00:15:5d:10:00:02",
      "ip": "192.168.110.2"
    },
    "clock_sync": "unknown"
  }
]
```

Read leases for what they are: a lease is the server's statement about a client. It never creates a host, never makes a device present and is not proof the device holds the address. `EXPIRES` is the expiry according to the last ACK seen; the laptop's lease shows `open` with an expiry in the past because no RELEASE was seen, which says nothing about whether the laptop gave the address up. `-o json` shows the full snapshot of each lease, including the gateway (from option 121 when the server sent classless routes, else the first router), the domain, the subnet and the T1/T2 renewal times.

## 7. TCP endpoints

When TCP checks are enabled (section 11), each address and port has a result: `OPEN` (the connect completed: nothing more; tcp/502 `OPEN` is not proof of Modbus), `REFUSED`, `TIMEOUT`, `UNREACHABLE`, or `LOCAL_ERROR` (a problem on this box, such as no direct route, so nothing was sent). The last definite answer (`OPEN` or `REFUSED`) is kept apart, so a timeout never erases that the port last answered:

```console
$ sudo lan-tracker services list
IP              PORT     LAST RESULT  CHECKED  LAST DEFINITE  MAC                IFACE
192.168.110.50  502/tcp  OPEN         3h ago   OPEN 3h ago    00:1b:1b:aa:bb:01  eth1
192.168.110.60  502/tcp  OPEN         3h ago   OPEN 3h ago    -                  eth1

$ sudo lan-tracker services list --ip 192.168.110.60 --at "2026-10-16 14:30"
IP              PORT     LAST RESULT  CHECKED  LAST DEFINITE  MAC  IFACE
192.168.110.60  502/tcp  TIMEOUT      19h ago  OPEN 22h ago   -    eth1
```

`MAC` is the device that alone held the address at the result, and whose binding was confirmed within the last 24 hours. For `.60` it is empty: two MACs hold that address, so the result belongs to the address, not to either device. `--at` shows the results in effect at that time; an endpoint with no result recorded by then is left out. Only changes into or out of `OPEN` are events (`SERVICE_STATE_CHANGED`), so the change from TIMEOUT to REFUSED at 14:45 is in the history (`--at "2026-10-16 15:00"` shows it) but not in the journal.

## 8. Names

Names are kept as the records they came from: mDNS announcements, DNS answers the box saw, DHCP hostnames and LLDP system names, each with its type, source, advertiser and TTL. A name is linked to a device only by evidence: a name a device gave itself (DHCP hostname, LLDP) belongs to its sender; an address record belongs to the device that alone held the address. An mDNS proxy advertising other devices therefore binds nothing to itself, and a name for an address nobody holds stays unlinked.

```console
$ sudo lan-tracker hosts find plc-a.local
$ sudo lan-tracker hosts list --vendor siemens
```

`hosts show` lists a host's names (section 3); `-o json` adds the record type, TTL, expiry and link. Names end only on evidence (an mDNS goodbye, a cache-flush record replacing them, or the address passing to another device); time alone never removes one.

## 9. Describing devices

Your description is the only statement about what a device is. It belongs to the host (the MAC on that interface) and survives address and name changes:

```console
$ sudo lan-tracker hosts set 00:0e:8c:11:22:33 --description "HMI line 3, cabinet B"
Description of 00:0e:8c:11:22:33 on eth1: HMI line 3, cabinet B

$ sudo lan-tracker hosts list --description "line 3"
MAC                IP               HOSTNAME   OUI REGISTRANT  IFACE  STATE   LAST SEEN  DESCRIPTION
00:0e:8c:11:22:33  192.168.110.120  HMI-LINE3  Siemens AG      eth1   ACTIVE  3h ago     HMI line 3, cabinet B
```

`sudo lan-tracker hosts unset <host> --description` removes it. Each change is a `HOST_DESCRIBED` event with the old and new text and the Unix user who made it. When a query matches several current hosts (the same MAC on two interfaces), add `--interface` or use the host ID.

## 10. Watching and the journal

`watch` prints events as they happen (`--interface`, `--type`, `--port` filter them; `-o jsonl` prints one JSON object per line):

```console
$ sudo lan-tracker watch --interface eth1
```

Every event is also one entry in the systemd journal, with the event's fields as journal fields. Observations are never logged, so the journal stays small. This is the journal of the scenario replayed under the installed unit:

```console
$ journalctl -u lan-tracker -o short-iso
2026-10-17T09:28:34+0000 563bb68480d1 systemd[1]: Starting lan-tracker.service - LAN Tracker...
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: lan-tracker starting
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: database ready
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: vendor table loaded
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: api listening
2026-10-17T09:28:34+0000 563bb68480d1 systemd[1]: Started lan-tracker.service - LAN Tracker.
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: active discovery: off (replay)
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: lan-tracker running
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: SUBNET_CHANGED eth1 192.168.110.0/24
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: HOST_DISCOVERED eth1 00:00:5e:00:01:01 192.168.110.1
...
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: DHCP_SERVER_UNEXPECTED eth1 00:50:56:c0:00:08 10.0.0.1
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: DHCP_LEASE_ASSIGNED eth1 3c:52:82:aa:00:10 192.168.110.121 192.168.110.2
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: IP_ADDED eth1 3c:52:82:aa:00:10 192.168.110.121
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: IP_ADDED eth1 3c:52:82:aa:00:10 192.168.110.60
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: DUPLICATE_IP_DETECTED eth1 3c:52:82:aa:00:10 192.168.110.60
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: SERVICE_STATE_CHANGED eth1 tcp/502 OPEN -> tcp/502 TIMEOUT 192.168.110.60
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: SERVICE_STATE_CHANGED eth1 tcp/502 REFUSED -> tcp/502 OPEN 192.168.110.60
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: DHCP_CLIENT_CONFIG_CHANGED eth1 00:0e:8c:11:22:33 dns=192.168.110.2 -> dns=192.168.110.2,192.168.110.4 192.168.110.2
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: replay finished
```

In a replay every entry is written when the replay ran; the `TS` field holds the time the event happened. On a live system the two are the same.

Warnings only (duplicate IPs, unexpected DHCP servers, a DHCP server answering from another MAC, proxy ARP, NAK and DECLINE, interfaces going down, collection gaps, the kill switch):

```console
$ journalctl -u lan-tracker -p warning
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: DHCP_SERVER_UNEXPECTED eth1 00:50:56:c0:00:08 10.0.0.1
2026-10-17T09:28:34+0000 563bb68480d1 lan-tracker[151]: DUPLICATE_IP_DETECTED eth1 3c:52:82:aa:00:10 192.168.110.60
```

One device, by its MAC:

```console
$ journalctl -u lan-tracker MAC=3c:52:82:aa:00:10 -o cat
HOST_DISCOVERED eth1 3c:52:82:aa:00:10
HOSTNAME_ADDED eth1 3c:52:82:aa:00:10 dhcp:SVC-LAPTOP
DHCP_LEASE_ASSIGNED eth1 3c:52:82:aa:00:10 192.168.110.121 192.168.110.2
IP_ADDED eth1 3c:52:82:aa:00:10 192.168.110.121
IP_ADDED eth1 3c:52:82:aa:00:10 192.168.110.60
DUPLICATE_IP_DETECTED eth1 3c:52:82:aa:00:10 192.168.110.60
```

All the fields of one event type (`EVENT` is the type in lower case):

```console
$ journalctl -u lan-tracker EVENT=duplicate_ip_detected -o verbose
Sat 2026-10-17 09:28:34.413233 UTC [s=38b5a343d1f4455fa0bf8ac08dd577d8;i=3df;b=8caeec6aed02496f894a97ce6ad7cca1;...]
    _UID=0
    ...
    _SYSTEMD_UNIT=lan-tracker.service
    IFACE=eth1
    CLOCK_SYNC=unknown
    SOURCE=passive_arp
    IP=192.168.110.60
    MAC=3c:52:82:aa:00:10
    HOST_ID=01a14931-0f6c-78ee-94e4-a59f66943c23
    PRIORITY=4
    NEW_VALUE=192.168.110.60
    TS=2026-10-16 10:20:00 +0000 UTC
    MESSAGE=DUPLICATE_IP_DETECTED eth1 3c:52:82:aa:00:10 192.168.110.60
    EVENT=duplicate_ip_detected
    RELATED_HOST_ID=01a14931-0f0d-7f03-9c7d-bad6af73375d
```

The fields are `EVENT`, `IFACE`, `MAC`, `IP`, `HOST_ID`, `RELATED_HOST_ID`, `OLD_VALUE`, `NEW_VALUE`, `SOURCE` (the evidence), `TS` (when it happened) and `CLOCK_SYNC`; `_CAP_EFFECTIVE=2000` shows the daemon held `CAP_NET_RAW` only. Useful combinations:

```bash
journalctl -u lan-tracker -f                                  # follow
journalctl -u lan-tracker EVENT=service_state_changed IP=192.168.110.60   # one endpoint's changes
journalctl -u lan-tracker IFACE=eth1 --since "2026-10-16 09:00" --until "2026-10-16 11:00"
journalctl -u lan-tracker EVENT=dhcp_server_unexpected -o json-pretty     # for a script
journalctl -u lan-tracker EVENT=observation_gap               # when the tracker could not see
```

`OBSERVATION_GAP` is written when a capture or neighbour collector stopped and recovered, and at start-up for the time the daemon was not running (one minute or more): history inside a gap is unknown, not quiet.

## 11. Active discovery

Active discovery is off until an interface enables it for explicit networks. Everything it sends takes a slot from a budget (by default 5 probe starts per second in all, ARP 5/s, ICMP 1/s, TCP 1 connect/s with at most 2 at once per interface and 1 per host, at most 4 probes in flight, 5 seconds between probes to one device), excluded addresses are never probed, and TCP checks are a bare connect with no payload. Review the devices on a network before enabling it; some OT equipment reacts badly to unexpected traffic. `deploy/config.active-example.yaml` is a reviewed ARP + TCP profile.

The test network's configuration:

```yaml
version: 1
interfaces:
  - name: eth0
    passive: { enabled: true }
    active:
      enabled: true
      networks: [172.30.99.0/28]
      exclude: [172.30.99.1]          # the gateway
active:
  startup_delay: 24h                  # operator scans only in this session
  tcp:
    targets:
      - { port: 502, name: modbus, timeout: 750ms }
profiles:
  survey: { arp: true, icmp: true, tcp: [502] }
```

`config validate` says what each interface would do and the worst-case start rate:

```console
$ sudo lan-tracker config validate
Configuration /c/config.yaml is valid.

Interfaces:
  NAME  PASSIVE  ACTIVE                                                                              DHCP SERVERS
  eth0  passive  active on  networks 172.30.99.0/28  exclude 172.30.99.1  sweep 13 addresses (~3 s)  no allowlist

Probes:   arp every 15m
Budgets:  global 5 starts/s; arp 5 starts/s; icmp 1 starts/s; tcp 1 connects/s (≤2 per interface, ≤1 per host); ≤4 jobs at once
Estimated maximum probe start rate: 5/s (kernel TCP packets come on top)
```

Preview a scan with `scan plan`, then run it with `scan run` (same arguments; the run refuses exactly what the plan refuses):

```console
$ sudo lan-tracker scan plan --profile survey
Interface:  eth0    Profile: survey    Networks: 172.30.99.0/28
Targets:    12 to sweep (excluded: 172.30.99.1), 0 known hosts
Probes:     arp; icmp; tcp/502
Rates:      arp 5 starts/s, icmp 1 starts/s, tcp 1 connects/s (≤2 per interface, ≤1 per host); global cap 5 probe starts/s
Estimate:   12 ARP requests: 12 probe starts (kernel TCP packets come on top)
Duration:   estimated typical ~4 s, estimated no-response ~4 s
Assumes:    probes start paced at the rates above, at most 5 starts/s in all, at least 5s apart per target and at most 4 in flight, each holding its slot while it waits for a reply; sending, connecting and answering take no time; the kernel's TCP handshake, retransmission and close packets are not counted
            typical: known hosts answer at once; most swept addresses are empty, so the ARP sweep waits its 1s reply timeout once, after its last request
            no-response: nothing answers, so every phase waits out its reply timeout once (ARP 1s, ICMP 1s, tcp/502 750ms)
            ICMP and TCP counts use the hosts known now; hosts that first answer the sweep are probed too, which adds to a run
Verdict:    ALLOWED

$ sudo lan-tracker scan run --profile survey
... (the plan again) ...
Scanning (estimated typical duration ~4 s) ...
eth0: done in 14 s; 3 hosts answered ARP
  arp      3 reply, 9 no_reply
  icmp     3 reply
  tcp/502  1 OPEN, 2 REFUSED
```

The run took longer than the plan because no host was known when it was planned: the three hosts that answered the sweep were then pinged and connected to, each at least 5 seconds after its ARP request (the plan's last `Assumes` line). The results are endpoints like any other:

```console
$ sudo lan-tracker services list
IP            PORT     LAST RESULT  CHECKED  LAST DEFINITE   MAC                IFACE
172.30.99.10  502/tcp  OPEN         0s ago   OPEN 0s ago     00:1b:1b:aa:bb:10  eth0
172.30.99.11  502/tcp  REFUSED      2s ago   REFUSED 2s ago  00:30:de:0a:00:11  eth0
172.30.99.12  502/tcp  REFUSED      1s ago   REFUSED 1s ago  00:0e:8c:11:22:12  eth0
```

What a plan refuses, and why:

```console
$ sudo lan-tracker scan plan --network 172.30.0.0/16
Interface:  eth0
Probes:     arp
Rates:      arp 5 starts/s; global cap 5 probe starts/s
Estimate:   nothing to send
Verdict:    REFUSED
  - 172.30.0.0/16 is wider than max_auto_scan_prefix_v4 /24
  - 172.30.0.0/16 is outside the configured networks of eth0
  - eth0: none of the requested networks is configured on the interface
```

The kill switch stops every probe at once, periodic and operator, and stays set across restarts until cleared:

```console
$ sudo lan-tracker active disable --reason "PLC firmware update"
Active discovery disabled (persists across restarts). Clear with: lan-tracker active enable

$ sudo lan-tracker scan run --arp
...
Verdict:    REFUSED
  - active discovery is disabled by the kill switch (PLC firmware update)

$ sudo lan-tracker active enable --reason "update finished"
Active discovery enabled.

$ sudo lan-tracker events list --type scan-started,scan-completed,active-disabled,active-enabled
TIME                  TYPE             IFACE  MAC  VALUE                                                                                        RELATED  CAUSE
2026-10-17 09:29:13*  SCAN_STARTED     eth0   -    operator scan arp,icmp,tcp/502 (profile survey): 12 addresses to sweep, 0 known hosts        -        operator
2026-10-17 09:29:27*  SCAN_COMPLETED   eth0   -    operator scan done in 14s; arp 3 reply, 9 no_reply; icmp 3 reply; tcp/502 1 open, 2 refused  -        operator
2026-10-17 09:29:27*  ACTIVE_DISABLED  -      -    active discovery disabled by root: PLC firmware update                                       -        operator
2026-10-17 09:29:27*  ACTIVE_ENABLED   -      -    active discovery enabled by root: update finished                                            -        operator
* written before the system clock was synchronised: the time may be off
```

`LAN_TRACKER_ACTIVE_DISABLED=1` in the unit's environment forces active discovery off regardless (`active enable` then refuses). Periodic probing starts with `active.arp`, `active.icmp`, `active.tcp` and `active.names` in the configuration and `sudo lan-tracker config reload`; `daemon status` shows each probe's last pass.

## 12. When the daemon is down: offline mode

`--offline` reads the database directly and read-only, with the same queries as the daemon; it refuses commands that need the daemon:

```console
$ sudo lan-tracker --offline hosts list --active
notice: /tmp/guide/hosts.db has no WAL (daemon not running); reading it as it is. If the daemon starts meanwhile, re-run.
MAC                IP               HOSTNAME     OUI REGISTRANT            IFACE  STATE   LAST SEEN  DESCRIPTION
00:00:5e:00:01:01  192.168.110.1    -            ICANN, IANA Department    eth1   ACTIVE  3h ago     
00:0e:8c:11:22:33  192.168.110.120  HMI-LINE3    Siemens AG                eth1   ACTIVE  3h ago     HMI line 3, cabinet B
...

$ sudo lan-tracker --offline daemon status
lan-tracker: lan-tracker daemon status needs the running daemon; it does not work with --offline
```

`--db` reads another database, for example a copy taken from a board: `lan-tracker --offline --db ./hosts.db events list --since 7d`.

## 13. Scripting

Every list supports `-o table` (default), `json`, `jsonl` and `csv`; records support `table` and `json`. CSV uses RFC 3339 UTC times:

```console
$ sudo lan-tracker -o csv hosts list
MAC,IP,HOSTNAME,OUI REGISTRANT,IFACE,STATE,LAST SEEN,DESCRIPTION
00:00:5e:00:01:01,192.168.110.1,,"ICANN, IANA Department",eth1,ACTIVE,2026-10-17T05:55:03Z,
00:0e:8c:11:22:33,192.168.110.120,HMI-LINE3,Siemens AG,eth1,ACTIVE,2026-10-17T05:55:23Z,"HMI line 3, cabinet B"
...
3c:52:82:aa:00:10,192.168.110.60 192.168.110.121,SVC-LAPTOP,Hewlett Packard,eth1,STALE,2026-10-16T10:31:00Z,
```

Exit codes: `0` success, `1` degraded or no result (`hosts find` with no holder, `hosts show` with no host), `2` unhealthy, a command error or a refused scan, `3` the daemon is unreachable, `64` a usage error. `--quiet` prints nothing and leaves the answer to the exit code:

```bash
if sudo lan-tracker --quiet hosts find 192.168.110.60 --at "2026-10-16 10:15"; then echo "someone held it"; fi
sudo lan-tracker --quiet daemon status || echo "lan-tracker needs attention"
```

The same data is on the local REST API ([`API.md`](API.md)) and, with `api.listen` and `metrics.enabled`, as Prometheus metrics on loopback (host counts per state, events, collectors up, DHCP servers per verdict, probe counts, database size; never a MAC or IP as a label).

## 14. The database

```console
$ sudo lan-tracker db info
Path:      /tmp/guide/hosts.db
Schema:    1
Journal:   wal
Size:      4 KB (data 578 KB)
WAL:       2 MB

TABLE                ROWS
address_sources      11
addresses            7
...
observations         1742
...

$ sudo lan-tracker db check
ok
```

Compaction runs hourly: raw observations older than 7 days are folded into hourly roll-ups, roll-ups are kept 90 days, events and the history they explain 2 years, all inside `storage.retention.max_db_size` (200 MB). A database that fails its integrity check at start-up is set aside as `<path>.corrupt-<time>`, a new one is created and the event `DATABASE_RECREATED` says so.

## 15. Quick answers

| Question | Command |
| --- | --- |
| Is the tracker healthy? | `sudo lan-tracker daemon status` |
| What is on this interface now? | `sudo lan-tracker hosts list --interface eth1 --active` |
| Who has this IP? | `sudo lan-tracker hosts find 192.168.110.60` |
| Who had it at 10:15 yesterday? | `sudo lan-tracker hosts find 192.168.110.60 --at "2026-10-16 10:15"` |
| Everything that happened to an IP | `sudo lan-tracker hosts history 192.168.110.60` |
| Everything about a device | `sudo lan-tracker hosts show <mac>`, `sudo lan-tracker hosts evidence <mac>` |
| Duplicate IPs | `sudo lan-tracker events list --type duplicate-ip` |
| Rogue DHCP servers | `sudo lan-tracker dhcp servers` (status `unexpected`) |
| What DHCP gave a device | `sudo lan-tracker dhcp leases --mac <mac>`, `sudo lan-tracker dhcp exchanges --mac <mac>` |
| Did port 502 answer at 14:30? | `sudo lan-tracker services list --ip <ip> --at "2026-10-16 14:30"` |
| Warnings in the journal | `journalctl -u lan-tracker -p warning` |
| Was the tracker blind? | `journalctl -u lan-tracker EVENT=observation_gap` |
| Stop all probing now | `sudo lan-tracker active disable --reason "..."` |
