import { useMemo, useState } from 'react';
import type { LTEvent } from '../../api/types';
import { Heatmap, TimeChart } from '../charts/charts';
import { useApp } from '../context';
import { EVENT_CATEGORIES, cli, dateTime, eventCategory, eventLabel, eventTypeParam, group } from '../format';
import { Card, Chip, Command, ErrorNote, SearchIcon, Skeleton, useLoad } from '../ui';
import { EventList } from './EventList';

export function Events() {
  const { api, scope, tick, live, navigate } = useApp();
  const params = new URLSearchParams(location.search);
  const fixedSince = params.get('since');
  const fixedUntil = params.get('until');
  const [cats, setCats] = useState<Set<string>>(() => new Set(params.get('cat') ? [params.get('cat')!] : []));
  const [type, setType] = useState(eventTypeParam(params.get('type')));
  const [warnOnly, setWarnOnly] = useState(false);
  const [q, setQ] = useState('');

  const window = useMemo(
    () => (fixedSince && fixedUntil ? { iface: scope.iface, range: { since: new Date(Number(fixedSince)), until: new Date(Number(fixedUntil)) } } : scope),
    [fixedSince, fixedUntil, scope],
  );
  const events = useLoad(() => api.events(window, 5000), [api, window.iface, window.range.since.getTime(), window.range.until.getTime(), tick]);
  const act = useLoad(() => api.activity(window, 72), [api, window.iface, window.range.since.getTime(), window.range.until.getTime(), tick]);

  const all = useMemo(() => {
    // Live events not yet in a stored answer go on top (no fixed window).
    const stored = events.data ?? [];
    if (fixedUntil) return [...stored].reverse();
    const last = stored.length ? stored[stored.length - 1]!.ts : '';
    return [...live.filter((e) => e.ts > last), ...[...stored].reverse()];
  }, [events.data, live, fixedUntil]);

  const filtered = useMemo(() => {
    const words = q.toLowerCase().split(/\s+/).filter(Boolean);
    return all.filter((e: LTEvent) => {
      if (type && e.type !== type) return false;
      if (cats.size && !cats.has(eventCategory(e.type).key)) return false;
      if (warnOnly && e.severity !== 'warning') return false;
      if (words.length) {
        const hay = [e.type, eventLabel(e.type), e.mac, e.old, e.new, e.cause, e.interface].join(' ').toLowerCase();
        if (!words.every((w) => hay.includes(w))) return false;
      }
      return true;
    });
  }, [all, type, cats, warnOnly, q]);

  const a = act.data;
  const toggle = (k: string) =>
    setCats((s) => {
      const n = new Set(s);
      if (n.has(k)) n.delete(k);
      else n.add(k);
      return n;
    });

  return (
    <div className="mx-auto flex max-w-[1600px] flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="font-display text-2xl font-semibold text-fg-strong">Events</h1>
        {fixedSince && fixedUntil && (
          <button type="button" className="cursor-pointer" onClick={() => navigate('/events')} title="Back to the selected range">
            <Chip tone="accent">
              {dateTime(new Date(Number(fixedSince)).toISOString())} → {dateTime(new Date(Number(fixedUntil)).toISOString())} ✕
            </Chip>
          </button>
        )}
        <span className="ml-auto">
          <Command cmd={cli(`events list --since ${Math.round((Date.now() - window.range.since.getTime()) / 3600_000)}h${eventTypeParam(type) ? ` --type ${eventTypeParam(type)}` : ''}`)} />
        </span>
      </div>
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[2fr_1fr]">
        <Card title="Events over time">
          {a ? (
            <TimeChart
              mode="bars"
              series={group(a.events, EVENT_CATEGORIES, a.buckets).map((g) => ({ key: g.cat.key, label: g.cat.label, color: g.cat.color, values: g.values }))}
              since={Date.parse(a.since)}
              stepMs={a.step_seconds * 1000}
              height={190}
              yLabel="Events"
              onBucket={(s, e) => navigate(`/events?since=${s}&until=${e}`)}
            />
          ) : (
            <Skeleton height={230} />
          )}
        </Card>
        <Card title="When things change" sub="Events by weekday and hour (local time)">
          <Heatmap times={filtered.map((e) => Date.parse(e.ts))} />
        </Card>
      </div>
      <Card>
        <div className="mb-3 flex flex-wrap items-center gap-2">
          {EVENT_CATEGORIES.map((c) => {
            const on = cats.has(c.key);
            return (
              <button
                key={c.key}
                type="button"
                aria-pressed={on}
                onClick={() => toggle(c.key)}
                className={`flex cursor-pointer items-center gap-1.5 rounded-full border px-2.5 py-1 text-[12px] ${on ? 'border-accent bg-accent-soft text-accent-strong' : 'border-line text-muted hover:text-fg'}`}
              >
                <span className="h-2 w-2 rounded-full" style={{ background: c.color }} />
                {c.label}
              </button>
            );
          })}
          <label className="ml-2 flex cursor-pointer items-center gap-1.5 text-[12px] text-muted">
            <input type="checkbox" checked={warnOnly} onChange={(e) => setWarnOnly(e.target.checked)} className="accent-[var(--ot-accent)]" />
            warnings only
          </label>
          {type && (
            <button type="button" className="cursor-pointer" onClick={() => setType('')}>
              <Chip tone="accent">{eventLabel(type)} ✕</Chip>
            </button>
          )}
          <label className="ml-auto flex min-w-[220px] items-center gap-2 rounded border border-control bg-bg px-2.5 py-1">
            <SearchIcon />
            <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter: MAC, IP, cause…" className="w-full bg-transparent text-[13px] outline-none placeholder:text-muted" aria-label="Filter events" />
          </label>
          <span className="font-mono text-[12px] text-muted">{filtered.length}</span>
        </div>
        {events.error !== undefined && <ErrorNote error={events.error} />}
        {events.data ? <EventList events={filtered.slice(0, 500)} /> : <Skeleton height={300} />}
        {filtered.length > 500 && <p className="pt-3 text-center text-[12px] text-muted">The latest 500 of {filtered.length} are shown; narrow the filter or the range.</p>}
      </Card>
    </div>
  );
}
