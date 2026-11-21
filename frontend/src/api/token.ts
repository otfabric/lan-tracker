/** Reads the bearer token from a URL fragment of the form `#token=...`. */
export function readToken(hash: string): string | null {
  return new URLSearchParams(hash.replace(/^#/, '')).get('token') || null;
}

/**
 * Takes the token out of the address bar: it is returned to be kept in memory
 * and the fragment is removed, so it does not end up in bookmarks, shared
 * links or the history entry. It is never written to a query string, a cookie
 * or storage.
 */
export function takeToken(win: Pick<Window, 'location' | 'history'>): string | null {
  const token = readToken(win.location.hash);
  if (token !== null) {
    win.history.replaceState(win.history.state, '', win.location.pathname + win.location.search);
  }
  return token;
}
