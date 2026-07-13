import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';

import App from './App.tsx';
import { createApiClient } from './api/client.ts';
import './theme.css';
import './index.css';

// The API base URL is same-origin by default; override with VITE_API_BASE_URL
// at build time when the admin UI and the provisioning API live on different
// hostnames (see the deploy runbook).
const apiBaseUrl = import.meta.env.VITE_API_BASE_URL ?? '';
const client = createApiClient({ baseUrl: apiBaseUrl });

const rootElement = document.getElementById('root');
if (rootElement === null) {
  throw new Error('root element not found');
}

createRoot(rootElement).render(
  <StrictMode>
    <App client={client} />
  </StrictMode>,
);
