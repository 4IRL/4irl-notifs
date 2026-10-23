import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

import '@testing-library/jest-dom/vitest';

import { installMatchMediaStub, resetViewport } from './test-viewport';

// jsdom does not implement scrolling, so scrollIntoView does not exist there. A no-op stands
// in (tests that assert on scrolling spy on it); the real layout is covered by the e2e suite.
// jsdom has no matchMedia either: a stub reports the roomy layout until a test calls
// `setCompactViewport(true)` (see test-viewport.ts).
// (The Pages Functions tests run in a node environment, with no window.)
if (typeof window !== 'undefined') {
  window.HTMLElement.prototype.scrollIntoView = () => undefined;
  installMatchMediaStub();
}

// Vitest runs with globals: false, so React Testing Library's automatic
// afterEach(cleanup) is never registered. Register it once here so every test
// file unmounts rendered trees between tests and the DOM does not leak.
afterEach(() => {
  cleanup();
  resetViewport();
});
