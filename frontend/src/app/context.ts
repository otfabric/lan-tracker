import { createContext, useContext } from 'react';
import type { Api, InterfaceInfo, LTEvent, Scope } from '../api/types';
import type { RangeKey } from './format';

export interface AppState {
  api: Api;
  scope: Scope;
  rangeKey: RangeKey;
  interfaces: InterfaceInfo[];
  /** Bumped by the refresh timer and by live events: views reload on it. */
  tick: number;
  /** Events streamed since the page opened, newest first. */
  live: LTEvent[];
  streaming: boolean;
  navigate: (path: string) => void;
  setIface: (iface: string) => void;
  setRange: (key: RangeKey) => void;
}

export const AppContext = createContext<AppState | null>(null);

export function useApp(): AppState {
  const s = useContext(AppContext);
  if (!s) throw new Error('useApp outside the app');
  return s;
}
