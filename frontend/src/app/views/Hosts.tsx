import { useMemo, useState } from 'react';
import type { HostSummary } from '../../api/types';
import { useApp } from '../context';
import { PRESENCE, ago, cli, presenceOf } from '../format';
import { Card, Chip, Command, Dot, ErrorNote, SearchIcon, Segmented, Skeleton, useLoad } from '../ui';

type SortKey = 'name' | 'ip' | 'mac' | 'vendor' | 'last_seen' | 'first_seen' | 'presence';

const presenceRank: Record<string, number> = { ACTIVE: 0, RECENT: 1, STALE: 2, MISSING: 3 };

function ipKey(ip: string | undefined): string {
  if (!ip) return '~';
  return ip.includes('.') ? ip.split('.').map((p) => p.padStart(3, '0')).join('.') : ip;
}

function matches(h: HostSummary, q: string): boolean {
  if (!q) return true;
  const hay = [h.preferred_name, h.description, h.mac, h.oui_registrant, h.interface, ...h.ips].join(' ').toLowerCase();
  return q
    .toLowerCase()
    .split(/\s+/)
    .every((w) => hay.includes(w));
}

export function Hosts() {
  const { api, scope, tick, navigate } = useApp();
  const params = new URLSearchParams(location.search);
  const [q, setQ] = useState(params.get('q') ?? '');
  const [vendor, setVendor] = useState(params.get('vendor') ?? '');
  const [presence, setPresence] = useState<'all' | 'live' | 'notlive'>('all');
  const [sort, setSort] = useState<{ key: SortKey; desc: boolean }>({ key: 'presence', desc: false });
  const hosts = useLoad(() => api.hosts(scope.iface), [api, scope.iface, tick]);

  const rows = useMemo(() => {
    const list = (hosts.data ?? []).filter((h) => {
      if (!matches(h, q)) return false;
      if (vendor && (h.oui_registrant || (h.locally_administered ? 'Locally administered (random) MAC' : 'Unknown registrant')) !== vendor) return false;
      const live = h.presence === 'ACTIVE' || h.presence === 'RECENT';
      return presence === 'all' || (presence === 'live') === live;
    });
    const val = (h: HostSummary): string | number => {
      switch (sort.key) {
        case 'name':
          return (h.preferred_name || h.description || '~').toLowerCase();
        case 'ip':
          return ipKey(h.ips[0]);
        case 'mac':
          return h.mac;
        case 'vendor':
          return (h.oui_registrant || '~').toLowerCase();
        case 'last_seen':
          return Date.parse(h.last_seen);
        case 'first_seen':
          return Date.parse(h.first_seen);
        case 'presence':
          return presenceRank[h.presence]! * 1e15 - Date.parse(h.last_seen) / 1000;
      }
    };
    return list.sort((a, b) => {
      const x = val(a);
      const y = val(b);
      const c = x < y ? -1 : x > y ? 1 : ipKey(a.ips[0]).localeCompare(ipKey(b.ips[0]));
      return sort.desc ? -c : c;
    });
  }, [hosts.data, q, vendor, presence, sort]);

  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const h of hosts.data ?? []) c[h.presence] = (c[h.presence] ?? 0) + 1;
    return c;
  }, [hosts.data]);

  const th = (key: SortKey, label: string, cls = '') => (
    <th className={`px-3 py-2 text-left ${cls}`} aria-sort={sort.key === key ? (sort.desc ? 'descending' : 'ascending') : undefined}>
      <button type="button" className="ot-label cursor-pointer hover:text-fg" onClick={() => setSort((s) => ({ key, desc: s.key === key ? !s.desc : key === 'last_seen' || key === 'first_seen' }))}>
        {label}
        {sort.key === key ? (sort.desc ? ' ↓' : ' ↑') : ''}
      </button>
    </th>
  );

  return (
    <div className="mx-auto flex max-w-[1600px] flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="font-display text-2xl font-semibold text-fg-strong">Devices</h1>
        <span className="flex flex-wrap gap-3 text-[12px] text-muted">
          {PRESENCE.map((p) => (
            <Dot key={p.key} color={p.color} label={`${p.label} ${counts[p.key] ?? 0}`} />
          ))}
        </span>
        <span className="ml-auto">
          <Command cmd={cli(`hosts list${scope.iface ? ` --interface ${scope.iface}` : ''}`)} />
        </span>
      </div>
      <Card>
        <div className="mb-3 flex flex-wrap items-center gap-3">
          <label className="flex min-w-[260px] flex-1 items-center gap-2 rounded border border-control bg-bg px-2.5 py-1.5">
            <SearchIcon />
            <input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="Search name, IP, MAC, registrant, description"
              className="w-full bg-transparent text-[13px] outline-none placeholder:text-muted"
              aria-label="Search devices"
            />
          </label>
          <Segmented
            label="Presence"
            value={presence}
            onChange={setPresence}
            options={[
              { key: 'all', label: 'all' },
              { key: 'live', label: 'live' },
              { key: 'notlive', label: 'not live' },
            ]}
          />
          {vendor && (
            <button type="button" className="cursor-pointer" onClick={() => setVendor('')} title="Remove the filter">
              <Chip tone="accent">registrant: {vendor} ✕</Chip>
            </button>
          )}
          <span className="font-mono text-[12px] text-muted">
            {rows.length} of {hosts.data?.length ?? 0}
          </span>
        </div>
        {hosts.error !== undefined && <ErrorNote error={hosts.error} />}
        {!hosts.data ? (
          <Skeleton height={400} />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-[13px]">
              <thead className="border-b border-line">
                <tr>
                  {th('presence', 'Presence')}
                  {th('name', 'Name')}
                  {th('ip', 'Addresses')}
                  {th('mac', 'MAC')}
                  {th('vendor', 'Registrant', 'hidden lg:table-cell')}
                  {th('first_seen', 'First seen', 'hidden xl:table-cell')}
                  {th('last_seen', 'Last seen')}
                </tr>
              </thead>
              <tbody>
                {rows.map((h) => {
                  const p = presenceOf(h.presence);
                  return (
                    <tr
                      key={h.host_id}
                      className="cursor-pointer border-b border-line/50 hover:bg-accent-soft/30"
                      onClick={() => navigate(`/hosts/${encodeURIComponent(h.host_id)}`)}
                      tabIndex={0}
                      onKeyDown={(e) => e.key === 'Enter' && navigate(`/hosts/${encodeURIComponent(h.host_id)}`)}
                    >
                      <td className="px-3 py-2" title={p.hint}>
                        <Dot color={p.color} label={<span className="text-[12px]">{p.label}</span>} pulse={h.presence === 'ACTIVE'} />
                      </td>
                      <td className="max-w-[320px] px-3 py-2">
                        <div className="truncate font-medium text-fg-strong">{h.preferred_name || <span className="text-muted">—</span>}</div>
                        {h.description && <div className="truncate text-[12px] text-muted">{h.description}</div>}
                      </td>
                      <td className="px-3 py-2 font-mono text-[12px]">
                        {h.ips.length ? h.ips.join(', ') : <span className="text-muted">none open</span>}
                        {h.proxy_arp && (
                          <span className="ml-2">
                            <Chip tone="warn" title="Answers ARP for addresses other hosts hold: its ARP replies prove nothing">
                              proxy ARP
                            </Chip>
                          </span>
                        )}
                      </td>
                      <td className="px-3 py-2 font-mono text-[12px] whitespace-nowrap">
                        {h.mac}
                        {h.locally_administered && (
                          <span className="ml-2">
                            <Chip title="Locally administered: often a randomised MAC (phones, laptops)">LA</Chip>
                          </span>
                        )}
                        {!scope.iface && <span className="ml-2 text-muted">{h.interface}</span>}
                      </td>
                      <td className="hidden max-w-[240px] truncate px-3 py-2 text-[12px] text-muted lg:table-cell">{h.oui_registrant ?? '—'}</td>
                      <td className="hidden px-3 py-2 font-mono text-[12px] text-muted xl:table-cell">{ago(h.first_seen)}</td>
                      <td className="px-3 py-2 font-mono text-[12px] whitespace-nowrap">{ago(h.last_seen)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
            {rows.length === 0 && <p className="py-8 text-center text-[13px] text-muted">No device matches.</p>}
          </div>
        )}
      </Card>
    </div>
  );
}
