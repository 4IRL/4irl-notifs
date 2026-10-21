import type { ReactNode } from 'react';

import { APP_STORE_URL, F_DROID_URL, GOOGLE_PLAY_URL } from '../ntfyLinks';
import { strings } from '../strings';
import './InstallCard.css';

interface Store {
  name: string;
  href: string;
  icon: ReactNode;
}

// Generic outline glyphs (play triangle, package, bag), not the stores' logos: the page
// ships no brand artwork. Each button is told apart by its visible store name.
const STORES: Store[] = [
  {
    name: strings.googlePlayName,
    href: GOOGLE_PLAY_URL,
    icon: <polygon points="7,4 20,12 7,20" />,
  },
  {
    name: strings.fDroidName,
    href: F_DROID_URL,
    icon: (
      <>
        <path d="M12 3l8 4.5v9L12 21l-8-4.5v-9L12 3z" />
        <path d="M12 12l8-4.5M12 12v9M12 12L4 7.5" />
      </>
    ),
  },
  {
    name: strings.appStoreName,
    href: APP_STORE_URL,
    icon: (
      <>
        <path d="M6 8h12l1 12H5L6 8z" />
        <path d="M9 8a3 3 0 0 1 6 0" />
      </>
    ),
  },
];

/**
 * The download card shown on every view, whatever the detected phone or selected tab, so
 * someone without the ntfy app can get it before the steps. The three store buttons stay
 * on one row at every width. The links open in a new tab, so they carry noopener noreferrer.
 */
export function InstallCard() {
  return (
    <section className="setup-page__install">
      <h2 className="setup-page__install-heading">{strings.installCardHeading}</h2>
      <p className="setup-page__install-note">{strings.installCardNote}</p>
      {/* role="list" restores list semantics that WebKit drops under `list-style: none`. */}
      <ul className="setup-page__stores" role="list">
        {STORES.map(({ name, href, icon }) => (
          <li key={name}>
            {/* The label names the app and the new-tab behavior; it contains the visible store name. */}
            <a
              className="setup-page__store"
              href={href}
              target="_blank"
              rel="noopener noreferrer"
              aria-label={`${strings.getNtfyOn}${name}${strings.opensInNewTab}`}
            >
              <svg
                className="setup-page__store-icon"
                viewBox="0 0 24 24"
                aria-hidden="true"
                focusable="false"
              >
                {icon}
              </svg>
              {name}
            </a>
          </li>
        ))}
      </ul>
    </section>
  );
}
