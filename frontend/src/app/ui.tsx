import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Sparkline } from './charts/charts';

export function Card({ title, sub, actions, children, className = '' }: { title?: ReactNode; sub?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={`flex min-w-0 flex-col rounded-lg border border-line bg-surface p-4 ${className}`}>
      {(title || actions) && (
        <header className="mb-3 flex items-start gap-3">
          <div className="min-w-0 flex-1">
            {title && <h2 className="font-display text-[1.08rem] font-semibold tracking-[0.01em] text-fg-strong">{title}</h2>}
            {sub && <p className="mt-0.5 text-[12px] text-muted">{sub}</p>}
          </div>
          {actions}
        </header>
      )}
      {children}
    </section>
  );
}

export type Tone = 'accent' | 'good' | 'warn' | 'bad' | 'muted' | 'blue';

const toneText: Record<Tone, string> = {
  accent: 'text-accent',
  good: 'text-good',
  warn: 'text-warn',
  bad: 'text-bad',
  muted: 'text-muted',
  blue: 'text-blue',
};

const toneVar: Record<Tone, string> = {
  accent: 'var(--ot-accent)',
  good: 'var(--st-good)',
  warn: 'var(--st-warn)',
  bad: 'var(--st-bad)',
  muted: 'var(--ot-muted)',
  blue: 'var(--ot-blue)',
};

/** A headline number with its trend. */
export function Stat({ label, value, unit, tone = 'accent', trend, hint, onClick, icon }: { label: string; value: ReactNode; unit?: string; tone?: Tone; trend?: number[]; hint?: string; onClick?: () => void; icon?: ReactNode }) {
  const inner = (
    <>
      <span className="ot-label flex items-center gap-1.5">
        {icon}
        {label}
      </span>
      <span className="mt-2 flex items-baseline gap-1.5">
        <span className={`font-display text-[2.2rem] leading-none font-semibold tabular-nums ${toneText[tone]}`}>{value}</span>
        {unit && <span className="text-[12px] text-muted">{unit}</span>}
      </span>
      {trend && (
        <span className="mt-2 block">
          <Sparkline values={trend} color={toneVar[tone]} />
        </span>
      )}
    </>
  );
  const cls = 'flex min-w-0 flex-col rounded-lg border border-line bg-surface p-4 text-left';
  return onClick ? (
    <button type="button" className={`${cls} cursor-pointer transition-colors hover:border-accent`} onClick={onClick} title={hint}>
      {inner}
    </button>
  ) : (
    <div className={cls} title={hint}>
      {inner}
    </div>
  );
}

export function Chip({ children, tone = 'muted', title }: { children: ReactNode; tone?: Tone; title?: string }) {
  return (
    <span title={title} className={`inline-flex items-center gap-1 rounded border border-current/40 px-1.5 py-[1px] font-mono text-[11px] whitespace-nowrap ${toneText[tone]}`}>
      {children}
    </span>
  );
}

/** A colored dot with a label: state is never color alone. */
export function Dot({ color, label, pulse }: { color: string; label?: ReactNode; pulse?: boolean }) {
  return (
    <span className="inline-flex items-center gap-1.5 whitespace-nowrap">
      <span className={`inline-block h-2 w-2 shrink-0 rounded-full ${pulse ? 'lt-live' : ''}`} style={{ background: color }} />
      {label}
    </span>
  );
}

export function IconButton({ label, onClick, children }: { label: string; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" aria-label={label} title={label} onClick={onClick} className="inline-flex h-8 w-8 cursor-pointer items-center justify-center rounded border border-line text-muted hover:border-control hover:text-fg">
      {children}
    </button>
  );
}

export function Segmented<T extends string>({ value, options, onChange, label }: { value: T; options: { key: T; label: string }[]; onChange: (v: T) => void; label: string }) {
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex rounded border border-line p-0.5">
      {options.map((o) => (
        <button
          key={o.key}
          type="button"
          role="radio"
          aria-checked={value === o.key}
          onClick={() => onChange(o.key)}
          className={`cursor-pointer rounded-[3px] px-2.5 py-1 font-mono text-[12px] ${value === o.key ? 'bg-accent-soft text-accent-strong' : 'text-muted hover:text-fg'}`}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="py-8 text-center text-[13px] text-muted">{children}</p>;
}

export function ErrorNote({ error }: { error: unknown }) {
  return <p className="rounded border border-bad/50 px-3 py-2 text-[13px] text-bad">{error instanceof Error ? error.message : String(error)}</p>;
}

export function Skeleton({ height = 120 }: { height?: number }) {
  return <div className="animate-pulse rounded bg-line/40" style={{ height }} />;
}

/** A shell command with a copy button: what the CLI says about the same thing. */
export function Command({ cmd }: { cmd: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <span className="inline-flex max-w-full items-center gap-2 rounded border border-line bg-bg px-2 py-1 font-mono text-[12px]">
      <span className="truncate text-muted">$</span>
      <span className="truncate">{cmd}</span>
      <button
        type="button"
        className="cursor-pointer text-muted hover:text-accent"
        aria-label="Copy command"
        onClick={() => {
          void navigator.clipboard?.writeText(cmd).then(() => {
            setCopied(true);
            setTimeout(() => setCopied(false), 1500);
          });
        }}
      >
        {copied ? '✓' : <CopyIcon />}
      </button>
    </span>
  );
}

export interface Loaded<T> {
  data: T | undefined;
  error: unknown;
  loading: boolean;
}

/**
 * Loads data and reloads it when deps change. A reload keeps the previous
 * data on screen until the new data arrives, so periodic refreshes do not
 * flicker.
 */
export function useLoad<T>(fn: () => Promise<T>, deps: unknown[]): Loaded<T> {
  const [state, setState] = useState<Loaded<T>>({ data: undefined, error: undefined, loading: true });
  const seq = useRef(0);
  useEffect(() => {
    const n = ++seq.current;
    setState((s) => ({ ...s, loading: true }));
    fn().then(
      (data) => n === seq.current && setState({ data, error: undefined, loading: false }),
      (error: unknown) => n === seq.current && setState((s) => ({ data: s.data, error, loading: false })),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  return state;
}

type IconProps = { size?: number };
const svg = (size: number, children: ReactNode) => (
  <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round" aria-hidden>
    {children}
  </svg>
);
export const GridIcon = ({ size = 18 }: IconProps) => svg(size, <><rect x="3" y="3" width="7" height="7" rx="1.5" /><rect x="14" y="3" width="7" height="7" rx="1.5" /><rect x="3" y="14" width="7" height="7" rx="1.5" /><rect x="14" y="14" width="7" height="7" rx="1.5" /></>);
export const HostsIcon = ({ size = 18 }: IconProps) => svg(size, <><rect x="3" y="4" width="18" height="6" rx="1.5" /><rect x="3" y="14" width="18" height="6" rx="1.5" /><path d="M7 7h.01M7 17h.01" /></>);
export const EventsIcon = ({ size = 18 }: IconProps) => svg(size, <path d="M3 12h4l3-8 4 16 3-8h4" />);
export const DhcpIcon = ({ size = 18 }: IconProps) => svg(size, <><circle cx="12" cy="5" r="2.5" /><circle cx="5" cy="19" r="2.5" /><circle cx="19" cy="19" r="2.5" /><path d="M12 7.5v4M12 11.5 6.5 17M12 11.5l5.5 5.5" /></>);
export const PortIcon = ({ size = 18 }: IconProps) => svg(size, <><rect x="4" y="6" width="16" height="12" rx="2" /><path d="M8 10v4M12 10v4M16 10v4" /></>);
export const HealthIcon = ({ size = 18 }: IconProps) => svg(size, <path d="M12 21s-7-4.5-9-9.5C1.5 7 5 3.5 8.5 4.5 10.5 5 12 7 12 7s1.5-2 3.5-2.5C19 3.5 22.5 7 21 11.5 19 16.5 12 21 12 21Z" />);
export const SunIcon = ({ size = 16 }: IconProps) => svg(size, <><circle cx="12" cy="12" r="4" /><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" /></>);
export const MoonIcon = ({ size = 16 }: IconProps) => svg(size, <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8Z" />);
export const CopyIcon = ({ size = 13 }: IconProps) => svg(size, <><rect x="9" y="9" width="12" height="12" rx="2" /><path d="M5 15V5a2 2 0 0 1 2-2h10" /></>);
export const WarnIcon = ({ size = 14 }: IconProps) => svg(size, <><path d="M12 3 2 21h20L12 3Z" /><path d="M12 10v5M12 18h.01" /></>);
export const SearchIcon = ({ size = 15 }: IconProps) => svg(size, <><circle cx="11" cy="11" r="7" /><path d="m20 20-3.5-3.5" /></>);
export const CloseIcon = ({ size = 16 }: IconProps) => svg(size, <path d="M6 6l12 12M18 6 6 18" />);
export const RefreshIcon = ({ size = 16 }: IconProps) => svg(size, <><path d="M20 11a8 8 0 1 0-2.3 5.7" /><path d="M20 4v7h-7" /></>);
export const CollapseIcon = ({ size = 16 }: IconProps) => svg(size, <path d="M15 6l-6 6 6 6" />);
