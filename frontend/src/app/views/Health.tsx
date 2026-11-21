import type { Status } from '../../api/types';
import { useApp } from '../context';
import { ago, bytes, cli, dateTime, duration } from '../format';
import { Card, Chip, Command, Dot, Empty, Skeleton, WarnIcon } from '../ui';

const stateColor = (s: string) => (s === 'running' ? 'var(--st-good)' : s === 'disabled' ? 'var(--s-other)' : s === 'starting' ? 'var(--st-info)' : 'var(--st-bad)');

/** What the tracker itself can and cannot see: the daemon, its database and every collector. */
export function Health({ status }: { status?: Status }) {
  const { scope } = useApp();
  if (!status) return <Skeleton height={500} />;
  const db = status.database;
  const ifs = (status.interfaces ?? []).filter((i) => !scope.iface || i.name === scope.iface);
  const uptime = Date.parse(status.now) - Date.parse(status.started);
  return (
    <div className="mx-auto flex max-w-[1600px] flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="font-display text-2xl font-semibold text-fg-strong">Tracker health</h1>
        <span className="ml-auto">
          <Command cmd={cli('daemon status')} />
        </span>
      </div>
      {status.problems && status.problems.length > 0 && (
        <div className="flex items-start gap-2 rounded-lg border border-warn/60 bg-surface p-3 text-[13px] text-warn">
          <WarnIcon size={16} />
          <ul>
            {status.problems.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        </div>
      )}
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2 xl:grid-cols-4">
        <Card title="Daemon">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[13px]">
            <dt className="text-muted">state</dt>
            <dd>
              <Dot color={status.state === 'ok' ? 'var(--st-good)' : status.state === 'degraded' ? 'var(--st-warn)' : 'var(--st-bad)'} label={status.state} />
            </dd>
            <dt className="text-muted">version</dt>
            <dd className="font-mono">{status.version}</dd>
            <dt className="text-muted">platform</dt>
            <dd className="font-mono">{status.platform}</dd>
            <dt className="text-muted">up for</dt>
            <dd className="font-mono" title={dateTime(status.started)}>
              {duration(uptime)}
            </dd>
            <dt className="text-muted">clock</dt>
            <dd>
              <Dot color={status.clock === 'synced' ? 'var(--st-good)' : 'var(--st-warn)'} label={status.clock} />
            </dd>
          </dl>
        </Card>
        <Card title="Database">
          <div className="font-display text-[2rem] leading-none font-semibold text-fg-strong">{bytes(db.used_size)}</div>
          <div className="mt-1 text-[12px] text-muted">in use, of {bytes(db.size)} on disk; WAL {bytes(db.wal_size)}</div>
          <div className="mt-3 h-2 overflow-hidden rounded bg-line/50" title="Data in use against the file size">
            <div className="h-full rounded bg-accent" style={{ width: `${Math.min(100, (db.used_size / Math.max(1, db.size)) * 100)}%` }} />
          </div>
          <div className="mt-2 font-mono text-[11px] text-muted">
            {db.path} · schema {db.schema_version}
          </div>
          {!db.ok && <p className="mt-2 text-[12px] text-bad">{db.error}</p>}
        </Card>
        <Card title="Active discovery">
          {status.active.disabled ? (
            <>
              <Chip tone="warn">
                <WarnIcon size={12} /> kill switch set
              </Chip>
              <p className="mt-2 text-[13px]">{status.active.reason}</p>
              <p className="mt-1 text-[12px] text-muted">
                by {status.active.by ?? 'unknown'} {status.active.at && ago(status.active.at)}
                {status.active.forced && ' (forced by LAN_TRACKER_ACTIVE_DISABLED)'}
              </p>
            </>
          ) : (
            <>
              <Chip tone="good">allowed</Chip>
              <p className="mt-2 text-[12px] text-muted">Probes run only on interfaces where active discovery is enabled, inside the rate budget.</p>
            </>
          )}
          <div className="mt-3">
            <Command cmd={cli(status.active.disabled ? 'active enable' : 'active disable --reason "…"')} />
          </div>
        </Card>
        <Card title="Last operator scan">
          {status.last_scan ? (
            <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-[13px]">
              <dt className="text-muted">interface</dt>
              <dd className="font-mono">{status.last_scan.interface}</dd>
              <dt className="text-muted">kind</dt>
              <dd className="font-mono">{status.last_scan.kind}</dd>
              <dt className="text-muted">started</dt>
              <dd className="font-mono">{ago(status.last_scan.started)}</dd>
              <dt className="text-muted">targets</dt>
              <dd className="font-mono">{status.last_scan.targets}</dd>
            </dl>
          ) : (
            <Empty>No operator scan yet.</Empty>
          )}
        </Card>
      </div>
      {ifs.map((i) => (
        <Card
          key={i.name}
          title={
            <span className="flex items-center gap-3">
              <span className="font-mono">{i.name}</span>
              <Dot color={i.state === 'up' ? 'var(--st-good)' : 'var(--st-bad)'} label={<span className="font-sans text-[12px] font-normal text-muted">{i.state}</span>} />
            </span>
          }
          sub={`${i.prefixes.join(', ') || 'no prefix'} · ${i.passive ? 'passive' : 'not passive'}${i.active ? ' + active' : ''}${i.replay ? ' · replay' : ''} · MAC ${i.mac ?? '?'}`}
        >
          <div className="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-4">
            {i.collectors.map((c) => (
              <div key={c.collector} className="rounded border border-line bg-bg p-3">
                <div className="flex items-center justify-between">
                  <span className="font-medium text-fg-strong">{c.collector}</span>
                  <Dot color={stateColor(c.state)} label={<span className="text-[12px]">{c.state}</span>} />
                </div>
                <div className="mt-1 font-mono text-[11px] text-muted">
                  {c.backend ?? '—'} · since {ago(c.since)}
                </div>
                {c.error && <p className="mt-1 text-[12px] text-bad">{c.error}</p>}
                {c.last_pass && (
                  <div className="mt-2 text-[12px]">
                    <div className="flex justify-between text-muted">
                      <span>last pass {ago(c.last_pass.at)}</span>
                      <span className="font-mono">{c.last_pass.seconds.toFixed(1)} s</span>
                    </div>
                    <div className="mt-1 h-1.5 overflow-hidden rounded bg-line/50" title={`${c.last_pass.replied} of ${c.last_pass.probed} answered`}>
                      <div className="h-full rounded bg-good" style={{ width: `${(c.last_pass.replied / Math.max(1, c.last_pass.probed)) * 100}%` }} />
                    </div>
                    <div className="mt-1 font-mono text-[11px] text-muted">
                      {c.last_pass.replied}/{c.last_pass.probed} answered
                      {c.last_pass.blocked ? ` · ${c.last_pass.blocked} blocked` : ''}
                      {!c.last_pass.complete && ' · cut short'}
                      {c.last_pass.overran && ' · overran'}
                    </div>
                  </div>
                )}
              </div>
            ))}
          </div>
        </Card>
      ))}
    </div>
  );
}
