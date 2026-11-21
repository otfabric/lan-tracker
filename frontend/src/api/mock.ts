// Development only (`npm run dev`, then /?mock): a made-up two-network site
// generated from a fixed seed, so every view can be worked on without a
// Linux daemon. Never part of what the daemon serves at runtime.

import type {
  Activity,
  Api,
  DHCPExchange,
  DHCPLease,
  DHCPServer,
  Endpoint,
  Host,
  HostSummary,
  InterfaceInfo,
  LTEvent,
  Presence,
  Scope,
  Status,
  TCPResult,
} from './types';

function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = s;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const H = 3600_000;
const DAY = 24 * H;

interface Model {
  vendor: string;
  oui: string;
  name: string;
  desc?: string;
  ports: number[];
  chatty: number; // observations per hour
  flaky?: boolean;
  dhcp?: boolean;
  la?: boolean;
}

const CELL: Model[] = [
  { vendor: 'Siemens AG', oui: '00:1b:1b', name: 'plc-line1', desc: 'S7 PLC, line 1', ports: [102, 502], chatty: 40 },
  { vendor: 'Siemens AG', oui: '00:1b:1b', name: 'plc-line2', desc: 'S7 PLC, line 2', ports: [102, 502], chatty: 38 },
  { vendor: 'Siemens AG', oui: '00:1b:1b', name: 'plc-press', ports: [102], chatty: 35, flaky: true },
  { vendor: 'Phoenix Contact GmbH & Co. KG', oui: 'a8:74:1d', name: 'axc-pump', desc: 'Pump station controller', ports: [502, 443], chatty: 22 },
  { vendor: 'Phoenix Contact GmbH & Co. KG', oui: 'a8:74:1d', name: 'axc-chiller', ports: [502], chatty: 18 },
  { vendor: 'SMA Solar Technology AG', oui: '00:40:ad', name: 'sma-inv-north', desc: 'Inverter, north roof', ports: [502, 80], chatty: 12 },
  { vendor: 'SMA Solar Technology AG', oui: '00:40:ad', name: 'sma-inv-south', desc: 'Inverter, south roof', ports: [502, 80], chatty: 12 },
  { vendor: 'Fronius International GmbH', oui: '00:03:ac', name: 'fronius-carport', ports: [502, 80], chatty: 10, flaky: true },
  { vendor: 'Hirschmann Automation and Control GmbH', oui: 'ec:e5:55', name: 'sw-cell-a', desc: 'Cell switch A', ports: [443], chatty: 30 },
  { vendor: 'Hirschmann Automation and Control GmbH', oui: 'ec:e5:55', name: 'sw-cell-b', ports: [443], chatty: 28 },
  { vendor: 'Beijer Electronics AB', oui: '00:05:51', name: 'hmi-line1', desc: 'Operator panel, line 1', ports: [80], chatty: 20, dhcp: true },
  { vendor: 'Beijer Electronics AB', oui: '00:05:51', name: 'hmi-line2', ports: [80], chatty: 19, dhcp: true },
  { vendor: 'WAGO GmbH & Co. KG', oui: '00:30:de', name: 'wago-meter', ports: [502, 80], chatty: 9 },
  { vendor: 'Moxa Inc.', oui: '00:90:e8', name: 'moxa-gw', desc: 'Serial gateway', ports: [502, 4001], chatty: 14 },
  { vendor: 'Axis Communications AB', oui: 'ac:cc:8e', name: 'cam-hall', ports: [443, 554], chatty: 55, dhcp: true },
  { vendor: 'Dell Inc.', oui: 'f8:bc:12', name: 'eng-laptop-07', ports: [], chatty: 70, dhcp: true },
  { vendor: 'Dell Inc.', oui: '54:bf:64', name: 'eng-ws-02', desc: 'Engineering workstation', ports: [3389], chatty: 65, dhcp: true },
  { vendor: 'Raspberry Pi Trading Ltd', oui: 'dc:a6:32', name: 'edge-gw', desc: 'This tracker', ports: [22], chatty: 25 },
  { vendor: '', oui: 'da:4f:21', name: 'Android_8F3C21', ports: [], chatty: 30, dhcp: true, la: true, flaky: true },
  { vendor: 'Apple, Inc.', oui: '3c:22:fb', name: 'maintenance-ipad', ports: [], chatty: 26, dhcp: true, flaky: true },
  { vendor: 'Schneider Electric', oui: '00:80:f4', name: 'm221-wtp', ports: [502], chatty: 15 },
  { vendor: 'Rockwell Automation', oui: '00:00:bc', name: 'cx-1769', ports: [44818], chatty: 16 },
];

const BUILDING: Model[] = [
  { vendor: 'Loxone Electronics GmbH', oui: '50:4f:94', name: 'loxone-miniserver', desc: 'Building controller', ports: [80], chatty: 30 },
  { vendor: 'Siemens Schweiz AG', oui: '00:a0:03', name: 'desigo-pxc', ports: [47808, 80], chatty: 20 },
  { vendor: 'Honeywell', oui: '00:40:84', name: 'hvac-ahu1', ports: [47808], chatty: 12 },
  { vendor: 'Mitsubishi Electric', oui: '00:26:92', name: 'vrf-gateway', ports: [502], chatty: 10, flaky: true },
  { vendor: 'Ubiquiti Inc.', oui: '24:5a:4c', name: 'ap-lobby', ports: [443], chatty: 45 },
  { vendor: 'Ubiquiti Inc.', oui: '24:5a:4c', name: 'ap-office', ports: [443], chatty: 44 },
  { vendor: 'Hikvision', oui: 'c0:56:e3', name: 'nvr-01', ports: [554, 80], chatty: 60 },
  { vendor: 'Lenovo', oui: '8c:16:45', name: 'facility-pc', ports: [3389], chatty: 50, dhcp: true },
  { vendor: 'Canon Inc.', oui: '00:00:85', name: 'printer-2f', ports: [631, 9100], chatty: 8, dhcp: true },
  { vendor: '', oui: '7a:13:9c', name: 'iPhone', ports: [], chatty: 22, dhcp: true, la: true, flaky: true },
  { vendor: 'Shelly', oui: 'e8:68:e7', name: 'shelly-pm-kitchen', ports: [80], chatty: 6, dhcp: true },
  { vendor: 'TP-Link', oui: '50:c7:bf', name: 'tapo-plug', ports: [], chatty: 7, dhcp: true, flaky: true },
];

const SOURCES = ['passive_arp', 'passive_ipv4', 'kernel_neighbor', 'passive_mdns', 'passive_dhcp', 'passive_dns', 'passive_lldp', 'arp_scan', 'tcp_connect'];
const SOURCE_SHARE = [0.32, 0.22, 0.12, 0.1, 0.05, 0.06, 0.04, 0.06, 0.03];

interface MockHost {
  s: HostSummary;
  m: Model;
  addresses: Host['addresses'];
}

export class MockApi implements Api {
  private readonly now = Date.now();
  private readonly hostList: MockHost[] = [];
  private readonly evs: LTEvent[] = [];
  private readonly eps: Endpoint[] = [];
  private readonly leases: DHCPLease[] = [];
  private readonly exchanges: DHCPExchange[] = [];
  private readonly servers: DHCPServer[] = [];
  private nextId = 1;

  constructor() {
    const r = rng(20261121);
    const nets: [string, string, Model[]][] = [
      ['eth1', '192.168.110', CELL],
      ['eth2', '10.20.0', BUILDING],
    ];
    const start = this.now - 30 * DAY;
    for (const [iface, net, models] of nets) {
      models.forEach((m, i) => {
        const mac = `${m.oui}:${hex(r())}:${hex(r())}:${hex(r())}`;
        const first = start + Math.floor(r() * 20 * DAY);
        const ago = m.flaky ? r() * 3 * DAY : r() * 4 * 60_000;
        const last = this.now - ago;
        const presence: Presence = ago < 5 * 60_000 ? 'ACTIVE' : ago < 30 * 60_000 ? 'RECENT' : ago < DAY ? 'STALE' : 'MISSING';
        const ipLast = m.dhcp ? 100 + i : 10 + i * 2;
        const ip = `${net}.${ipLast}`;
        const addresses: Host['addresses'] = [];
        if (m.dhcp && r() < 0.6) {
          const prev = `${net}.${150 + i}`;
          const moved = first + (last - first) * (0.3 + r() * 0.4);
          addresses.push({ ip: prev, first_seen: t(first), last_seen: t(moved - 60_000), ended_at: t(moved), conflict: false });
          addresses.push({ ip, first_seen: t(moved), last_seen: t(last), ended_at: null, conflict: false });
          this.ev(moved, 'IP_REMOVED', 'notice', iface, mac, { old: prev, cause: 'address passed to another MAC' });
          this.ev(moved + 1000, 'IP_ADDED', 'notice', iface, mac, { new: ip, cause: 'DHCP REQUEST' });
        } else {
          addresses.push({ ip, first_seen: t(first), last_seen: t(last), ended_at: null, conflict: false });
        }
        const s: HostSummary = {
          host_id: `h-${iface}-${i}`,
          interface: iface,
          mac,
          oui_registrant: m.vendor || undefined,
          locally_administered: !!m.la,
          presence,
          preferred_name: m.name,
          description: m.desc,
          ips: presence === 'MISSING' ? [] : [ip],
          first_seen: t(first),
          last_seen: t(last),
          last_frame_seen: t(last),
          last_address_confirmed: t(last - 30_000),
        };
        this.hostList.push({ s, m, addresses });
        this.ev(first, 'HOST_DISCOVERED', 'notice', iface, mac, { new: ip, cause: 'first frame from MAC' }, s.host_id);
        this.ev(first + 2000, 'HOSTNAME_ADDED', 'notice', iface, mac, { new: m.name, cause: m.dhcp ? 'DHCP option 12' : 'mDNS announcement' }, s.host_id);
        if (m.desc) this.ev(first + DAY, 'HOST_DESCRIBED', 'notice', iface, mac, { new: m.desc, cause: 'operator bart' }, s.host_id);
        // Flaky devices come and go.
        if (m.flaky) {
          for (let k = 0; k < 4 + Math.floor(r() * 10); k++) {
            const at = start + r() * 30 * DAY;
            this.ev(at, 'HOST_DISAPPEARED', 'notice', iface, mac, { cause: 'no presence evidence for 24h' }, s.host_id);
            this.ev(at + r() * 6 * H, 'HOST_REAPPEARED', 'notice', iface, mac, { cause: 'ARP reply' }, s.host_id);
          }
        }
        m.ports.forEach((port) => {
          const res: TCPResult = m.flaky && r() < 0.5 ? (r() < 0.5 ? 'TIMEOUT' : 'REFUSED') : r() < 0.06 ? 'REFUSED' : 'OPEN';
          this.eps.push({
            interface: iface,
            ip,
            proto: 'tcp',
            port,
            host_id: s.host_id,
            mac,
            last_result: res,
            last_result_at: t(this.now - r() * 15 * 60_000),
            last_open_at: res === 'OPEN' ? t(this.now - r() * 15 * 60_000) : t(this.now - r() * 3 * DAY),
            last_definite_result: res === 'TIMEOUT' ? 'OPEN' : res,
            first_seen: t(first),
          });
          if (res !== 'OPEN') {
            this.ev(this.now - r() * 2 * DAY, 'SERVICE_STATE_CHANGED', 'notice', iface, mac, { old: `tcp/${port} OPEN`, new: `tcp/${port} ${res}`, cause: 'TCP check' }, s.host_id);
          }
        });
        if (m.dhcp) {
          for (let k = 0; k < 3 + Math.floor(r() * 6); k++) {
            const at = this.now - r() * 30 * DAY;
            this.leases.push({
              id: this.leases.length + 1,
              interface: iface,
              chaddr: mac,
              host_id: s.host_id,
              ip,
              server_id: `${net}.2`,
              sender_mac: '00:50:56:aa:00:02',
              first_ack: t(at),
              last_ack: t(at + 4 * H),
              acks: 1 + Math.floor(r() * 5),
              lease_seconds: 86400,
              expires_at: t(at + DAY),
              config: { routers: [`${net}.1`], dns: [`${net}.2`], subnet_mask: '255.255.255.0' },
            });
            this.ev(at, 'DHCP_LEASE_ASSIGNED', 'notice', iface, mac, { new: ip, cause: `ACK from ${net}.2` }, s.host_id);
          }
        }
      });
    }
    const outcomes = ['ack', 'ack', 'ack', 'ack', 'ack', 'ack', 'ack', 'ack', 'offered', 'nak', 'incomplete', 'decline', 'release', 'inform_ack'];
    const dhcpHosts = this.hostList.filter((x) => x.m.dhcp);
    for (let k = 0; k < 420; k++) {
      const h = dhcpHosts[Math.floor(r() * dhcpHosts.length)]!;
      const at = this.now - r() * 30 * DAY;
      const outcome = outcomes[Math.floor(r() * outcomes.length)]!;
      this.exchanges.push({
        id: k + 1,
        interface: h.s.interface,
        xid: Math.floor(r() * 2 ** 32),
        chaddr: h.s.mac,
        host_id: h.s.host_id,
        first_seen: t(at),
        last_seen: t(at + 400),
        messages: outcome === 'ack' ? 'DISCOVER OFFER REQUEST ACK' : outcome.toUpperCase(),
        offers: [],
        outcome,
        visibility: r() < 0.7 ? 'complete' : 'partial',
      });
      if (outcome === 'nak') this.ev(at, 'DHCP_LEASE_NAK', 'warning', h.s.interface, h.s.mac, { cause: 'NAK: requested address not on this network' }, h.s.host_id);
      if (outcome === 'decline') this.ev(at, 'DHCP_LEASE_DECLINED', 'warning', h.s.interface, h.s.mac, { cause: 'client found the address in use' }, h.s.host_id);
    }
    this.servers.push(
      { interface: 'eth1', server_id: '192.168.110.2', mac: '00:50:56:aa:00:02', ip: '192.168.110.2', status: 'allowed', config: { router: '192.168.110.1', dns: '192.168.110.2', subnet_mask: '255.255.255.0', domain: 'cell.plant.local' }, first_seen: t(start), last_seen: t(this.now - 120_000) },
      { interface: 'eth2', server_id: '10.20.0.2', mac: '00:50:56:aa:10:02', ip: '10.20.0.2', status: 'allowed', config: { router: '10.20.0.1', dns: '10.20.0.2', subnet_mask: '255.255.255.0' }, first_seen: t(start), last_seen: t(this.now - 300_000) },
      { interface: 'eth2', server_id: '192.168.0.1', mac: '50:c7:bf:12:34:56', ip: '192.168.0.1', status: 'unexpected', config: { router: '192.168.0.1', dns: '192.168.0.1', subnet_mask: '255.255.255.0' }, first_seen: t(this.now - 2 * DAY - 3 * H), last_seen: t(this.now - 2 * DAY) },
    );
    this.ev(this.now - 2 * DAY - 3 * H, 'DHCP_SERVER_DISCOVERED', 'notice', 'eth2', '50:c7:bf:12:34:56', { new: '192.168.0.1', cause: 'DHCP OFFER' });
    this.ev(this.now - 2 * DAY - 3 * H + 10, 'DHCP_SERVER_UNEXPECTED', 'warning', 'eth2', '50:c7:bf:12:34:56', { new: '192.168.0.1', cause: 'server not in the allowlist' });
    // A duplicate IP: the HMI and a laptop briefly claim the same address.
    const hmi = this.hostList.find((x) => x.m.name === 'hmi-line2')!;
    const lap = this.hostList.find((x) => x.m.name === 'eng-laptop-07')!;
    const dup = this.now - 9 * H;
    hmi.addresses[hmi.addresses.length - 1]!.conflict = true;
    this.ev(dup, 'DUPLICATE_IP_DETECTED', 'warning', 'eth1', hmi.s.mac, { new: hmi.addresses.at(-1)!.ip, cause: 'ARP replies from two MACs', related: lap.s.mac }, hmi.s.host_id);
    this.ev(dup + 47 * 60_000, 'DUPLICATE_IP_RESOLVED', 'notice', 'eth1', hmi.s.mac, { new: hmi.addresses.at(-1)!.ip, cause: 'one holder left' }, hmi.s.host_id);
    this.ev(this.now - 5 * DAY, 'INTERFACE_DOWN', 'warning', 'eth2', undefined, { cause: 'link down' });
    this.ev(this.now - 5 * DAY + 40 * 60_000, 'INTERFACE_UP', 'notice', 'eth2', undefined, { cause: 'link up' });
    this.ev(this.now - 5 * DAY + 41 * 60_000, 'OBSERVATION_GAP', 'warning', 'eth2', undefined, { cause: 'capture stopped for 40m' });
    this.ev(this.now - 3 * DAY, 'ACTIVE_DISABLED', 'warning', '', undefined, { cause: 'operator bart: PLC firmware update' });
    this.ev(this.now - 3 * DAY + 2 * H, 'ACTIVE_ENABLED', 'notice', '', undefined, { cause: 'operator bart' });
    this.evs.sort((a, b) => a.ts.localeCompare(b.ts));
  }

  private ev(at: number, type: string, severity: LTEvent['severity'], iface: string, mac: string | undefined, f: { old?: string; new?: string; cause: string; related?: string }, hostId?: string) {
    if (at > this.now) return;
    this.evs.push({
      id: this.nextId++,
      ts: t(at),
      type,
      severity,
      interface: iface || undefined,
      host_id: hostId,
      mac,
      related_mac: f.related,
      old: f.old,
      new: f.new,
      cause: f.cause,
      evidence: f.new && /^\d+\.\d+\.\d+\.\d+$/.test(f.new) ? { ip: f.new } : null,
      clock_sync: 'synced',
    });
  }

  private delay<T>(v: T): Promise<T> {
    return new Promise((res) => setTimeout(() => res(structuredClone(v)), 120));
  }

  status(): Promise<Status> {
    const hosts: Status['hosts'] = {};
    for (const h of this.hostList) {
      const m = (hosts[h.s.interface] ??= {});
      m[h.s.presence] = (m[h.s.presence] ?? 0) + 1;
    }
    const ifs = this.ifs().map((i) => ({
      ...i,
      collectors: ['capture', 'neighbor', 'interface', 'arp', 'tcp'].map((c) => ({
        interface: i.name,
        collector: c,
        backend: c === 'capture' ? 'afpacket' : c === 'neighbor' || c === 'interface' ? 'netlink' : 'socket',
        state: i.name === 'eth2' && c === 'tcp' ? 'disabled' : 'running',
        since: t(this.now - 6 * DAY),
        last_pass: c === 'arp' || c === 'tcp' ? { at: t(this.now - 4 * 60_000), seconds: c === 'arp' ? 51.2 : 8.4, probed: c === 'arp' ? 254 : 44, replied: c === 'arp' ? 21 : 39, complete: true } : undefined,
      })),
    }));
    return this.delay({
      version: 'v0.3.0-dev',
      commit: 'mock',
      platform: 'linux/arm64',
      pid: 812,
      started: t(this.now - 6 * DAY),
      now: t(this.now),
      replay: false,
      state: 'ok',
      database: { path: '/data/lan-tracker/hosts.db', ok: true, size: 61_400_000, used_size: 58_900_000, wal_size: 2_100_000, schema_version: 1 },
      active: { disabled: false },
      clock: 'synced',
      interfaces: ifs,
      hosts,
      last_scan: { id: 7, interface: 'eth1', kind: 'arp', trigger: 'operator', started: t(this.now - DAY), finished: t(this.now - DAY + 52_000), targets: 254 },
    });
  }

  private ifs(): InterfaceInfo[] {
    return [
      { name: 'eth1', state: 'up', mac: 'dc:a6:32:01:02:03', prefixes: ['192.168.110.0/24'], passive: true, active: true, hosts: CELL.length, first_seen: t(this.now - 30 * DAY), last_seen: t(this.now) },
      { name: 'eth2', state: 'up', mac: 'dc:a6:32:01:02:04', prefixes: ['10.20.0.0/24'], passive: true, active: false, hosts: BUILDING.length, first_seen: t(this.now - 30 * DAY), last_seen: t(this.now) },
    ];
  }

  interfaces() {
    return this.delay(this.ifs());
  }
  hosts(iface: string) {
    return this.delay(this.hostList.filter((h) => !iface || h.s.interface === iface).map((h) => h.s));
  }
  host(id: string): Promise<Host> {
    const h = this.hostList.find((x) => x.s.host_id === id);
    if (!h) return Promise.reject(new Error('unknown host ' + id));
    return this.delay({
      ...h.s,
      addresses: h.addresses,
      names: [
        { name: h.m.name, type: h.m.dhcp ? 'dhcp' : 'mdns', record_type: 'NAME', source: h.m.dhcp ? 'passive_dhcp' : 'passive_mdns', link: 'sender', advertiser_mac: h.s.mac, first_seen: h.s.first_seen, last_seen: h.s.last_seen, ended_at: null, freshness: 'fresh' },
        ...(h.m.dhcp ? [] : [{ name: `${h.m.name}.plant.local`, type: 'dns_ptr', record_type: 'PTR', address: h.addresses.at(-1)!.ip, source: 'passive_dns', link: 'address', first_seen: h.s.first_seen, last_seen: h.s.last_seen, ended_at: null, freshness: 'stale' }]),
      ],
      endpoints: this.eps.filter((e) => e.host_id === id),
      dhcp_leases: this.leases.filter((l) => l.host_id === id).sort((a, b) => b.first_ack.localeCompare(a.first_ack)),
    });
  }
  hostEvents(id: string, since: Date) {
    return this.delay(this.evs.filter((e) => e.host_id === id && Date.parse(e.ts) >= since.getTime()));
  }
  events(scope: Scope, limit = 2000) {
    const out = this.evs.filter((e) => inScope(e.ts, scope) && (!scope.iface || e.interface === scope.iface));
    return this.delay(out.slice(-limit));
  }

  activity(scope: Scope, buckets: number, hostId?: string): Promise<Activity> {
    const since = scope.range.since.getTime();
    const until = scope.range.until.getTime();
    let step = Math.max(1000, Math.ceil((until - since) / buckets / 1000) * 1000);
    const n = Math.ceil((until - since) / step);
    const events: Record<string, number[]> = {};
    for (const e of this.evs) {
      const ts = Date.parse(e.ts);
      if (ts < since || ts >= until) continue;
      if (hostId ? e.host_id !== hostId : scope.iface && e.interface !== scope.iface) continue;
      const row = (events[e.type] ??= new Array<number>(n).fill(0));
      const i = Math.min(n - 1, Math.floor((ts - since) / step));
      row[i] = (row[i] ?? 0) + 1;
    }
    const hosts = this.hostList.filter((h) => (hostId ? h.s.host_id === hostId : !scope.iface || h.s.interface === scope.iface));
    const r = rng(since / 60000 + (hostId ? hostId.length : 0));
    const observations: Record<string, number[]> = {};
    const totals = new Map<string, number>();
    for (let b = 0; b < n; b++) {
      const mid = since + (b + 0.5) * step;
      const hour = new Date(mid).getHours();
      const day = new Date(mid).getDay();
      const workday = day > 0 && day < 6;
      // Plant traffic follows the shifts; spikes when laptops join.
      const diurnal = (hour >= 6 && hour < 22 ? 1 : 0.45) * (workday ? 1 : 0.7);
      let total = 0;
      for (const h of hosts) {
        const lastSeen = Date.parse(h.s.last_seen);
        const firstSeen = Date.parse(h.s.first_seen);
        if (mid < firstSeen || mid > lastSeen + 10 * 60_000) continue;
        const human = h.m.dhcp ? (hour >= 7 && hour < 18 && workday ? 1.6 : 0.15) : 1;
        const c = h.m.chatty * (step / H) * diurnal * human * (0.7 + r() * 0.6);
        total += c;
        totals.set(h.s.host_id, (totals.get(h.s.host_id) ?? 0) + c);
      }
      if (r() < 0.04) total *= 2.5 + r() * 2;
      SOURCES.forEach((src, i) => {
        (observations[src] ??= new Array(n).fill(0))[b] = Math.round(total * SOURCE_SHARE[i]! * (0.8 + r() * 0.4));
      });
    }
    const top_hosts = hostId
      ? []
      : [...totals.entries()]
          .sort((a, b) => b[1] - a[1])
          .slice(0, 10)
          .map(([id, c]) => {
            const h = this.hostList.find((x) => x.s.host_id === id)!;
            return { host_id: id, interface: h.s.interface, mac: h.s.mac, preferred_name: h.s.preferred_name, description: h.s.description, observations: Math.round(c) };
          });
    step = step / 1000;
    return this.delay({ since: t(since), until: t(until), step_seconds: step, buckets: n, events, observations, top_hosts });
  }

  services(iface: string) {
    return this.delay(this.eps.filter((e) => !iface || e.interface === iface));
  }
  dhcpServers(iface: string) {
    return this.delay(this.servers.filter((s) => !iface || s.interface === iface));
  }
  dhcpLeases(iface: string) {
    return this.delay(this.leases.filter((l) => !iface || l.interface === iface).sort((a, b) => b.first_ack.localeCompare(a.first_ack)));
  }
  dhcpExchanges(iface: string, since: Date) {
    return this.delay(this.exchanges.filter((x) => (!iface || x.interface === iface) && Date.parse(x.first_seen) >= since.getTime()).sort((a, b) => b.first_seen.localeCompare(a.first_seen)));
  }
  setDescription(id: string, description: string) {
    const h = this.hostList.find((x) => x.s.host_id === id);
    if (!h) return Promise.reject(new Error('unknown host'));
    h.s.description = description || undefined;
    this.ev(Date.now(), 'HOST_DESCRIBED', 'notice', h.s.interface, h.s.mac, { new: description, cause: 'ui' }, id);
    return this.delay(h.s);
  }

  stream(iface: string, onEvent: (e: LTEvent) => void, onState: (live: boolean) => void): () => void {
    onState(true);
    const r = rng(7);
    const timer = setInterval(() => {
      const hosts = this.hostList.filter((h) => !iface || h.s.interface === iface);
      const h = hosts[Math.floor(r() * hosts.length)]!;
      const kinds: [string, LTEvent['severity'], string][] = [
        ['HOST_REAPPEARED', 'notice', 'ARP reply'],
        ['DHCP_LEASE_ASSIGNED', 'notice', 'ACK from server'],
        ['SERVICE_STATE_CHANGED', 'notice', 'TCP check'],
        ['HOSTNAME_ADDED', 'notice', 'mDNS announcement'],
      ];
      const [type, sev, cause] = kinds[Math.floor(r() * kinds.length)]!;
      onEvent({ id: this.nextId++, ts: t(Date.now()), type, severity: sev, interface: h.s.interface, host_id: h.s.host_id, mac: h.s.mac, new: h.s.ips[0], cause, evidence: null, clock_sync: 'synced' });
    }, 6000);
    return () => clearInterval(timer);
  }
}

function hex(x: number): string {
  return Math.floor(x * 256)
    .toString(16)
    .padStart(2, '0');
}

function t(ms: number): string {
  return new Date(ms).toISOString();
}

function inScope(ts: string, scope: Scope): boolean {
  const v = Date.parse(ts);
  return v >= scope.range.since.getTime() && v <= scope.range.until.getTime();
}
