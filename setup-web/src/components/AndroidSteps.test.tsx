import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';

import { screenshots } from '../screenshots';
import { strings } from '../strings';
import { AndroidSteps } from './AndroidSteps';

const PROPS = {
  displayName: 'Chore Reaper',
  user: 'u_abcdefghijklmnop',
  personalTopic: 'chores4irl-abcdefghijklmnop-all',
  broadcastTopic: 'chores4irl-broadcast',
};

const RETURN_URL = 'https://chores.4irl.app/home';

function renderSteps({ oneTap, returnUrl = null }: { oneTap: boolean; returnUrl?: string | null }) {
  return render(<AndroidSteps {...PROPS} oneTap={oneTap} returnUrl={returnUrl} />);
}

// Step 1 and step 2 both carry a "Service URL" field, so lookups for step 2's field go
// through the step heading, never a page-wide label query.
function stepTwo() {
  const heading = screen.getByRole('heading', {
    level: 2,
    name: new RegExp(`^(${strings.androidOneTapTitle}|${strings.androidCopyTitle})$`),
  });
  const step = heading.closest('li');
  if (!step) throw new Error('step 2 heading is not inside a list item');
  return step;
}

function serviceUrlButtons(step: HTMLElement) {
  return within(step).queryAllByRole('button', {
    name: strings.copyButtonLabel(strings.labelServiceUrl),
  });
}

async function expectStepTwoServiceUrlCopies({
  user,
}: {
  user: ReturnType<typeof userEvent.setup>;
}) {
  await user.click(serviceUrlButtons(stepTwo())[0]);
  expect(await navigator.clipboard.readText()).toBe('https://notifs.4irl.app');
}

// Service URL is the one label that appears twice (step 1 and step 2), so a label-keyed
// map cannot cover it; the other copy buttons are checked here and step 2's Service URL
// separately through expectStepTwoServiceUrlCopies.
async function expectCopyFields() {
  expect(screen.getAllByRole('button')).toHaveLength(5);
  const user = userEvent.setup();
  await expectStepTwoServiceUrlCopies({ user });
  const expected: Record<string, string> = {
    [strings.labelUsername]: PROPS.user,
    [strings.labelPersonalTopic]: PROPS.personalTopic,
    [strings.labelBroadcastTopic]: PROPS.broadcastTopic,
  };
  for (const [label, value] of Object.entries(expected)) {
    await user.click(screen.getByRole('button', { name: strings.copyButtonLabel(label) }));
    expect(await navigator.clipboard.readText()).toBe(value);
  }
  await user.click(
    within(screen.getAllByRole('listitem')[0]).getByRole('button', {
      name: strings.copyButtonLabel(strings.labelServiceUrl),
    }),
  );
  expect(await navigator.clipboard.readText()).toBe('https://notifs.4irl.app');
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

  it('renders two same-tab return links named after the app alongside the ntfy anchors', () => {
    renderSteps({ oneTap: true, returnUrl: RETURN_URL });

    const links = screen.getAllByRole('link', { name: 'Chore Reaper' });
    expect(links).toHaveLength(2);
    links.forEach((link) => {
      expect(link).toHaveAttribute('href', RETURN_URL);
      expect(link).not.toHaveAttribute('target');
    });
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

  it('keeps all five copy buttons copying the exact values', async () => {
    renderSteps({ oneTap: true });

    await expectCopyFields();
  });

  it('shows exactly one Service URL field in step 2, in the first fallback only', () => {
    renderSteps({ oneTap: true });

    const step = stepTwo();
    expect(serviceUrlButtons(step)).toHaveLength(1);
    const serviceUrl = serviceUrlButtons(step)[0];
    const personalCopy = within(step).getByRole('button', {
      name: strings.copyButtonLabel(strings.labelPersonalTopic),
    });
    const broadcastAnchor = within(step).getByRole('link', {
      name: strings.androidOneTapBroadcast,
    });
    // It sits before the personal topic field, so before the second fallback.
    expect(follows({ earlier: serviceUrl, later: personalCopy })).toBe(true);
    expect(follows({ earlier: serviceUrl, later: broadcastAnchor })).toBe(true);
  });

  it('names Use another server in both fallbacks, the second as the same server address', () => {
    const { container } = renderSteps({ oneTap: true });

    expect(container).toHaveTextContent(
      "Button didn't open ntfy? Tap + in ntfy, turn on Use another server, paste the server address, then paste:",
    );
    const second = screen.getByText(strings.androidFallbackBroadcastEnd, { exact: false });
    expect(second).toHaveTextContent('Or tap + again, turn on Use another server');
    expect(second).toHaveTextContent('paste the same server address');
    // The second fallback is followed by the broadcast topic field, not a Service URL field.
    const next = second.nextElementSibling;
    expect(next).not.toBeNull();
    expect(within(next as HTMLElement).queryByText(strings.labelServiceUrl)).toBeNull();
    expect(within(next as HTMLElement).getByText(strings.labelBroadcastTopic)).toBeVisible();
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

  it('renders exactly two return links named after the app with a returnUrl', () => {
    renderSteps({ oneTap: false, returnUrl: RETURN_URL });

    const links = screen.getAllByRole('link');
    expect(links).toHaveLength(2);
    links.forEach((link) => {
      expect(link).toHaveAccessibleName('Chore Reaper');
      expect(link).toHaveAttribute('href', RETURN_URL);
      expect(link).not.toHaveAttribute('target');
    });
  });

  it('keeps all five copy buttons copying the exact values', async () => {
    renderSteps({ oneTap: false });

    await expectCopyFields();
  });

  it('tells the person to turn on Use another server and offers the server before the topics', () => {
    const { container } = renderSteps({ oneTap: false });

    expect(container).toHaveTextContent(
      'In ntfy, tap +, turn on Use another server, paste the server address, then paste each topic:',
    );
    const step = stepTwo();
    expect(serviceUrlButtons(step)).toHaveLength(1);
    const personalCopy = within(step).getByRole('button', {
      name: strings.copyButtonLabel(strings.labelPersonalTopic),
    });
    expect(follows({ earlier: serviceUrlButtons(step)[0], later: personalCopy })).toBe(true);
    expect(screen.getByText('Use another server').tagName).toBe('B');
  });
});

describe('AndroidSteps shared content', () => {
  it.each([true, false])('describes the password in text only (oneTap=%s)', (oneTap) => {
    const { container } = renderSteps({ oneTap });

    expect(container).toHaveTextContent(
      "Password: the notification password you set in Chore Reaper. It's never shown on this page.",
    );
    expect(container.querySelector('.setup-page__app-link')).toBeNull();
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
