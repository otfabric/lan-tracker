import type { LTEvent } from '../../api/types';
import { useApp } from '../context';
import { ago, dateTime, eventCategory, eventLabel } from '../format';
import { Chip, Empty } from '../ui';

const sevTone = { info: 'muted', notice: 'blue', warning: 'warn' } as const;

/** Events newest first: what happened, to which device, and why. */
export function EventList({ events, fresh, compact = false }: { events: LTEvent[]; fresh?: Set<string>; compact?: boolean }) {
  const { navigate } = useApp();
  if (events.length === 0) return <Empty>No events in this range.</Empty>;
  return (
    <ol className="flex flex-col">
      {events.map((e, i) => {
        const cat = eventCategory(e.type);
        const key = `${e.id ?? ''}-${e.ts}-${e.type}-${i}`;
        const change = [e.old, e.new].filter(Boolean).join(' → ');
        return (
          <li key={key} className={`grid grid-cols-[4px_1fr] gap-3 border-b border-line/60 py-2 last:border-0 ${fresh?.has(`${e.id}-${e.ts}`) ? 'lt-new' : ''}`}>
            <span className="rounded-full" style={{ background: cat.color }} title={cat.label} />
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                <span className="font-medium text-fg-strong">{eventLabel(e.type)}</span>
                {e.severity === 'warning' && <Chip tone={sevTone[e.severity]}>warning</Chip>}
                {e.interface && <span className="font-mono text-[11px] text-muted">{e.interface}</span>}
                {e.mac &&
                  (e.host_id ? (
                    <button type="button" className="cursor-pointer font-mono text-[12px] text-accent hover:underline" onClick={() => navigate(`/hosts/${encodeURIComponent(e.host_id!)}`)}>
                      {e.mac}
                    </button>
                  ) : (
                    <span className="font-mono text-[12px]">{e.mac}</span>
                  ))}
                {change && <span className="truncate font-mono text-[12px] text-fg">{change}</span>}
                <span className="ml-auto font-mono text-[11px] whitespace-nowrap text-muted" title={dateTime(e.ts) + (e.clock_sync === 'unsynced' ? ' (clock not synchronised)' : '')}>
                  {compact ? ago(e.ts) : dateTime(e.ts)}
                </span>
              </div>
              {!compact && e.cause && <p className="mt-0.5 truncate text-[12px] text-muted">{e.cause}</p>}
            </div>
          </li>
        );
      })}
    </ol>
  );
}
