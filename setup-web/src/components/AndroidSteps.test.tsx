import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

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
  it('renders three steps titled for the one-tap flow', () => {
    renderSteps({ oneTap: true });

    const steps = screen.getAllByRole('listitem');
    const titles = [strings.logInTitle, strings.androidOneTapTitle, strings.testItTitle];
    expect(steps).toHaveLength(titles.length);
    // Accessible name excludes the aria-hidden numeral, so this pins title and order.
    titles.forEach((title, index) => {
      expect(within(steps[index]).getByRole('heading', { level: 2, name: title })).toBeVisible();
      // The visible numeral is aria-hidden, so assert it separately.
      expect(steps[index].querySelector('.setup-page__num')).toHaveTextContent(String(index + 1));
    });
    expect(screen.getByRole('list')).toBeInTheDocument();
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
  it('titles step 2 "Subscribe to your topics" with the on-your-phone note', () => {
    renderSteps({ oneTap: false });

    const steps = screen.getAllByRole('listitem');
    const titles = [strings.logInTitle, strings.androidCopyTitle, strings.testItTitle];
    expect(steps).toHaveLength(titles.length);
    titles.forEach((title, index) => {
      expect(within(steps[index]).getByRole('heading', { level: 2, name: title })).toBeVisible();
      expect(steps[index].querySelector('.setup-page__num')).toHaveTextContent(String(index + 1));
    });

    // The numeral badge is aria-hidden, so the accessible name is just the title.
    expect(screen.getByRole('heading', { name: strings.androidCopyTitle })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: strings.androidOneTapTitle })).toBeNull();
    expect(screen.getByText(strings.androidCopyOnPhoneNote)).toBeInTheDocument();
  });

  it('renders zero ntfy:// anchors', () => {
    const { container } = renderSteps({ oneTap: false });

    expect(container.querySelector('a[href^="ntfy://"]')).toBeNull();
    // No store links either: the install card owns those.
    expect(screen.queryAllByRole('link')).toHaveLength(0);
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

  it.each([true, false])('renders the Add user screenshot in step 1 (oneTap=%s)', (oneTap) => {
    renderSteps({ oneTap });

    const steps = screen.getAllByRole('listitem');
    const shot = within(steps[0]).getByRole('img', { name: screenshots.androidAddUser.alt });
    expect(shot).toHaveAttribute('src', screenshots.androidAddUser.src);
    expect(screen.getAllByRole('img')).toHaveLength(1);
  });
});
