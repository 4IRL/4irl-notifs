import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { APP_STORE_URL, F_DROID_URL, GOOGLE_PLAY_URL } from '../ntfyLinks';
import { strings } from '../strings';
import { InstallCard } from './InstallCard';

// The card is static: the same three stores, in this order, on every platform.
const STORES = [
  { name: strings.googlePlayName, href: GOOGLE_PLAY_URL },
  { name: strings.fDroidName, href: F_DROID_URL },
  { name: strings.appStoreName, href: APP_STORE_URL },
];

function linkNameFor(storeName: string) {
  return `${strings.getNtfyOn}${storeName}${strings.opensInNewTab}`;
}

describe('InstallCard', () => {
  it('shows the heading and the skip-if-installed note', () => {
    render(<InstallCard />);

    expect(
      screen.getByRole('heading', { level: 2, name: strings.installCardHeading }),
    ).toBeVisible();
    expect(screen.getByText(strings.installCardNote)).toBeVisible();
  });

  it('renders the three store links in order with their URLs', () => {
    render(<InstallCard />);

    const links = within(screen.getByRole('list')).getAllByRole('link');
    expect(links).toHaveLength(STORES.length);
    STORES.forEach(({ name, href }, index) => {
      expect(links[index]).toHaveAccessibleName(linkNameFor(name));
      expect(links[index]).toHaveAttribute('href', href);
    });
  });

  it('shows each store name as the visible label', () => {
    render(<InstallCard />);

    for (const { name } of STORES) {
      expect(screen.getByRole('link', { name: linkNameFor(name) })).toHaveTextContent(name);
    }
  });

  it('opens every link in a new tab with noopener noreferrer', () => {
    render(<InstallCard />);

    for (const link of screen.getAllByRole('link')) {
      expect(link).toHaveAttribute('target', '_blank');
      expect(link).toHaveAttribute('rel', 'noopener noreferrer');
    }
  });

  it('draws generic decorative icons and loads no images or brand artwork', () => {
    const { container } = render(<InstallCard />);

    const icons = container.querySelectorAll('svg');
    expect(icons).toHaveLength(STORES.length);
    for (const icon of icons) {
      expect(icon).toHaveAttribute('aria-hidden', 'true');
    }
    expect(container.querySelector('img')).toBeNull();
    expect(screen.queryByRole('img')).not.toBeInTheDocument();
  });
});
