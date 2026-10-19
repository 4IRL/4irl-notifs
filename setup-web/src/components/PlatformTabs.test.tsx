import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { Platform } from '../platform';
import { strings } from '../strings';
import { CopyField } from './CopyField';
import { PlatformTabs } from './PlatformTabs';

function Harness({ detected = null }: { detected?: Platform | null }) {
  const [selected, setSelected] = useState<Platform | null>(detected);
  return (
    <PlatformTabs
      detected={detected}
      selected={selected}
      onSelect={setSelected}
      iosPanel={<CopyField label="iOS value" value="ios-topic" />}
      androidPanel={<CopyField label="Android value" value="android-topic" />}
    />
  );
}

const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');

afterEach(() => {
  vi.restoreAllMocks();
  if (originalClipboard) {
    Object.defineProperty(navigator, 'clipboard', originalClipboard);
  } else {
    Reflect.deleteProperty(navigator, 'clipboard');
  }
});

describe('PlatformTabs', () => {
  it('renders a labelled tablist with two tabs wired to their own tabpanels', () => {
    render(<Harness detected="ios" />);

    expect(screen.getByRole('tablist', { name: strings.tabsAriaLabel })).toBeInTheDocument();
    for (const [platform, label] of [
      ['ios', strings.tabIphone],
      ['android', strings.tabAndroid],
    ] as const) {
      const tab = screen.getByRole('tab', { name: label });
      expect(tab).toHaveAttribute('aria-controls', `platform-panel-${platform}`);
      const panel = document.getElementById(`platform-panel-${platform}`);
      expect(panel).toHaveAttribute('role', 'tabpanel');
      expect(panel).toHaveAttribute('aria-labelledby', tab.id);
    }
  });

  it('uses a roving tabindex that follows the selected tab', async () => {
    const user = userEvent.setup();
    render(<Harness detected="ios" />);
    const iphone = screen.getByRole('tab', { name: strings.tabIphone });
    const android = screen.getByRole('tab', { name: strings.tabAndroid });
    expect(iphone).toHaveAttribute('tabindex', '0');
    expect(android).toHaveAttribute('tabindex', '-1');

    await user.click(android);

    expect(iphone).toHaveAttribute('tabindex', '-1');
    expect(android).toHaveAttribute('tabindex', '0');
  });

  it('renders no panel content until a tab is selected', async () => {
    const user = userEvent.setup();
    render(<Harness detected={null} />);
    expect(screen.queryByText('ios-topic')).not.toBeInTheDocument();
    expect(screen.queryByText('android-topic')).not.toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: strings.tabIphone }));

    expect(screen.getByText('ios-topic')).toBeVisible();
  });

  it('keeps both panels mounted, hiding only the inactive one', async () => {
    const user = userEvent.setup();
    render(<Harness detected="ios" />);

    expect(screen.getByText('ios-topic')).toBeVisible();
    expect(screen.getByText('android-topic')).not.toBeVisible();

    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));

    expect(screen.getByText('ios-topic')).not.toBeVisible();
    expect(screen.getByText('android-topic')).toBeVisible();
  });

  it('preserves a CopyField "Copied!" state across a platform switch and back', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
      configurable: true,
      writable: true,
    });
    render(<Harness detected="ios" />);

    await user.click(screen.getByRole('button', { name: 'Copy iOS value' }));
    expect(await screen.findByRole('button', { name: strings.copiedButton })).toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));
    await user.click(screen.getByRole('tab', { name: strings.tabIphone }));

    expect(screen.getByRole('button', { name: strings.copiedButton })).toBeInTheDocument();
  });

  it('preserves a CopyField error state across a platform switch and back', async () => {
    const user = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
      configurable: true,
      writable: true,
    });
    render(<Harness detected="ios" />);

    await user.click(screen.getByRole('button', { name: 'Copy iOS value' }));
    expect(await screen.findByText(strings.copyError)).toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));
    await user.click(screen.getByRole('tab', { name: strings.tabIphone }));

    expect(screen.getByText(strings.copyError)).toBeVisible();
  });

  it('reports the tab the user chose through onSelect', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<PlatformTabs detected="ios" selected="ios" onSelect={onSelect} />);

    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));

    expect(onSelect).toHaveBeenCalledWith('android');
  });
});
