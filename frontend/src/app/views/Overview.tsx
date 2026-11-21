import { useMemo } from 'react';
import type { Status } from '../../api/types';
import { BarList, Donut, Legend, TimeChart } from '../charts/charts';
import { useApp } from '../context';
import { EVENT_CATEGORIES, PRESENCE, SOURCE_GROUPS, compact, eventCategory, eventLabel, group, hostLabel, num } from '../format';
import { Card, ErrorNote, Skeleton, Stat, WarnIcon, useLoad } from '../ui';
import { EventList } from './EventList';

function sumTypes(events: Record<string, number[]>, types: string[], n: number): number[] {
  const out = new Array<number>(n).fill(0);
  for (const t of types) events[t]?.forEach((v, i) => (out[i]! += v));
  return out;
}

const total = (v: number[]) => v.reduce((a, b) => a + b, 0);

export function Overview({ status }: { status?: Status }) {
  const { api, scope, tick, navigate, live } = useApp();
  const act = useLoad(() => api.activity(scope, 96), [api, scope.iface, scope.range.since.getTime(), tick]);
  const hosts = useLoad(() => api.hosts(scope.iface), [api, scope.iface, tick]);
  const events = useLoad(() => api.events(scope, 40), [api, scope.iface, scope.range.since.getTime(), tick]);
  const exchanges = useLoad(() => api.dhcpExchanges(scope.iface, scope.range.since), [api, scope.iface, scope.range.since.getTime(), tick]);

  const a = act.data;
  const n = a?.buckets ?? 0;
  const ev = a?.events ?? {};
  const kpi = useMemo(() => {
    const k = (types: string[]) => sumTypes(ev, types, n);
    return {
      discovered: k(['HOST_DISCOVERED']),
      moves: k(['IP_ADDED', 'IP_REMOVED']),
      conflicts: k(['DUPLICATE_IP_DETECTED', 'PROXY_ARP_DETECTED']),
      dhcpTrouble: k(['DHCP_LEASE_NAK', 'DHCP_LEASE_DECLINED', 'DHCP_SERVER_UNEXPECTED', 'DHCP_SERVER_MAC_CHANGED']),
      ports: k(['SERVICE_STATE_CHANGED']),
      comeGo: k(['HOST_DISAPPEARED', 'HOST_REAPPEARED']),
      observations: (() => {
        const out = new Array<number>(n).fill(0);
        for (const v of Object.values(a?.observations ?? {})) v.forEach((c, i) => (out[i]! += c));
        return out;
      })(),
    };
  }, [ev, n, a]);

  const presence = useMemo(() => {
    const c: Record<string, number> = { ACTIVE: 0, RECENT: 0, STALE: 0, MISSING: 0 };
    for (const h of hosts.data ?? []) c[h.presence] = (c[h.presence] ?? 0) + 1;
    return c;
  }, [hosts.data]);
  const liveCount = (presence.ACTIVE ?? 0) + (presence.RECENT ?? 0);
  const allHosts = hosts.data?.length ?? 0;

  const vendors = useMemo(() => {
    const m = new Map<string, number>();
    for (const h of hosts.data ?? []) {
      const v = h.oui_registrant || (h.locally_administered ? 'Locally administered (random) MAC' : 'Unknown registrant');
      m.set(v, (m.get(v) ?? 0) + 1);
    }
    return [...m.entries()].sort((x, y) => y[1] - x[1]).slice(0, 8);
  }, [hosts.data]);

  const outcomes = useMemo(() => {
    const m = new Map<string, number>();
    for (const x of exchanges.data ?? []) m.set(x.outcome, (m.get(x.outcome) ?? 0) + 1);
    return [...m.entries()].sort((x, y) => y[1] - x[1]);
  }, [exchanges.data]);

  const typeTotals = useMemo(
    () =>
      Object.entries(ev)
        .map(([t, v]) => [t, total(v)] as const)
        .filter(([, v]) => v > 0)
        .sort((x, y) => y[1] - x[1])
        .slice(0, 10),
    [ev],
  );

  const stepMs = (a?.step_seconds ?? 0) * 1000;
  const since = a ? Date.parse(a.since) : 0;
  const warnings = (events.data ?? []).filter((e) => e.severity === 'warning');
  const feed = useMemo(() => {
    const seen = new Set<string>();
    return [...live, ...[...(events.data ?? [])].reverse()].filter((e) => {
      const k = `${e.id}-${e.ts}-${e.type}`;
      if (seen.has(k)) return false;
      seen.add(k);
      return true;
    });
  }, [live, events.data]);
  const freshKeys = useMemo(() => new Set(live.slice(0, 3).map((e) => `${e.id}-${e.ts}`)), [live]);

  return (
    <div className="mx-auto flex max-w-[1600px] flex-col gap-4">
      {act.error !== undefined && <ErrorNote error={act.error} />}
      <div className="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
        <Stat label="Devices live" value={hosts.data ? liveCount : '…'} unit={hosts.data ? `of ${allHosts}` : undefined} tone="good" hint="Active or recent: seen in the last 30 minutes" onClick={() => navigate('/hosts')} />
        <Stat label="New devices" value={a ? total(kpi.discovered) : '…'} trend={kpi.discovered} tone="accent" hint="HOST_DISCOVERED: a MAC seen for the first time on an interface" onClick={() => navigate('/events?type=HOST_DISCOVERED')} />
        <Stat label="Address changes" value={a ? total(kpi.moves) : '…'} trend={kpi.moves} tone="blue" hint="Addresses gained or ended (IP_ADDED, IP_REMOVED)" onClick={() => navigate('/events?cat=addresses')} />
        <Stat
          label="IP conflicts"
          icon={a && total(kpi.conflicts) > 0 ? <WarnIcon /> : undefined}
          value={a ? total(kpi.conflicts) : '…'}
          trend={kpi.conflicts}
          tone={a && total(kpi.conflicts) > 0 ? 'bad' : 'muted'}
          hint="Duplicate IPs and proxy ARP detected"
          onClick={() => navigate('/events?cat=conflicts')}
        />
        <Stat
          label="DHCP trouble"
          icon={a && total(kpi.dhcpTrouble) > 0 ? <WarnIcon /> : undefined}
          value={a ? total(kpi.dhcpTrouble) : '…'}
          trend={kpi.dhcpTrouble}
          tone={a && total(kpi.dhcpTrouble) > 0 ? 'warn' : 'muted'}
          hint="NAKs, declined leases, unexpected servers, server MAC changes"
          onClick={() => navigate('/dhcp')}
        />
        <Stat label="Observations" value={a ? compact(total(kpi.observations)) : '…'} trend={kpi.observations} tone="accent" hint="Evidence the tracker recorded: frames, neighbour entries, probe answers" />
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[2fr_1fr]">
        <Card title="Network activity" sub="What the tracker saw, by how it learned it. Click a bucket to see its events.">
          {a ? (
            <TimeChart
              series={group(a.observations, SOURCE_GROUPS, n).map((g) => ({ key: g.cat.key, label: g.cat.label, color: g.cat.color, values: g.values }))}
              since={since}
              stepMs={stepMs}
              height={260}
              yLabel="Observations"
              onBucket={(s, e) => navigate(`/events?since=${s}&until=${e}`)}
              markers={warnings.map((w) => ({ at: Date.parse(w.ts), color: 'var(--st-warn)', title: `${eventLabel(w.type)} — ${w.cause}` }))}
            />
          ) : (
            <Skeleton height={300} />
          )}
        </Card>
        <Card title="Presence" sub="Silence is never proof that a device is offline.">
          <div className="flex flex-1 flex-col items-center justify-center gap-4">
            <Donut
              size={180}
              center={String(allHosts)}
              sub="devices"
              slices={PRESENCE.map((p) => ({ key: p.key, label: p.label, value: presence[p.key] ?? 0, color: p.color, hint: p.hint }))}
            />
            <Legend items={PRESENCE.map((p) => ({ key: p.key, label: p.label, color: p.color, value: String(presence[p.key] ?? 0) }))} />
            {status && status.interfaces && status.interfaces.length > 1 && (
              <div className="w-full border-t border-line pt-3">
                {status.interfaces.map((i) => {
                  const h = status.hosts[i.name] ?? {};
                  const all = PRESENCE.reduce((s, p) => s + (h[p.key] ?? 0), 0) || 1;
                  return (
                    <div key={i.name} className="mb-2 flex items-center gap-3 text-[12px]">
                      <span className="w-12 font-mono">{i.name}</span>
                      <div className="flex h-2.5 flex-1 gap-[2px] overflow-hidden rounded">
                        {PRESENCE.map((p) => (h[p.key] ?? 0) > 0 && <span key={p.key} title={`${p.label}: ${h[p.key]}`} style={{ width: `${((h[p.key] ?? 0) / all) * 100}%`, background: p.color }} />)}
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </Card>
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[2fr_1fr]">
        <Card title="What changed" sub="Events by category over the selected range.">
          {a ? (
            <TimeChart
              mode="bars"
              series={group(a.events, EVENT_CATEGORIES, n).map((g) => ({ key: g.cat.key, label: g.cat.label, color: g.cat.color, values: g.values }))}
              since={since}
              stepMs={stepMs}
              height={200}
              yLabel="Events"
              onBucket={(s, e) => navigate(`/events?since=${s}&until=${e}`)}
            />
          ) : (
            <Skeleton height={240} />
          )}
        </Card>
        <Card title="Events by type">
          <BarList
            items={typeTotals.map(([t, v]) => ({ key: t, label: eventLabel(t), value: v, color: eventCategory(t).color, onClick: () => navigate(`/events?type=${t}`) }))}
          />
        </Card>
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Card title="Most talkative devices" sub="By observations in the range">
          <BarList
            color="var(--s1)"
            format={compact}
            items={(a?.top_hosts ?? []).map((h) => ({
              key: h.host_id,
              label: (
                <span className="flex items-baseline gap-2">
                  <span className="truncate">{hostLabel(h)}</span>
                  <span className="font-mono text-[11px] text-muted">{h.interface}</span>
                </span>
              ),
              value: h.observations,
              hint: h.mac,
              onClick: () => navigate(`/hosts/${encodeURIComponent(h.host_id)}`),
            }))}
          />
        </Card>
        <Card title="MAC registrants" sub="IEEE OUI of each MAC: a hint about the network interface, not the device's maker">
          <BarList color="var(--s3)" items={vendors.map(([v, c]) => ({ key: v, label: v, value: c, onClick: () => navigate(`/hosts?vendor=${encodeURIComponent(v)}`) }))} empty="No devices." />
        </Card>
        <Card title="DHCP exchanges" sub="Outcome of each transaction seen in the range">
          <BarList
            items={outcomes.map(([o, c]) => ({
              key: o,
              label: o,
              value: c,
              color: o === 'nak' || o === 'decline' ? 'var(--st-bad)' : o === 'incomplete' || o === 'offered' ? 'var(--st-warn)' : 'var(--s2)',
              onClick: () => navigate('/dhcp'),
            }))}
            empty="No DHCP exchanges seen in this range."
          />
        </Card>
      </div>

      <Card title="Recent events" sub={`Latest first; new events arrive live. ${num(feed.length)} shown.`} actions={<button type="button" className="cursor-pointer text-[12px] text-accent hover:underline" onClick={() => navigate('/events')}>All events →</button>}>
        <EventList events={feed.slice(0, 25)} fresh={freshKeys} />
      </Card>
    </div>
  );
}
