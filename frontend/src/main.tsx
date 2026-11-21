// Entry of the web UI the daemon serves (ui.enabled, ADR 0012).

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { HttpApi } from './api/http';
import { takeToken } from './api/token';
import type { Api } from './api/types';
import { App } from './app/App';
import './styles/index.css';

async function createApi(): Promise<Api> {
  // Development only: `npm run dev`, then /?mock serves a generated site.
  if (import.meta.env.DEV && new URLSearchParams(location.search).has('mock')) {
    const { MockApi } = await import('./api/mock');
    return new MockApi();
  }
  return new HttpApi(takeToken(window));
}

void createApi().then((api) =>
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <App api={api} />
    </StrictMode>,
  ),
);
