import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';

import App from './App.tsx';
import './theme.css';
import './index.css';

const rootElement = document.getElementById('root');
if (rootElement === null) {
  throw new Error('root element not found');
}

createRoot(rootElement).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
