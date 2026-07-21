import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';

import App from './App.tsx';
import { createApiClient } from './api/client.ts';
import { createPersonApiClient } from './api/personClient.ts';
import './theme.css';
import './index.css';

// Both clients are always same-origin: the provisioning API (`/v1/*`) and the
// person service (`/people`) are reached through Cloudflare Pages Functions
// that proxy to the backends server-side, so no build-time base URL is needed.
// The People view is gated on the VITE_PEOPLE_ENABLED build flag (set to 'true'
// in the Pages build) rather than a base URL: local dev has no person backend,
// so the flag stays unset there and the People view is omitted entirely.
const client = createApiClient({});

const peopleEnabled = import.meta.env.VITE_PEOPLE_ENABLED === 'true';
const personClient = peopleEnabled ? createPersonApiClient({}) : undefined;

const rootElement = document.getElementById('root');
if (rootElement === null) {
  throw new Error('root element not found');
}

createRoot(rootElement).render(
  <StrictMode>
    <App client={client} personClient={personClient} />
  </StrictMode>,
);
