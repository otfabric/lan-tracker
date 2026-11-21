import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import tailwindcss from '@tailwindcss/vite';

// The web build the daemon embeds (frontend/embed.go) and serves when
// ui.enabled is set. `npm run dev` serves it with mock data at /?mock.
export default defineConfig({
  plugins: [react(), tailwindcss()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // The page runs under CSP `default-src 'self'`: a data: URI for a small
    // font or image would be blocked, so nothing is inlined.
    assetsInlineLimit: 0,
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['src/test/setup.ts'],
    css: false,
  },
});
