import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

afterEach(cleanup);

// jsdom does no layout: give the virtualized table a viewport to fill.
Object.defineProperty(HTMLElement.prototype, 'clientHeight', { configurable: true, get: () => 2400 });

globalThis.ResizeObserver = class {
  observe() {}
  unobserve() {}
  disconnect() {}
};

// jsdom does not scroll.
Element.prototype.scrollTo = function scrollTo() {};
