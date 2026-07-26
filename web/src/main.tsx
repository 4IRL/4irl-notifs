import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';

import App from './App.tsx';
import { createApiClient } from './api/client.ts';
import { createPersonApiClient } from './api/personClient.ts';
import './theme.css';
import './index.css';

// Both clients are always same-origin: the provisioning API (`/v1/*`) and the
// person service (`/people`, `/apps`) are reached through Cloudflare Pages
// Functions that proxy to the backends server-side, so no build-time base URL
// is needed. The People and Apps views are gated on their own build flags
// (VITE_PEOPLE_ENABLED / VITE_APPS_ENABLED, both set to 'true' in the Pages
// build) rather than a base URL: local dev has no person backend, so the flags
// stay unset there and those views are omitted. Both views live in the
// person-service, so the personClient is created whenever either is enabled.
const client = createApiClient({});

const peopleEnabled = import.meta.env.VITE_PEOPLE_ENABLED === 'true';
const appsEnabled = import.meta.env.VITE_APPS_ENABLED === 'true';
const personClient = peopleEnabled || appsEnabled ? createPersonApiClient({}) : undefined;

const rootElement = document.getElementById('root');
if (rootElement === null) {
  throw new Error('root element not found');
}

createRoot(rootElement).render(
  <StrictMode>
    <App client={client} personClient={personClient} appsEnabled={appsEnabled} />
  </StrictMode>,
);
