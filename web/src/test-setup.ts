import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

import '@testing-library/jest-dom/vitest';

// Vitest runs with globals: false, so React Testing Library's automatic
// afterEach(cleanup) is never registered. Register it once here so every test
// file unmounts rendered trees between tests and the DOM does not leak.
afterEach(() => {
  cleanup();
});
