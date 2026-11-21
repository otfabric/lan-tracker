import { useMemo } from 'react';
import { BarList, Donut, Legend, TimeChart } from '../charts/charts';
import { useApp } from '../context';
import { ago, cli, dateTime, duration } from '../format';
import { Card, Chip, Command, Empty, ErrorNote, Skeleton, WarnIcon, useLoad } from '../ui';

const OUTCOMES: { key: string; label: string; color: string }[] = [
  { key: 'ack', label: 'acknowledged', color: 'var(--s2)' },
  { key: 'inform_ack', label: 'inform answered', color: 'var(--s3)' },
  { key: 'release', label: 'released', color: 'var(--s5)' },
  { key: 'offered', label: 'offered only', color: 'var(--st-warn)' },
  { key: 'incomplete', label: 'unanswered', color: 'var(--s-other)' },
  { key: 'nak', label: 'refused (NAK)', color: 'var(--st-bad)' },
  { key: 'decline', label: 'declined', color: 'var(--s7)' },
];

const statusTone = { allowed: 'good', unexpected: 'bad', unchecked: 'muted' } as const;

export function Dhcp() {
  const { api, scope, tick, navigate } = useApp();
  const servers = useLoad(() => api.dhcpServers(scope.iface), [api, scope.iface, tick]);
  const leases = useLoad(() => api.dhcpLeases(scope.iface), [api, scope.iface, tick]);
  const xs = useLoad(() => api.dhcpExchanges(scope.iface, scope.range.since), [api, scope.iface, scope.range.since.getTime(), tick]);

  const since = scope.range.since.getTime();
  const until = scope.range.until.getTime();
  const n = 60;
  const step = Math.ceil((until - since) / n);
  const series = useMemo(() => {
    const per: Record<string, number[]> = {};
    for (const x of xs.data ?? []) {
      const i = Math.floor((Date.parse(x.first_seen) - since) / step);
      if (i < 0 || i >= n) continue;
      (per[x.outcome] ??= new Array<number>(n).fill(0))[i]! += 1;
    }
    return OUTCOMES.map((o) => ({ key: o.key, label: o.label, color: o.color, values: per[o.key] ?? new Array<number>(n).fill(0) }));
  }, [xs.data, since, step]);
  const totals = series.map((s) => ({ ...s, value: s.values.reduce((a, b) => a + b, 0) }));
  const all = totals.reduce((a, b) => a + b.value, 0);
  const ok = (totals.find((t) => t.key === 'ack')?.value ?? 0) + (totals.find((t) => t.key === 'inform_ack')?.value ?? 0);

  const busiest = useMemo(() => {
    const m = new Map<string, { n: number; host?: string }>();
    for (const x of xs.data ?? []) {
      const e = m.get(x.chaddr) ?? { n: 0, host: x.host_id };
      e.n++;
      m.set(x.chaddr, e);
    }
    return [...m.entries()].sort((a, b) => b[1].n - a[1].n).slice(0, 8);
  }, [xs.data]);

  return (
    <div className="mx-auto flex max-w-[1600px] flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="font-display text-2xl font-semibold text-fg-strong">DHCP</h1>
        <span className="text-[12px] text-muted">Receive only: on a switched port unicast replies are not seen, so silence never proves there is no server or lease.</span>
        <span className="ml-auto">
          <Command cmd={cli('dhcp servers')} />
        </span>
      </div>
      {servers.error !== undefined && <ErrorNote error={servers.error} />}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-3">
        {(servers.data ?? []).map((s) => (
          <Card key={`${s.interface}-${s.server_id}-${s.mac}`} className={s.status === 'unexpected' ? 'border-bad/60' : ''}>
            <div className="flex items-start gap-3">
              <div className="min-w-0 flex-1">
                <div className="ot-label">DHCP server · {s.interface}</div>
                <div className="mt-1 font-mono text-[1.25rem] font-medium text-fg-strong">{s.server_id ?? 'identity unknown'}</div>
                <div className="font-mono text-[12px] text-muted">
                  {s.mac}
                  {s.relay && ` · via relay ${s.relay}`}
                </div>
              </div>
              <Chip tone={statusTone[s.status]} title={s.status === 'unchecked' ? 'No allowlist on this interface: not checked, never authorised' : undefined}>
                {s.status === 'unexpected' && <WarnIcon size={12} />}
                {s.status}
              </Chip>
            </div>
            <dl className="mt-3 grid grid-cols-[auto_1fr] gap-x-4 gap-y-1 text-[12px]">
              {Object.entries(s.config).map(([k, v]) => (
                <div key={k} className="contents">
                  <dt className="text-muted">{k.replace('_', ' ')}</dt>
                  <dd className="font-mono">{v}</dd>
                </div>
              ))}
              <dt className="text-muted">first seen</dt>
              <dd className="font-mono">{dateTime(s.first_seen)}</dd>
              <dt className="text-muted">last reply</dt>
              <dd className="font-mono">{ago(s.last_seen)}</dd>
            </dl>
          </Card>
        ))}
        {servers.data && servers.data.length === 0 && (
          <Card>
            <Empty>No DHCP server heard from yet.</Empty>
          </Card>
        )}
      </div>
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[2fr_1fr]">
        <Card title="Exchanges over time" sub="Each DHCP transaction by how it ended">
          {xs.data ? <TimeChart mode="bars" series={series} since={since} stepMs={step} height={210} yLabel="Exchanges" /> : <Skeleton height={250} />}
        </Card>
        <Card title="Outcomes">
          <div className="flex flex-col items-center gap-3">
            <Donut size={160} center={all ? `${Math.round((ok / all) * 100)}%` : '—'} sub="acknowledged" slices={totals.map((t) => ({ key: t.key, label: t.label, value: t.value, color: t.color }))} />
            <Legend items={totals.filter((t) => t.value > 0).map((t) => ({ key: t.key, label: t.label, color: t.color, value: String(t.value) }))} />
          </div>
        </Card>
      </div>
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[1fr_2fr]">
        <Card title="Busiest clients" sub="Exchanges per client MAC in the range">
          <BarList
            color="var(--s2)"
            items={busiest.map(([mac, v]) => ({ key: mac, label: <span className="font-mono">{mac}</span>, value: v.n, onClick: v.host ? () => navigate(`/hosts/${encodeURIComponent(v.host!)}`) : undefined }))}
          />
        </Card>
        <Card title="Leases" sub="What servers acknowledged, newest first">
          {!leases.data ? (
            <Skeleton height={200} />
          ) : leases.data.length === 0 ? (
            <Empty>No leases seen.</Empty>
          ) : (
            <div className="max-h-[360px] overflow-auto">
              <table className="w-full text-[13px]">
                <thead className="sticky top-0 border-b border-line bg-surface">
                  <tr className="text-left">
                    <th className="ot-label py-2 pr-3">Client</th>
                    <th className="ot-label py-2 pr-3">Address</th>
                    <th className="ot-label py-2 pr-3">Server</th>
                    <th className="ot-label py-2 pr-3">Last ACK</th>
                    <th className="ot-label py-2 pr-3">Lease</th>
                    <th className="ot-label py-2">State</th>
                  </tr>
                </thead>
                <tbody>
                  {leases.data.slice(0, 200).map((l) => (
                    <tr key={l.id} className={`border-b border-line/50 ${l.host_id ? 'cursor-pointer hover:bg-accent-soft/30' : ''}`} onClick={l.host_id ? () => navigate(`/hosts/${encodeURIComponent(l.host_id!)}`) : undefined}>
                      <td className="py-1.5 pr-3 font-mono text-[12px]">{l.chaddr}</td>
                      <td className="py-1.5 pr-3 font-mono">{l.ip}</td>
                      <td className="py-1.5 pr-3 font-mono text-[12px]">{l.server_id ?? '?'}</td>
                      <td className="py-1.5 pr-3 font-mono text-[12px] text-muted">{ago(l.last_ack)}</td>
                      <td className="py-1.5 pr-3 font-mono text-[12px]">{l.lease_seconds === undefined ? '?' : l.lease_seconds < 0 ? 'infinite' : duration(l.lease_seconds * 1000)}</td>
                      <td className="py-1.5">
                        {l.ended_at ? <Chip>{l.end_reason ?? 'ended'}</Chip> : <Chip tone="good">in effect</Chip>}
                        {l.partial && (
                          <span className="ml-1">
                            <Chip title="The client's REQUEST was not seen">partial</Chip>
                          </span>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Card>
      </div>
    </div>
  );
}
