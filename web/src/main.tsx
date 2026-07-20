import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';

import App from './App.tsx';
import { createApiClient } from './api/client.ts';
import { createPersonApiClient } from './api/personClient.ts';
import './theme.css';
import './index.css';

// The API base URL is same-origin by default; override with VITE_API_BASE_URL
// at build time when the admin UI and the provisioning API live on different
// hostnames (see the deploy runbook).
const apiBaseUrl = import.meta.env.VITE_API_BASE_URL ?? '';
const client = createApiClient({ baseUrl: apiBaseUrl });

// The person-service base URL is set at Cloudflare Pages build time via
// VITE_PERSON_SERVICE_URL; the local stack has no person-service Worker, so
// it stays unset there and the people view is omitted entirely.
const personServiceUrl = import.meta.env.VITE_PERSON_SERVICE_URL ?? '';
const personClient =
  personServiceUrl === '' ? undefined : createPersonApiClient({ baseUrl: personServiceUrl });

const rootElement = document.getElementById('root');
if (rootElement === null) {
  throw new Error('root element not found');
}

createRoot(rootElement).render(
  <StrictMode>
    <App client={client} personClient={personClient} />
  </StrictMode>,
);
