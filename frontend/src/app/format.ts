// Labels, groupings and formatting shared by the views.

/** Event categories, in the fixed series order of the chart palette. */
export interface Category {
  key: string;
  label: string;
  color: string; // a series token
  types: string[];
}

export const EVENT_CATEGORIES: Category[] = [
  { key: 'devices', label: 'Devices', color: 'var(--s1)', types: ['HOST_DISCOVERED', 'HOST_DISAPPEARED', 'HOST_REAPPEARED', 'HOST_DESCRIBED', 'MAC_ALSO_OBSERVED'] },
  {
    key: 'dhcp',
    label: 'DHCP',
    color: 'var(--s2)',
    types: ['DHCP_SERVER_DISCOVERED', 'DHCP_SERVER_UNEXPECTED', 'DHCP_SERVER_MAC_CHANGED', 'DHCP_LEASE_ASSIGNED', 'DHCP_CLIENT_CONFIG_CHANGED', 'DHCP_LEASE_NAK', 'DHCP_LEASE_DECLINED', 'DHCP_LEASE_RELEASED'],
  },
  { key: 'addresses', label: 'Addresses', color: 'var(--s3)', types: ['IP_ADDED', 'IP_REMOVED', 'SUBNET_CHANGED'] },
  { key: 'tracker', label: 'Tracker', color: 'var(--s4)', types: ['SCAN_STARTED', 'SCAN_COMPLETED', 'ACTIVE_DISABLED', 'ACTIVE_ENABLED', 'INTERFACE_UP', 'INTERFACE_DOWN', 'DATABASE_RECREATED', 'OBSERVATION_GAP'] },
  { key: 'names', label: 'Names', color: 'var(--s5)', types: ['HOSTNAME_ADDED', 'HOSTNAME_REMOVED'] },
  { key: 'services', label: 'Services', color: 'var(--s6)', types: ['SERVICE_STATE_CHANGED'] },
  { key: 'conflicts', label: 'Conflicts', color: 'var(--s7)', types: ['DUPLICATE_IP_DETECTED', 'DUPLICATE_IP_RESOLVED', 'PROXY_ARP_DETECTED'] },
];

const categoryOf = new Map<string, Category>();
for (const c of EVENT_CATEGORIES) for (const t of c.types) categoryOf.set(t, c);

export function eventCategory(type: string): Category {
  return categoryOf.get(type) ?? EVENT_CATEGORIES[3]!;
}

const EVENT_LABELS: Record<string, string> = {
  HOST_DISCOVERED: 'New device',
  HOST_DISAPPEARED: 'Device went quiet',
  HOST_REAPPEARED: 'Device back',
  HOST_DESCRIBED: 'Description set',
  IP_ADDED: 'Address gained',
  IP_REMOVED: 'Address ended',
  MAC_ALSO_OBSERVED: 'MAC also seen',
  PROXY_ARP_DETECTED: 'Proxy ARP',
  DUPLICATE_IP_DETECTED: 'Duplicate IP',
  DUPLICATE_IP_RESOLVED: 'Duplicate IP resolved',
  HOSTNAME_ADDED: 'Name added',
  HOSTNAME_REMOVED: 'Name ended',
  SERVICE_STATE_CHANGED: 'Port state changed',
  SCAN_STARTED: 'Scan started',
  SCAN_COMPLETED: 'Scan completed',
  ACTIVE_DISABLED: 'Active discovery off',
  ACTIVE_ENABLED: 'Active discovery on',
  INTERFACE_UP: 'Interface up',
  INTERFACE_DOWN: 'Interface down',
  SUBNET_CHANGED: 'Subnet changed',
  DATABASE_RECREATED: 'Database recreated',
  OBSERVATION_GAP: 'Observation gap',
  DHCP_SERVER_DISCOVERED: 'DHCP server seen',
  DHCP_SERVER_UNEXPECTED: 'Unexpected DHCP server',
  DHCP_SERVER_MAC_CHANGED: 'DHCP server MAC changed',
  DHCP_LEASE_ASSIGNED: 'Lease assigned',
  DHCP_CLIENT_CONFIG_CHANGED: 'Client config changed',
  DHCP_LEASE_NAK: 'Lease refused (NAK)',
  DHCP_LEASE_DECLINED: 'Lease declined',
  DHCP_LEASE_RELEASED: 'Lease released',
};

export function eventLabel(type: string): string {
  return EVENT_LABELS[type] ?? type.toLowerCase().replaceAll('_', ' ');
}

/** Observation sources grouped by how the tracker learned something. */
export const SOURCE_GROUPS: Category[] = [
  { key: 'arp', label: 'ARP', color: 'var(--s1)', types: ['passive_arp', 'arp_scan'] },
  { key: 'dhcp', label: 'DHCP', color: 'var(--s2)', types: ['passive_dhcp', 'passive_dhcp_server'] },
  { key: 'ip', label: 'IP traffic', color: 'var(--s3)', types: ['passive_ipv4', 'passive_ipv6', 'passive_ndp'] },
  { key: 'neighbor', label: 'Neighbour table', color: 'var(--s4)', types: ['kernel_neighbor'] },
  { key: 'names', label: 'Names', color: 'var(--s5)', types: ['passive_mdns', 'passive_dns', 'passive_lldp', 'dns_query', 'mdns_query'] },
  { key: 'probes', label: 'Probes', color: 'var(--s6)', types: ['icmp_scan', 'tcp_connect', 'ndp_probe'] },
];

/** Sums per-type bucket counts into the given groups. */
export function group(counts: Record<string, number[]>, groups: Category[], n: number): { cat: Category; values: number[] }[] {
  return groups.map((cat) => {
    const values = new Array<number>(n).fill(0);
    for (const t of cat.types) {
      const c = counts[t];
      if (!c) continue;
      for (let i = 0; i < n; i++) values[i]! += c[i] ?? 0;
    }
    return { cat, values };
  });
}

export const PRESENCE: { key: 'ACTIVE' | 'RECENT' | 'STALE' | 'MISSING'; label: string; color: string; hint: string }[] = [
  { key: 'ACTIVE', label: 'Active', color: 'var(--st-good)', hint: 'Seen in the last 5 minutes' },
  { key: 'RECENT', label: 'Recent', color: 'var(--st-info)', hint: 'Seen in the last 30 minutes' },
  { key: 'STALE', label: 'Stale', color: 'var(--st-warn)', hint: 'Not seen for 30 minutes; silence is not offline' },
  { key: 'MISSING', label: 'Missing', color: 'var(--s-other)', hint: 'Not seen for a day; silence is not offline' },
];

export function presenceOf(key: string) {
  return PRESENCE.find((p) => p.key === key) ?? PRESENCE[3]!;
}

export const RANGES = [
  { key: '1h', label: '1 hour', ms: 3600_000 },
  { key: '6h', label: '6 hours', ms: 6 * 3600_000 },
  { key: '24h', label: '24 hours', ms: 24 * 3600_000 },
  { key: '7d', label: '7 days', ms: 7 * 24 * 3600_000 },
  { key: '30d', label: '30 days', ms: 30 * 24 * 3600_000 },
] as const;

export type RangeKey = (typeof RANGES)[number]['key'];

const nf = new Intl.NumberFormat();
export const num = (n: number) => nf.format(Math.round(n));

export function compact(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(n >= 1e7 ? 0 : 1)}M`;
  if (n >= 1e4) return `${Math.round(n / 1e3)}k`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(Math.round(n));
}

export function bytes(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)} GB`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)} MB`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(0)} kB`;
  return `${n} B`;
}

/** "3 min ago", "2 h ago", "4 d ago". */
export function ago(iso: string | undefined, now = Date.now()): string {
  if (!iso) return '—';
  const s = Math.max(0, (now - Date.parse(iso)) / 1000);
  if (s < 45) return 'just now';
  if (s < 3600) return `${Math.round(s / 60)} min ago`;
  if (s < 86400) return `${Math.round(s / 3600)} h ago`;
  return `${Math.round(s / 86400)} d ago`;
}

export function duration(ms: number): string {
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s} s`;
  if (s < 3600) return `${Math.round(s / 60)} min`;
  if (s < 86400) return `${(s / 3600).toFixed(s < 36000 ? 1 : 0)} h`;
  return `${(s / 86400).toFixed(1)} d`;
}

export function dateTime(iso: string): string {
  return new Date(iso).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' });
}

export function axisTime(ms: number, spanMs: number): string {
  const d = new Date(ms);
  if (spanMs <= 2 * 86400_000) return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

/** The host's name to show: the preferred name, else the description, else the MAC. */
export function hostLabel(h: { description?: string; preferred_name?: string; mac: string }): string {
  return h.preferred_name || h.description || h.mac;
}

/** The CLI command that shows the same thing, to copy. */
// EVENT_TYPE is the form of an event type (IP_ADDED). A type from the URL
// that does not have it is ignored, so it never reaches the filter or the
// command line the page shows.
const EVENT_TYPE = /^[A-Z][A-Z0-9_]*$/;

export function eventTypeParam(t: string | null | undefined): string {
  return t && EVENT_TYPE.test(t) ? t : '';
}

export function cli(cmd: string): string {
  return `sudo lan-tracker ${cmd}`;
}
