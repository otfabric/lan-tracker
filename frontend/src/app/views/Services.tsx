import { useMemo } from 'react';
import type { Endpoint } from '../../api/types';
import { BarList, Legend } from '../charts/charts';
import { useApp } from '../context';
import { ago, cli, dateTime } from '../format';
import { Card, Command, Empty, ErrorNote, Skeleton, useLoad } from '../ui';

const RESULTS: { key: Endpoint['last_result']; label: string; color: string; hint: string }[] = [
  { key: 'OPEN', label: 'open', color: 'var(--st-good)', hint: 'The connect completed' },
  { key: 'REFUSED', label: 'refused', color: 'var(--st-info)', hint: 'The host answered with a reset: it is there, the port is closed' },
  { key: 'TIMEOUT', label: 'timeout', color: 'var(--st-warn)', hint: 'No answer: never proof that the host is offline' },
  { key: 'UNREACHABLE', label: 'unreachable', color: 'var(--st-bad)', hint: 'ICMP unreachable or no route' },
  { key: 'LOCAL_ERROR', label: 'local error', color: 'var(--s-other)', hint: 'The probe could not be sent' },
];

function ipKey(ip: string) {
  return ip.includes('.') ? ip.split('.').map((p) => p.padStart(3, '0')).join('.') : ip;
}

/** The TCP endpoints as a matrix: one row per address, one column per port. */
export function Services() {
  const { api, scope, tick, navigate } = useApp();
  const eps = useLoad(() => api.services(scope.iface), [api, scope.iface, tick]);
  const hosts = useLoad(() => api.hosts(scope.iface), [api, scope.iface, tick]);

  const { ports, rows, counts, byPort } = useMemo(() => {
    const ports = [...new Set((eps.data ?? []).map((e) => e.port))].sort((a, b) => a - b);
    const m = new Map<string, Map<number, Endpoint>>();
    const counts: Record<string, number> = {};
    const byPort = new Map<number, number>();
    for (const e of eps.data ?? []) {
      const k = `${e.interface}|${e.ip}`;
      if (!m.has(k)) m.set(k, new Map());
      m.get(k)!.set(e.port, e);
      counts[e.last_result] = (counts[e.last_result] ?? 0) + 1;
      if (e.last_result === 'OPEN') byPort.set(e.port, (byPort.get(e.port) ?? 0) + 1);
    }
    const rows = [...m.entries()].sort((a, b) => ipKey(a[0].split('|')[1]!).localeCompare(ipKey(b[0].split('|')[1]!)));
    return { ports, rows, counts, byPort };
  }, [eps.data]);
  const names = useMemo(() => new Map((hosts.data ?? []).map((h) => [h.host_id, h.preferred_name || h.description || ''])), [hosts.data]);

  return (
    <div className="mx-auto flex max-w-[1600px] flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="font-display text-2xl font-semibold text-fg-strong">TCP endpoints</h1>
        <span className="text-[12px] text-muted">A plain connect with no payload: OPEN says a connection completed, never which protocol or device answers.</span>
        <span className="ml-auto">
          <Command cmd={cli('services list')} />
        </span>
      </div>
      {eps.error !== undefined && <ErrorNote error={eps.error} />}
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[3fr_1fr]">
        <Card title="Reachability matrix" sub="Last result per address and port; hover a cell for when it last answered.">
          <Legend items={RESULTS.map((r) => ({ key: r.key, label: r.label, color: r.color, value: String(counts[r.key] ?? 0) }))} />
          {!eps.data ? (
            <Skeleton height={300} />
          ) : rows.length === 0 ? (
            <Empty>No TCP checks yet. They run when active.tcp is enabled on an interface, or with `lan-tracker scan run --tcp`.</Empty>
          ) : (
            <div className="mt-3 overflow-x-auto">
              <table className="border-separate border-spacing-[3px] text-[12px]">
                <thead>
                  <tr>
                    <th />
                    {ports.map((p) => (
                      <th key={p} className="px-1 font-mono font-normal text-muted">
                        {p}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {rows.map(([k, cells]) => {
                    const [iface, ip] = k.split('|') as [string, string];
                    const any = [...cells.values()][0]!;
                    return (
                      <tr key={k}>
                        <th className="pr-3 text-left font-normal whitespace-nowrap">
                          <button type="button" disabled={!any.host_id} className="cursor-pointer text-left disabled:cursor-default" onClick={() => any.host_id && navigate(`/hosts/${encodeURIComponent(any.host_id)}`)}>
                            <span className="font-mono text-fg">{ip}</span>
                            <span className="ml-2 text-muted">{(any.host_id && names.get(any.host_id)) || (scope.iface ? '' : iface)}</span>
                          </button>
                        </th>
                        {ports.map((p) => {
                          const e = cells.get(p);
                          const r = e && RESULTS.find((x) => x.key === e.last_result)!;
                          return (
                            <td
                              key={p}
                              className="h-6 w-12 min-w-12 rounded text-center font-mono text-[10px]"
                              style={{ background: r ? `color-mix(in srgb, ${r.color} 30%, transparent)` : 'transparent', border: r ? `1px solid ${r.color}` : '1px dashed var(--grid)' }}
                              title={
                                e
                                  ? `${ip}:${p} ${e.last_result} ${ago(e.last_result_at)}${e.last_open_at ? `\nlast open ${dateTime(e.last_open_at)}` : ''}${e.last_detail ? `\n${e.last_detail}` : ''}\n${r!.hint}`
                                  : 'not checked'
                              }
                            >
                              {r ? r.label.slice(0, 4) : ''}
                            </td>
                          );
                        })}
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </Card>
        <Card title="Open ports" sub="Endpoints whose last result was OPEN">
          <BarList color="var(--st-good)" items={[...byPort.entries()].sort((a, b) => b[1] - a[1]).map(([p, c]) => ({ key: String(p), label: <span className="font-mono">tcp/{p}</span>, value: c }))} empty="None open." />
        </Card>
      </div>
    </div>
  );
}
