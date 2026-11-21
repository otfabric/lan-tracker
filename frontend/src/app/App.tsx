import { useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import { ApiError } from '../api/http';
import type { Api, InterfaceInfo, LTEvent, Status } from '../api/types';
import logoDark from '../assets/logo-dark.svg';
import logoLight from '../assets/logo-light.svg';
import appIcon from '../assets/lan-tracker.svg';
import { AppContext, useApp, type AppState } from './context';
import { RANGES, type RangeKey } from './format';
import {
  CollapseIcon,
  DhcpIcon,
  Dot,
  EventsIcon,
  GridIcon,
  HealthIcon,
  HostsIcon,
  IconButton,
  MoonIcon,
  PortIcon,
  RefreshIcon,
  Segmented,
  SunIcon,
  useLoad,
} from './ui';
import { Overview } from './views/Overview';
import { Hosts } from './views/Hosts';
import { HostDetail } from './views/HostDetail';
import { Events } from './views/Events';
import { Dhcp } from './views/Dhcp';
import { Services } from './views/Services';
import { Health } from './views/Health';

export type Theme = 'dark' | 'light';

function store<T extends string>(key: string, ok: readonly T[]): [() => T | null, (v: T) => void] {
  const k = `lan-tracker.${key}`;
  return [
    () => {
      try {
        const v = localStorage.getItem(k);
        return ok.includes(v as T) ? (v as T) : null;
      } catch {
        return null;
      }
    },
    (v: T) => {
      try {
        localStorage.setItem(k, v);
      } catch {
        /* not remembered */
      }
    },
  ];
}

const [loadTheme, saveTheme] = store<Theme>('theme', ['dark', 'light']);
const [loadRange, saveRange] = store<RangeKey>('range', RANGES.map((r) => r.key));
const [loadNav, saveNav] = store<'open' | 'closed'>('nav', ['open', 'closed']);

const NAV: { path: string; label: string; icon: ReactNode }[] = [
  { path: '/', label: 'Overview', icon: <GridIcon /> },
  { path: '/hosts', label: 'Devices', icon: <HostsIcon /> },
  { path: '/events', label: 'Events', icon: <EventsIcon /> },
  { path: '/dhcp', label: 'DHCP', icon: <DhcpIcon /> },
  { path: '/services', label: 'TCP endpoints', icon: <PortIcon /> },
  { path: '/health', label: 'Tracker health', icon: <HealthIcon /> },
];

const REFRESH_MS = 30_000;

export function App({ api }: { api: Api }) {
  const [theme, setTheme] = useState<Theme>(() => loadTheme() ?? (matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark'));
  const [loc, setLoc] = useState(location.pathname + location.search);
  const path = loc.split('?')[0]!;
  const [iface, setIface] = useState('');
  const [rangeKey, setRangeKey] = useState<RangeKey>(() => loadRange() ?? '24h');
  const [tick, setTick] = useState(0);
  const [live, setLive] = useState<LTEvent[]>([]);
  const [streaming, setStreaming] = useState(false);
  const [navOpen, setNavOpen] = useState(() => loadNav() !== 'closed');
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);

  useEffect(() => {
    const onPop = () => setLoc(location.pathname + location.search);
    addEventListener('popstate', onPop);
    return () => removeEventListener('popstate', onPop);
  }, []);

  // The time range slides with the clock: each refresh moves it forward.
  useEffect(() => {
    const t = setInterval(() => {
      setNow(Date.now());
      setTick((n) => n + 1);
    }, REFRESH_MS);
    return () => clearInterval(t);
  }, []);

  useEffect(() => {
    setLive([]);
    return api.stream(
      iface,
      (e) => setLive((l) => [e, ...l].slice(0, 200)),
      (s) => setStreaming(s),
    );
  }, [api, iface]);

  const ifaces = useLoad(() => api.interfaces(), [api, tick]);
  const status = useLoad(() => api.status(), [api, tick]);

  const navigate = useCallback((p: string) => {
    if (p !== location.pathname + location.search) history.pushState(null, '', p);
    setLoc(p);
    document.getElementById('main')?.scrollTo({ top: 0 });
  }, []);

  const scope = useMemo(() => {
    const ms = RANGES.find((r) => r.key === rangeKey)!.ms;
    return { iface, range: { since: new Date(now - ms), until: new Date(now) } };
  }, [iface, rangeKey, now]);

  const state: AppState = {
    api,
    scope,
    rangeKey,
    interfaces: ifaces.data ?? [],
    tick,
    live,
    streaming,
    navigate,
    setIface,
    setRange: (k) => {
      saveRange(k);
      setRangeKey(k);
      setNow(Date.now());
    },
  };

  const authError = (ifaces.error instanceof ApiError && ifaces.error.auth) || (status.error instanceof ApiError && status.error.auth);
  const hostMatch = /^\/hosts\/(.+)$/.exec(path);
  let view: ReactNode;
  if (hostMatch) view = <HostDetail id={decodeURIComponent(hostMatch[1]!)} />;
  else if (path === '/hosts') view = <Hosts />;
  else if (path === '/events') view = <Events />;
  else if (path === '/dhcp') view = <Dhcp />;
  else if (path === '/services') view = <Services />;
  else if (path === '/health') view = <Health status={status.data} />;
  else view = <Overview status={status.data} />;

  return (
    <AppContext.Provider value={state}>
      <div className="flex h-full flex-col">
        <Header
          theme={theme}
          onTheme={() => {
            const t = theme === 'dark' ? 'light' : 'dark';
            saveTheme(t);
            setTheme(t);
          }}
          status={status.data}
          streaming={streaming}
        />
        <div className="flex min-h-0 flex-1">
          <nav aria-label="Views" className={`flex shrink-0 flex-col border-r border-line bg-header py-3 transition-[width] ${navOpen ? 'w-52' : 'w-14'}`}>
            {NAV.map((n) => {
              const active = n.path === '/' ? path === '/' : path.startsWith(n.path);
              return (
                <a
                  key={n.path}
                  href={n.path}
                  onClick={(e) => {
                    e.preventDefault();
                    navigate(n.path);
                  }}
                  aria-current={active ? 'page' : undefined}
                  title={n.label}
                  className={`mx-2 flex items-center gap-3 rounded border-l-2 px-2.5 py-2 text-[13px] ${active ? 'border-accent bg-accent-soft text-accent-strong' : 'border-transparent text-muted hover:bg-surface hover:text-fg'}`}
                >
                  {n.icon}
                  {navOpen && <span className="truncate">{n.label}</span>}
                </a>
              );
            })}
            <div className="mt-auto px-2">
              {navOpen && <InterfaceList ifaces={ifaces.data ?? []} status={status.data} />}
              <button
                type="button"
                onClick={() => {
                  saveNav(navOpen ? 'closed' : 'open');
                  setNavOpen(!navOpen);
                }}
                className="mt-2 flex w-full cursor-pointer items-center gap-2 rounded px-2.5 py-2 text-[12px] text-muted hover:text-fg"
                aria-label={navOpen ? 'Collapse the menu' : 'Expand the menu'}
              >
                <span className={navOpen ? '' : 'rotate-180'}>
                  <CollapseIcon />
                </span>
                {navOpen && 'Collapse'}
              </button>
            </div>
          </nav>
          <div className="flex min-w-0 flex-1 flex-col">
            <Toolbar
              ifaces={ifaces.data ?? []}
              onRefresh={() => {
                setNow(Date.now());
                setTick((n) => n + 1);
              }}
            />
            <main id="main" className="min-h-0 flex-1 overflow-y-auto p-5">
              {authError ? <TokenNeeded /> : <div key={loc}>{view}</div>}
            </main>
          </div>
        </div>
      </div>
    </AppContext.Provider>
  );
}

function Header({ theme, onTheme, status, streaming }: { theme: Theme; onTheme: () => void; status?: Status; streaming: boolean }) {
  const state = status?.state;
  return (
    <header className="flex h-14 shrink-0 items-center gap-4 border-b border-line bg-header px-4">
      <img src={appIcon} alt="" width={32} height={32} />
      <span className="font-display text-[1.45rem] font-semibold tracking-[0.01em] text-fg-strong">LAN Tracker</span>
      <span className="hidden items-center gap-1.5 text-[12px] text-muted sm:flex">
        powered by
        <img src={theme === 'dark' ? logoDark : logoLight} alt="" width={16} height={16} />
        <span className="font-display text-[14px] font-medium tracking-[0.01em] text-fg">
          <b className="font-bold">OT</b> Fabric
        </span>
      </span>
      <span className="ml-auto flex items-center gap-3 text-[12px]">
        {status && (
          <span className="hidden font-mono text-muted md:inline" title={`${status.platform}, pid ${status.pid}`}>
            {status.version}
            {status.replay && ' · replay'}
          </span>
        )}
        {state && (
          <span className="ot-label inline-flex items-center rounded border border-line px-2 py-1.5" title={status?.problems?.join('\n') || 'Every configured collector is running'}>
            <Dot color={state === 'ok' ? 'var(--st-good)' : state === 'degraded' ? 'var(--st-warn)' : 'var(--st-bad)'} label={state} />
          </span>
        )}
        <span className="ot-label inline-flex items-center rounded border border-line px-2 py-1.5" title={streaming ? 'Receiving events as they happen' : 'Not connected to the event stream; retrying'}>
          <Dot color={streaming ? 'var(--ot-accent)' : 'var(--ot-muted)'} pulse={streaming} label={streaming ? 'live' : 'offline'} />
        </span>
        <IconButton label={theme === 'dark' ? 'Switch to light theme' : 'Switch to dark theme'} onClick={onTheme}>
          {theme === 'dark' ? <SunIcon /> : <MoonIcon />}
        </IconButton>
      </span>
    </header>
  );
}

function Toolbar({ ifaces, onRefresh }: { ifaces: InterfaceInfo[]; onRefresh: () => void }) {
  const { scope, setIface, rangeKey, setRange } = useApp();
  return (
    <div className="flex flex-wrap items-center gap-3 border-b border-line px-5 py-2.5">
      <label className="flex items-center gap-2 text-[12px] text-muted">
        <span className="ot-label">Network</span>
        <select
          value={scope.iface}
          onChange={(e) => setIface(e.target.value)}
          className="rounded border border-control bg-bg px-2 py-1 font-mono text-[12px] text-fg"
        >
          <option value="">All interfaces</option>
          {ifaces.map((i) => (
            <option key={i.name} value={i.name}>
              {i.name}
              {i.prefixes[0] ? ` · ${i.prefixes[0]}` : ''}
            </option>
          ))}
        </select>
      </label>
      <span className="ml-auto flex items-center gap-2">
        <Segmented label="Time range" value={rangeKey} options={RANGES.map((r) => ({ key: r.key, label: r.key }))} onChange={setRange} />
        <IconButton label="Refresh now" onClick={onRefresh}>
          <RefreshIcon />
        </IconButton>
      </span>
    </div>
  );
}

function InterfaceList({ ifaces, status }: { ifaces: InterfaceInfo[]; status?: Status }) {
  if (ifaces.length === 0) return null;
  return (
    <div className="mb-1 rounded border border-line p-2">
      <div className="ot-label mb-1.5">Interfaces</div>
      <ul className="flex flex-col gap-1.5 text-[12px]">
        {ifaces.map((i) => {
          const live = (status?.hosts[i.name]?.ACTIVE ?? 0) + (status?.hosts[i.name]?.RECENT ?? 0);
          return (
            <li key={i.name} className="flex items-center justify-between gap-2" title={`${i.prefixes.join(', ')}\n${i.passive ? 'passive' : ''}${i.active ? ' + active' : ''}`}>
              <Dot color={i.state === 'up' ? 'var(--st-good)' : i.state === 'unknown' ? 'var(--ot-muted)' : 'var(--st-bad)'} label={<span className="font-mono">{i.name}</span>} />
              <span className="font-mono text-muted">
                {live}/{i.hosts}
              </span>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function TokenNeeded() {
  return (
    <div className="mx-auto mt-16 max-w-xl rounded-lg border border-line bg-surface p-6">
      <h1 className="font-display text-xl font-semibold text-fg-strong">Open the UI with its token</h1>
      <p className="mt-2 text-[13px] text-muted">
        This page needs the access token the daemon created. On the edge device, print the URL and open it here (the part after # is the token; treat it like a password):
      </p>
      <pre className="mt-3 rounded border border-line bg-bg p-3 font-mono text-[12px]">sudo lan-tracker ui url</pre>
      <p className="mt-3 text-[13px] text-muted">From your workstation, forward the port first:</p>
      <pre className="mt-2 rounded border border-line bg-bg p-3 font-mono text-[12px]">ssh -N -L 9735:127.0.0.1:9735 user@edge-device</pre>
    </div>
  );
}
