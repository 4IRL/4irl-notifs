import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { GOOGLE_PLAY_URL } from '../ntfyLinks';
import { screenshots } from '../screenshots';
import { strings } from '../strings';
import { AndroidSteps } from './AndroidSteps';
import { expectCopyButtonsCopy } from './copyAssertions';

const PROPS = {
  displayName: 'Chore Reaper',
  user: 'u_abcdefghijklmnop',
  personalTopic: 'chores4irl-abcdefghijklmnop-all',
  broadcastTopic: 'chores4irl-broadcast',
};

function renderSteps({ oneTap }: { oneTap: boolean }) {
  return render(<AndroidSteps {...PROPS} oneTap={oneTap} />);
}

function expectFourCopyFields() {
  return expectCopyButtonsCopy({
    [strings.labelServiceUrl]: 'https://notifs.4irl.app',
    [strings.labelUsername]: PROPS.user,
    [strings.labelPersonalTopic]: PROPS.personalTopic,
    [strings.labelBroadcastTopic]: PROPS.broadcastTopic,
  });
}

// True when `later` comes after `earlier` in document order.
function follows({ earlier, later }: { earlier: Node; later: Node }) {
  return Boolean(earlier.compareDocumentPosition(later) & Node.DOCUMENT_POSITION_FOLLOWING);
}

describe('AndroidSteps with one-tap', () => {
  it('renders four steps titled for the one-tap flow', () => {
    renderSteps({ oneTap: true });

    const steps = screen.getAllByRole('listitem');
    const titles = [
      strings.installNtfyTitle,
      strings.logInTitle,
      strings.androidOneTapTitle,
      strings.testItTitle,
    ];
    expect(steps).toHaveLength(titles.length);
    // Accessible name excludes the aria-hidden numeral, so this pins title and order.
    titles.forEach((title, index) => {
      expect(within(steps[index]).getByRole('heading', { level: 2, name: title })).toBeVisible();
    });
    expect(screen.getByRole('list')).toBeInTheDocument();
  });

  it('links to Google Play in a new tab with noopener noreferrer', () => {
    renderSteps({ oneTap: true });

    const link = screen.getByRole('link', { name: new RegExp(strings.googlePlayLink) });
    expect(link).toHaveAttribute('href', GOOGLE_PLAY_URL);
    expect(link).toHaveAttribute('target', '_blank');
    expect(link).toHaveAttribute('rel', 'noopener noreferrer');
  });

  it('renders both ntfy:// anchors with no target, primary then secondary', () => {
    const { container } = renderSteps({ oneTap: true });

    const personal = screen.getByRole('link', { name: strings.androidOneTapPersonal });
    const broadcast = screen.getByRole('link', { name: strings.androidOneTapBroadcast });
    expect(personal).toHaveAttribute('href', `ntfy://notifs.4irl.app/${PROPS.personalTopic}`);
    expect(broadcast).toHaveAttribute('href', `ntfy://notifs.4irl.app/${PROPS.broadcastTopic}`);
    expect(personal).not.toHaveAttribute('target');
    expect(broadcast).not.toHaveAttribute('target');
    // The broadcast anchor is the visually secondary one.
    expect(personal).not.toHaveClass('setup-page__primary--secondary');
    expect(broadcast).toHaveClass('setup-page__primary--secondary');
    expect(container.querySelectorAll('a[href^="ntfy://"]')).toHaveLength(2);
  });

  it('pairs each anchor with its own topic copy field, personal group first', () => {
    renderSteps({ oneTap: true });

    const personal = screen.getByRole('link', { name: strings.androidOneTapPersonal });
    const broadcast = screen.getByRole('link', { name: strings.androidOneTapBroadcast });
    const personalCopy = screen.getByRole('button', {
      name: strings.copyButtonLabel(strings.labelPersonalTopic),
    });
    const broadcastCopy = screen.getByRole('button', {
      name: strings.copyButtonLabel(strings.labelBroadcastTopic),
    });
    // anchor 1 < its copy field < anchor 2 < its copy field
    expect(follows({ earlier: personal, later: personalCopy })).toBe(true);
    expect(follows({ earlier: personalCopy, later: broadcast })).toBe(true);
    expect(follows({ earlier: broadcast, later: broadcastCopy })).toBe(true);
  });

  it('keeps all four copy buttons copying the exact values', async () => {
    renderSteps({ oneTap: true });

    await expectFourCopyFields();
  });

  it('shows both copy fallback prompts and not the on-your-phone note', () => {
    const { container } = renderSteps({ oneTap: true });

    expect(container).toHaveTextContent("Button didn't open ntfy? Tap + in ntfy and paste:");
    expect(container).toHaveTextContent(strings.androidFallbackBroadcast);
    expect(screen.queryByText(strings.androidCopyOnPhoneNote)).not.toBeInTheDocument();
  });
});

describe('AndroidSteps copy-only', () => {
  it('titles step 3 "Subscribe to your topics" with the on-your-phone note', () => {
    renderSteps({ oneTap: false });

    // The numeral badge is aria-hidden, so the accessible name is just the title.
    expect(screen.getByRole('heading', { name: strings.androidCopyTitle })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: strings.androidOneTapTitle })).toBeNull();
    expect(screen.getByText(strings.androidCopyOnPhoneNote)).toBeInTheDocument();
  });

  it('renders zero ntfy:// anchors', () => {
    const { container } = renderSteps({ oneTap: false });

    expect(container.querySelector('a[href^="ntfy://"]')).toBeNull();
    // Only the Google Play link remains.
    expect(screen.getAllByRole('link')).toHaveLength(1);
  });

  it('keeps all four copy buttons copying the exact values', async () => {
    renderSteps({ oneTap: false });

    await expectFourCopyFields();
  });
});

describe('AndroidSteps shared content', () => {
  it.each([true, false])('describes the password in text only (oneTap=%s)', (oneTap) => {
    const { container } = renderSteps({ oneTap });

    expect(screen.getByText(strings.passwordNote('Chore Reaper'))).toBeInTheDocument();
    expect(container.querySelector('input, textarea, form')).toBeNull();
    expect(container).toHaveTextContent('Back in Chore Reaper, tap Send test notification.');
  });

  it.each([true, false])(
    'names the real Android path: Settings → Manage users → Add new user (oneTap=%s)',
    (oneTap) => {
      const { container } = renderSteps({ oneTap });

      expect(container).toHaveTextContent('ntfy → Settings → Manage users → Add new user');
      expect(container).not.toHaveTextContent('Add user');
      expect(screen.getByText('Manage users').tagName).toBe('B');
    },
  );

  it.each([true, false])('renders the Add user screenshot in step 2 (oneTap=%s)', (oneTap) => {
    renderSteps({ oneTap });

    const steps = screen.getAllByRole('listitem');
    const shot = within(steps[1]).getByRole('img', { name: screenshots.androidAddUser.alt });
    expect(shot).toHaveAttribute('src', screenshots.androidAddUser.src);
    expect(screen.getAllByRole('img')).toHaveLength(1);
  });
});
