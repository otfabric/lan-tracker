// Small SVG charts drawn with the theme tokens: no chart library, so the
// page stays inside its CSP and its bundle stays small. Every chart has a
// hover layer (crosshair and tooltip, or a per-mark tooltip) and a legend
// for two or more series.

import { useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { axisTime, compact, num } from '../format';

export interface Series {
  key: string;
  label: string;
  color: string;
  values: number[];
}

/** The width of an element, following resizes. */
export function useWidth<T extends HTMLElement>(fallback = 600): [React.RefObject<T | null>, number] {
  const ref = useRef<T>(null);
  const [w, setW] = useState(fallback);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const update = () => setW(el.clientWidth || fallback);
    update();
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => ro.disconnect();
  }, [fallback]);
  return [ref, w];
}

/** Rounds a maximum up to a readable axis top with 4 steps. */
function niceMax(v: number): number {
  if (v <= 4) return 4;
  const p = 10 ** Math.floor(Math.log10(v / 4));
  for (const m of [1, 2, 2.5, 5, 10]) {
    if ((m * p * 4) >= v) return m * p * 4;
  }
  return v;
}

export function Legend({ items, hidden, onToggle }: { items: { key: string; label: string; color: string; value?: string }[]; hidden?: Set<string>; onToggle?: (k: string) => void }) {
  return (
    <ul className="flex flex-wrap gap-x-4 gap-y-1 text-[12px] text-muted">
      {items.map((it) => {
        const off = hidden?.has(it.key);
        return (
          <li key={it.key}>
            <button
              type="button"
              className={`flex items-center gap-1.5 ${onToggle ? 'cursor-pointer hover:text-fg' : 'cursor-default'} ${off ? 'opacity-40' : ''}`}
              onClick={onToggle ? () => onToggle(it.key) : undefined}
              aria-pressed={onToggle ? !off : undefined}
              title={onToggle ? (off ? 'Show' : 'Hide') : undefined}
            >
              <span className="inline-block h-2.5 w-2.5 rounded-[3px]" style={{ background: it.color }} />
              <span>{it.label}</span>
              {it.value !== undefined && <span className="font-mono text-fg">{it.value}</span>}
            </button>
          </li>
        );
      })}
    </ul>
  );
}

interface TimeChartProps {
  series: Series[];
  since: number; // ms
  stepMs: number;
  height?: number;
  mode?: 'area' | 'bars';
  yLabel: string;
  /** Called with the bucket's start when a bucket is clicked. */
  onBucket?: (start: number, end: number) => void;
  /** Point markers drawn on the time axis: time and color, with a tooltip. */
  markers?: { at: number; color: string; title: string }[];
}

/**
 * A stacked time chart (area or bars) with a crosshair tooltip. Series can
 * be hidden from the legend; colors follow the series, never their rank.
 */
export function TimeChart({ series, since, stepMs, height = 220, mode = 'area', yLabel, onBucket, markers }: TimeChartProps) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const [hover, setHover] = useState<number | null>(null);
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  const shown = series.filter((s) => !hidden.has(s.key));
  const n = series[0]?.values.length ?? 0;
  const pad = { l: 44, r: 12, t: 10, b: 24 };
  const iw = Math.max(10, width - pad.l - pad.r);
  const ih = height - pad.t - pad.b;

  const { stacks, max } = useMemo(() => {
    const stacks: number[][] = [];
    const base = new Array<number>(n).fill(0);
    for (const s of shown) {
      const top = s.values.map((v, i) => (base[i]! += v));
      stacks.push(top.slice());
    }
    return { stacks, max: niceMax(Math.max(1, ...base)) };
  }, [shown, n]);

  const x = (i: number) => pad.l + (i / Math.max(1, n)) * iw;
  const y = (v: number) => pad.t + ih - (v / max) * ih;
  const bw = iw / Math.max(1, n);
  const span = n * stepMs;
  const ticks = Math.max(2, Math.min(8, Math.floor(iw / 90)));

  function onMove(e: React.MouseEvent<SVGRectElement>) {
    const r = (e.currentTarget as SVGRectElement).getBoundingClientRect();
    const i = Math.floor(((e.clientX - r.left) / r.width) * n);
    setHover(Math.min(n - 1, Math.max(0, i)));
  }

  const total = hover === null ? 0 : shown.reduce((a, s) => a + (s.values[hover] ?? 0), 0);
  const tipLeft = hover === null ? 0 : Math.min(width - 190, Math.max(0, x(hover) + bw / 2 + 12));

  return (
    <div className="flex flex-col gap-2">
      <div ref={ref} className="relative w-full" style={{ height }}>
        <svg width={width} height={height} role="img" aria-label={`${yLabel} over time`}>
          {[0, 0.25, 0.5, 0.75, 1].map((f) => (
            <g key={f}>
              <line x1={pad.l} x2={pad.l + iw} y1={y(max * f)} y2={y(max * f)} stroke="var(--grid)" strokeWidth={1} />
              <text x={pad.l - 6} y={y(max * f) + 3.5} textAnchor="end" className="fill-muted font-mono text-[10px]">
                {compact(max * f)}
              </text>
            </g>
          ))}
          {Array.from({ length: ticks + 1 }, (_, k) => {
            const i = Math.round((k / ticks) * n);
            const anchor = k === 0 ? 'start' : k === ticks ? 'end' : 'middle';
            return (
              <text key={k} x={x(i)} y={height - 6} textAnchor={anchor} className="fill-muted font-mono text-[10px]">
                {axisTime(since + i * stepMs, span)}
              </text>
            );
          })}
          {mode === 'area' &&
            shown.map((s, si) => {
              const top = stacks[si]!;
              const bottom = si === 0 ? new Array<number>(n).fill(0) : stacks[si - 1]!;
              const pts = top.map((v, i) => `${x(i) + bw / 2},${y(v)}`);
              const back = bottom.map((v, i) => `${x(i) + bw / 2},${y(v)}`).reverse();
              return (
                <g key={s.key}>
                  <polygon points={[...pts, ...back].join(' ')} fill={s.color} fillOpacity={0.22} />
                  <polyline points={pts.join(' ')} fill="none" stroke={s.color} strokeWidth={1.75} strokeLinejoin="round" />
                </g>
              );
            })}
          {mode === 'bars' &&
            shown.map((s, si) => {
              const top = stacks[si]!;
              return (
                <g key={s.key}>
                  {top.map((v, i) => {
                    const b = si === 0 ? 0 : stacks[si - 1]![i]!;
                    if (v === b) return null;
                    const h = Math.max(1, y(b) - y(v) - (si > 0 ? 1 : 0));
                    return <rect key={i} x={x(i) + 0.5} y={y(v)} width={Math.max(1, bw - 2)} height={h} rx={bw > 6 ? 2 : 0} fill={s.color} />;
                  })}
                </g>
              );
            })}
          {markers?.map((m, k) => {
            const i = (m.at - since) / stepMs;
            if (i < 0 || i > n) return null;
            return (
              <g key={k}>
                <title>{m.title}</title>
                <line x1={x(i)} x2={x(i)} y1={pad.t} y2={pad.t + ih} stroke={m.color} strokeDasharray="3 3" strokeWidth={1} />
                <circle cx={x(i)} cy={pad.t + 4} r={4} fill={m.color} stroke="var(--ot-surface)" strokeWidth={2} />
              </g>
            );
          })}
          {hover !== null && <rect x={x(hover)} y={pad.t} width={bw} height={ih} fill="var(--ot-fg)" fillOpacity={0.06} pointerEvents="none" />}
          <rect
            x={pad.l}
            y={pad.t}
            width={iw}
            height={ih}
            fill="transparent"
            onMouseMove={onMove}
            onMouseLeave={() => setHover(null)}
            onClick={onBucket && hover !== null ? () => onBucket(since + hover * stepMs, since + (hover + 1) * stepMs) : undefined}
            className={onBucket ? 'cursor-pointer' : ''}
          />
        </svg>
        {hover !== null && (
          <div className="pointer-events-none absolute top-1 z-10 w-[180px] rounded-md border border-line bg-bg/95 p-2 text-[12px] shadow-lg" style={{ left: tipLeft }}>
            <div className="mb-1 font-mono text-[11px] text-muted">
              {new Date(since + hover * stepMs).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })}
            </div>
            {shown
              .filter((s) => (s.values[hover] ?? 0) > 0)
              .map((s) => (
                <div key={s.key} className="flex items-center justify-between gap-2">
                  <span className="flex items-center gap-1.5">
                    <span className="h-2 w-2 rounded-sm" style={{ background: s.color }} />
                    {s.label}
                  </span>
                  <span className="font-mono">{num(s.values[hover] ?? 0)}</span>
                </div>
              ))}
            <div className="mt-1 flex justify-between border-t border-line pt-1 font-medium text-fg-strong">
              <span>{yLabel}</span>
              <span className="font-mono">{num(total)}</span>
            </div>
          </div>
        )}
      </div>
      {series.length > 1 && (
        <Legend
          items={series.map((s) => ({ key: s.key, label: s.label, color: s.color, value: compact(s.values.reduce((a, b) => a + b, 0)) }))}
          hidden={hidden}
          onToggle={(k) =>
            setHidden((h) => {
              const next = new Set(h);
              if (next.has(k)) next.delete(k);
              else next.add(k);
              return next;
            })
          }
        />
      )}
    </div>
  );
}

/** A trend line for a stat tile. */
export function Sparkline({ values, color = 'var(--ot-accent)', height = 28 }: { values: number[]; color?: string; height?: number }) {
  const [ref, width] = useWidth<HTMLDivElement>(120);
  const max = Math.max(1, ...values);
  const n = values.length;
  if (n < 2) return <div ref={ref} style={{ height }} />;
  const pts = values.map((v, i) => `${(i / (n - 1)) * width},${height - 2 - (v / max) * (height - 4)}`);
  return (
    <div ref={ref} className="w-full" style={{ height }} aria-hidden>
      <svg width={width} height={height}>
        <polygon points={[`0,${height}`, ...pts, `${width},${height}`].join(' ')} fill={color} fillOpacity={0.15} />
        <polyline points={pts.join(' ')} fill="none" stroke={color} strokeWidth={1.5} strokeLinejoin="round" />
      </svg>
    </div>
  );
}

export interface BarItem {
  key: string;
  label: ReactNode;
  value: number;
  color?: string;
  hint?: string;
  onClick?: () => void;
}

/** Ranked horizontal bars with the value at the end. */
export function BarList({ items, color = 'var(--s1)', empty = 'Nothing in this range.', format = num }: { items: BarItem[]; color?: string; empty?: string; format?: (n: number) => string }) {
  const max = Math.max(1, ...items.map((i) => i.value));
  if (items.length === 0) return <p className="py-6 text-center text-[13px] text-muted">{empty}</p>;
  return (
    <ul className="flex flex-col gap-1.5">
      {items.map((it) => {
        const body = (
          <>
            <span className="relative z-10 min-w-0 flex-1 truncate">{it.label}</span>
            <span className="relative z-10 font-mono text-[12px] text-fg">{format(it.value)}</span>
            <span
              className="absolute inset-y-0 left-0 rounded-[4px] opacity-25 transition-[width] duration-500"
              style={{ width: `${(it.value / max) * 100}%`, background: it.color ?? color }}
            />
            <span className="absolute inset-y-0 left-0 w-[3px] rounded-l-[4px]" style={{ background: it.color ?? color }} />
          </>
        );
        const cls = 'relative flex w-full items-center gap-3 overflow-hidden rounded-[4px] px-2.5 py-1.5 text-left text-[13px]';
        return (
          <li key={it.key} title={it.hint}>
            {it.onClick ? (
              <button type="button" onClick={it.onClick} className={`${cls} cursor-pointer hover:bg-accent-soft/40`}>
                {body}
              </button>
            ) : (
              <div className={cls}>{body}</div>
            )}
          </li>
        );
      })}
    </ul>
  );
}

/** A donut with the total in the middle; each slice has a tooltip. */
export function Donut({ slices, size = 150, center, sub }: { slices: { key: string; label: string; value: number; color: string; hint?: string }[]; size?: number; center: string; sub: string }) {
  const total = slices.reduce((a, s) => a + s.value, 0);
  const r = size / 2 - 10;
  const c = 2 * Math.PI * r;
  let off = 0;
  const [hover, setHover] = useState<string | null>(null);
  const h = slices.find((s) => s.key === hover);
  return (
    <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} role="img" aria-label={`${center} ${sub}`}>
      <circle cx={size / 2} cy={size / 2} r={r} fill="none" stroke="var(--grid)" strokeWidth={14} />
      {total > 0 &&
        slices.map((s) => {
          const len = (s.value / total) * c;
          const gap = slices.filter((x) => x.value > 0).length > 1 ? 2 : 0;
          const el = (
            <circle
              key={s.key}
              cx={size / 2}
              cy={size / 2}
              r={r}
              fill="none"
              stroke={s.color}
              strokeWidth={hover === s.key ? 18 : 14}
              strokeDasharray={`${Math.max(0, len - gap)} ${c}`}
              strokeDashoffset={-off}
              transform={`rotate(-90 ${size / 2} ${size / 2})`}
              onMouseEnter={() => setHover(s.key)}
              onMouseLeave={() => setHover(null)}
              className="transition-[stroke-width]"
            >
              <title>{`${s.label}: ${s.value}${s.hint ? ` — ${s.hint}` : ''}`}</title>
            </circle>
          );
          off += len;
          return s.value > 0 ? el : null;
        })}
      <text x="50%" y="48%" textAnchor="middle" className="fill-fg-strong font-display text-[28px] font-semibold">
        {h ? h.value : center}
      </text>
      <text x="50%" y="62%" textAnchor="middle" className="fill-muted text-[11px]">
        {h ? h.label : sub}
      </text>
    </svg>
  );
}

/**
 * A week × hour-of-day heatmap of counts: when the network changes. One
 * hue, light to dark (sequential).
 */
export function Heatmap({ times, color = 'var(--s1)' }: { times: number[]; color?: string }) {
  const grid = useMemo(() => {
    const g = Array.from({ length: 7 }, () => new Array<number>(24).fill(0));
    for (const t of times) {
      const d = new Date(t);
      g[(d.getDay() + 6) % 7]![d.getHours()]! += 1;
    }
    return g;
  }, [times]);
  const max = Math.max(1, ...grid.flat());
  const days = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
  return (
    <div className="overflow-x-auto">
      <table className="border-separate border-spacing-[2px]">
        <tbody>
          {grid.map((row, di) => (
            <tr key={di}>
              <th className="pr-2 text-right font-mono text-[10px] font-normal text-muted">{days[di]}</th>
              {row.map((v, hi) => (
                <td key={hi} title={`${days[di]} ${String(hi).padStart(2, '0')}:00 — ${v} event${v === 1 ? '' : 's'}`} className="h-4 w-4 min-w-4 rounded-[3px]" style={{ background: v ? color : 'var(--grid)', opacity: v ? 0.2 + 0.8 * (v / max) : 1 }} />
              ))}
            </tr>
          ))}
          <tr>
            <th />
            {Array.from({ length: 24 }, (_, h) => (
              <td key={h} className="text-center font-mono text-[9px] text-muted">
                {h % 6 === 0 ? String(h).padStart(2, '0') : ''}
              </td>
            ))}
          </tr>
        </tbody>
      </table>
    </div>
  );
}

export interface Span {
  key: string;
  label: string;
  start: number;
  end: number;
  open: boolean;
  color: string;
  hint: string;
}

/** Address bindings on a time axis: one lane per address. */
export function Gantt({ spans, from, to }: { spans: Span[]; from: number; to: number }) {
  const [ref, width] = useWidth<HTMLDivElement>();
  const lanes = [...new Set(spans.map((s) => s.label))];
  const pad = { l: 120, r: 12 };
  const iw = Math.max(10, width - pad.l - pad.r);
  const x = (t: number) => pad.l + ((Math.min(Math.max(t, from), to) - from) / Math.max(1, to - from)) * iw;
  const lh = 26;
  const height = lanes.length * lh + 22;
  return (
    <div ref={ref} className="w-full">
      <svg width={width} height={height} role="img" aria-label="Address history">
        {lanes.map((l, i) => (
          <g key={l}>
            <text x={0} y={i * lh + 17} className="fill-fg font-mono text-[12px]">
              {l}
            </text>
            <line x1={pad.l} x2={pad.l + iw} y1={i * lh + 13} y2={i * lh + 13} stroke="var(--grid)" />
          </g>
        ))}
        {spans.map((s) => {
          const i = lanes.indexOf(s.label);
          const x1 = x(s.start);
          const x2 = Math.max(x1 + 3, x(s.end));
          return (
            <g key={s.key}>
              <title>{s.hint}</title>
              <rect x={x1} y={i * lh + 6} width={x2 - x1} height={14} rx={4} fill={s.color} fillOpacity={s.open ? 0.9 : 0.45} />
              {s.open && <circle cx={x2} cy={i * lh + 13} r={4} fill={s.color} stroke="var(--ot-surface)" strokeWidth={2} />}
            </g>
          );
        })}
        {[0, 0.25, 0.5, 0.75, 1].map((f) => (
          <text key={f} x={pad.l + f * iw} y={height - 4} textAnchor={f === 0 ? 'start' : f === 1 ? 'end' : 'middle'} className="fill-muted font-mono text-[10px]">
            {axisTime(from + f * (to - from), to - from)}
          </text>
        ))}
      </svg>
    </div>
  );
}
