import { useState } from 'react';
import type { Host } from '../../api/types';
import { Gantt, TimeChart } from '../charts/charts';
import { useApp } from '../context';
import { SOURCE_GROUPS, ago, cli, dateTime, duration, eventLabel, group, presenceOf } from '../format';
import { Card, Chip, Command, Dot, Empty, ErrorNote, Skeleton, useLoad } from '../ui';
import { EventList } from './EventList';

const resultTone = { OPEN: 'good', REFUSED: 'blue', TIMEOUT: 'warn', UNREACHABLE: 'bad', LOCAL_ERROR: 'muted' } as const;

export function HostDetail({ id }: { id: string }) {
  const { api, scope, tick, navigate } = useApp();
  const [saved, setSaved] = useState(0);
  const host = useLoad(() => api.host(id), [api, id, tick, saved]);
  const act = useLoad(() => api.activity(scope, 96, id), [api, id, scope.range.since.getTime(), tick]);
  const history = useLoad(() => api.hostEvents(id, new Date(Date.now() - 90 * 86400_000)), [api, id, tick, saved]);

  if (host.error !== undefined && !host.data) {
    return (
      <div className="mx-auto max-w-3xl">
        <ErrorNote error={host.error} />
      </div>
    );
  }
  const h = host.data;
  if (!h) return <Skeleton height={500} />;
  const p = presenceOf(h.presence);
  const a = act.data;

  return (
    <div className="mx-auto flex max-w-[1600px] flex-col gap-4">
      <button type="button" className="w-fit cursor-pointer text-[12px] text-accent hover:underline" onClick={() => navigate('/hosts')}>
        ← Devices
      </button>
      <div className="flex flex-wrap items-start gap-4">
        <div className="min-w-0 flex-1">
          <h1 className="flex flex-wrap items-center gap-3 font-display text-[1.9rem] leading-tight font-semibold text-fg-strong">
            {h.preferred_name || h.mac}
            <span className="text-[13px] font-normal" title={p.hint}>
              <Dot color={p.color} pulse={h.presence === 'ACTIVE'} label={p.label} />
            </span>
          </h1>
          <div className="mt-1 flex flex-wrap items-center gap-x-4 gap-y-1 font-mono text-[12px] text-muted">
            <span className="text-fg">{h.mac}</span>
            {h.locally_administered && <Chip title="Locally administered: often a randomised MAC">locally administered</Chip>}
            {h.proxy_arp && <Chip tone="warn">proxy ARP</Chip>}
            <span title="IEEE registrant of the MAC prefix: a hint, not the device's maker">{h.oui_registrant ?? 'unknown registrant'}</span>
            <span>{h.interface}</span>
            <span>{h.ips.join(', ') || 'no open address'}</span>
          </div>
          <Description host={h} onSaved={() => setSaved((n) => n + 1)} />
        </div>
        <Command cmd={cli(`hosts show ${h.mac}${scope.iface ? '' : ` --interface ${h.interface}`}`)} />
      </div>

      <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <Evidence label="Last frame from the MAC" at={h.last_frame_seen} />
        <Evidence label="Last address confirmed" at={h.last_address_confirmed} />
        <Evidence label="Last endpoint answer" at={h.last_endpoint_answer} />
        <Evidence label="First seen" at={h.first_seen} />
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[3fr_2fr]">
        <Card title="Activity" sub="Observations of this device in the selected range; dashed lines are its events.">
          {a ? (
            <TimeChart
              series={group(a.observations, SOURCE_GROUPS, a.buckets).map((g) => ({ key: g.cat.key, label: g.cat.label, color: g.cat.color, values: g.values }))}
              since={Date.parse(a.since)}
              stepMs={a.step_seconds * 1000}
              height={200}
              yLabel="Observations"
              markers={(history.data ?? [])
                .filter((e) => Date.parse(e.ts) >= Date.parse(a.since))
                .map((e) => ({ at: Date.parse(e.ts), color: e.severity === 'warning' ? 'var(--st-warn)' : 'var(--ot-muted)', title: `${eventLabel(e.type)} ${e.new ?? ''}` }))}
            />
          ) : (
            <Skeleton height={240} />
          )}
        </Card>
        <Card title="Address history" sub="Each address the MAC held; open bindings end in a dot.">
          <Addresses host={h} />
        </Card>
      </div>

      <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Card title="Names" sub="Advertised or resolved; expired and stale names stay in effect.">
          {h.names.length === 0 ? (
            <Empty>No names seen.</Empty>
          ) : (
            <table className="w-full text-[13px]">
              <tbody>
                {h.names.map((n, i) => (
                  <tr key={i} className="border-b border-line/50 last:border-0">
                    <td className="py-1.5 pr-3 font-mono">{n.name}</td>
                    <td className="py-1.5 pr-3">
                      <Chip>{n.type}</Chip>
                    </td>
                    <td className="py-1.5 pr-3 text-[12px] text-muted">{n.link === 'sender' ? 'announced by the device' : n.link === 'address' ? 'for its address' : 'not linked'}</td>
                    <td className="py-1.5 text-right">
                      <Chip tone={n.freshness === 'fresh' ? 'good' : n.freshness === 'ended' ? 'muted' : 'warn'}>{n.freshness}</Chip>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Card>
        <Card title="TCP endpoints" sub="A plain connect: OPEN means a connection completed, not which protocol answers.">
          {h.endpoints.length === 0 ? (
            <Empty>No TCP endpoints checked for this device.</Empty>
          ) : (
            <div className="flex flex-wrap gap-2">
              {h.endpoints.map((e) => (
                <div key={`${e.ip}:${e.port}`} className="min-w-[150px] rounded border border-line bg-bg p-2.5" title={e.last_detail}>
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-mono text-[15px] font-medium text-fg-strong">{e.port}</span>
                    <Chip tone={resultTone[e.last_result]}>{e.last_result}</Chip>
                  </div>
                  <div className="mt-1 font-mono text-[11px] text-muted">{e.ip}</div>
                  <div className="mt-0.5 text-[11px] text-muted">checked {ago(e.last_result_at)}</div>
                  {e.last_result !== 'OPEN' && e.last_open_at && <div className="text-[11px] text-muted">last open {ago(e.last_open_at)}</div>}
                </div>
              ))}
            </div>
          )}
        </Card>
      </div>

      {h.dhcp_leases.length > 0 && (
        <Card title="DHCP leases" sub="What servers acknowledged for this MAC: the server's statement, not proof the device holds the address.">
          <div className="overflow-x-auto">
            <table className="w-full text-[13px]">
              <thead className="border-b border-line">
                <tr className="text-left">
                  <th className="ot-label py-2 pr-3">Address</th>
                  <th className="ot-label py-2 pr-3">Server</th>
                  <th className="ot-label py-2 pr-3">First ACK</th>
                  <th className="ot-label py-2 pr-3">ACKs</th>
                  <th className="ot-label py-2 pr-3">Lease</th>
                  <th className="ot-label py-2">State</th>
                </tr>
              </thead>
              <tbody>
                {h.dhcp_leases.slice(0, 20).map((l) => (
                  <tr key={l.id} className="border-b border-line/50 last:border-0">
                    <td className="py-1.5 pr-3 font-mono">{l.ip}</td>
                    <td className="py-1.5 pr-3 font-mono text-[12px]">{l.server_id ?? '?'}</td>
                    <td className="py-1.5 pr-3 font-mono text-[12px] text-muted">{dateTime(l.first_ack)}</td>
                    <td className="py-1.5 pr-3 font-mono text-[12px]">{l.acks}</td>
                    <td className="py-1.5 pr-3 font-mono text-[12px]">{l.lease_seconds === undefined ? '?' : l.lease_seconds < 0 ? 'infinite' : duration(l.lease_seconds * 1000)}</td>
                    <td className="py-1.5">{l.ended_at ? <Chip>{l.end_reason ?? 'ended'}</Chip> : <Chip tone="good">in effect</Chip>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}

      <Card title="Timeline" sub="Every event of this device in the last 90 days, newest first.">
        {history.data ? <EventList events={[...history.data].reverse()} /> : <Skeleton height={200} />}
      </Card>
    </div>
  );
}

function Evidence({ label, at }: { label: string; at?: string }) {
  return (
    <div className="rounded-lg border border-line bg-surface p-3" title={at ? dateTime(at) : 'Not seen yet'}>
      <div className="ot-label">{label}</div>
      <div className="mt-1.5 font-display text-[1.35rem] font-semibold text-fg-strong">{ago(at)}</div>
      <div className="font-mono text-[11px] text-muted">{at ? dateTime(at) : 'never'}</div>
    </div>
  );
}

function Addresses({ host }: { host: Host }) {
  if (host.addresses.length === 0) return <Empty>No address bindings.</Empty>;
  const from = Math.min(...host.addresses.map((b) => Date.parse(b.first_seen)));
  const to = Date.now();
  return (
    <>
      <Gantt
        from={from}
        to={to}
        spans={host.addresses.map((b, i) => {
          const end = b.ended_at ? Date.parse(b.ended_at) : Date.parse(b.last_seen);
          return {
            key: `${b.ip}-${i}`,
            label: b.ip,
            start: Date.parse(b.first_seen),
            end: b.ended_at ? end : Math.max(end, to),
            open: !b.ended_at,
            color: b.conflict ? 'var(--st-bad)' : 'var(--s3)',
            hint: `${b.ip}: ${dateTime(b.first_seen)} → ${b.ended_at ? dateTime(b.ended_at) : 'open'}${b.conflict ? ' (conflict)' : ''}\nlast seen ${dateTime(b.last_seen)}${
              b.sources?.length ? `\nsources: ${b.sources.map((s) => s.source).join(', ')}` : ''
            }`,
          };
        })}
      />
      {host.addresses.some((b) => b.conflict) && (
        <p className="mt-2 text-[12px] text-bad">A red binding was held by another MAC at the same time (duplicate IP).</p>
      )}
    </>
  );
}

function Description({ host, onSaved }: { host: Host; onSaved: () => void }) {
  const { api } = useApp();
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState(host.description ?? '');
  const [err, setErr] = useState<unknown>();
  const [busy, setBusy] = useState(false);
  if (!editing) {
    return (
      <p className="mt-2 flex items-center gap-2 text-[14px]">
        {host.description ? <span className="text-fg">{host.description}</span> : <span className="text-muted italic">No description.</span>}
        <button type="button" className="cursor-pointer text-[12px] text-accent hover:underline" onClick={() => setEditing(true)}>
          {host.description ? 'Edit' : 'Describe this device'}
        </button>
      </p>
    );
  }
  const save = async (value: string) => {
    setBusy(true);
    setErr(undefined);
    try {
      await api.setDescription(host.host_id, value);
      setEditing(false);
      onSaved();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  };
  return (
    <form
      className="mt-2 flex max-w-2xl flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        void save(text.trim());
      }}
    >
      <div className="flex gap-2">
        <input
          autoFocus
          value={text}
          maxLength={200}
          onChange={(e) => setText(e.target.value)}
          aria-label="Description"
          placeholder="e.g. Inverter, north roof"
          className="flex-1 rounded border border-control bg-bg px-2.5 py-1.5 text-[13px]"
        />
        <button type="submit" disabled={busy} className="cursor-pointer rounded bg-accent px-3 py-1.5 text-[13px] font-medium text-bg disabled:opacity-50">
          Save
        </button>
        {host.description && (
          <button type="button" disabled={busy} onClick={() => void save('')} className="cursor-pointer rounded border border-line px-3 py-1.5 text-[13px] text-muted hover:text-fg">
            Remove
          </button>
        )}
        <button type="button" onClick={() => setEditing(false)} className="cursor-pointer px-2 text-[13px] text-muted hover:text-fg">
          Cancel
        </button>
      </div>
      <p className="text-[11px] text-muted">One line, at most 200 characters. The change is recorded as a HOST_DESCRIBED event by "ui".</p>
      {err !== undefined && <ErrorNote error={err} />}
    </form>
  );
}
