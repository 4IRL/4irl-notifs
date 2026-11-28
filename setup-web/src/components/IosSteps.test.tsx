import { render, screen, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { screenshots } from '../screenshots';
import { strings } from '../strings';
import { expectCopyButtonsCopy } from './copyAssertions';
import { IosSteps } from './IosSteps';

const PROPS = {
  displayName: 'Chore Reaper',
  user: 'u_abcdefghijklmnop',
  personalTopic: 'chores4irl-abcdefghijklmnop-all',
  broadcastTopic: 'chores4irl-broadcast',
};

const RETURN_URL = 'https://chores.4irl.app/home';

function renderSteps({ returnUrl = null }: { returnUrl?: string | null } = {}) {
  return render(<IosSteps {...PROPS} returnUrl={returnUrl} />);
}

describe('IosSteps', () => {
  it('renders the five steps in order with their headings', () => {
    renderSteps();

    const steps = screen.getAllByRole('listitem');
    const titles = [
      strings.iosServerTitle,
      strings.logInTitle,
      strings.iosPersonalTitle,
      strings.iosBroadcastTitle,
      strings.testItTitle,
    ];
    expect(steps).toHaveLength(titles.length);
    // Accessible name excludes the aria-hidden numeral, so this pins title and order.
    titles.forEach((title, index) => {
      expect(within(steps[index]).getByRole('heading', { level: 2, name: title })).toBeVisible();
      // The visible numeral is aria-hidden, so assert it separately.
      expect(steps[index].querySelector('.setup-page__num')).toHaveTextContent(String(index + 1));
    });
    expect(screen.getByRole('list')).toBeInTheDocument();
  });

  it('renders no links without a returnUrl: the install card owns the store downloads', () => {
    renderSteps({ returnUrl: null });

    expect(screen.queryByRole('link')).not.toBeInTheDocument();
  });

  it('renders exactly two same-tab return links named after the app with a returnUrl', () => {
    renderSteps({ returnUrl: RETURN_URL });

    const links = screen.getAllByRole('link');
    expect(links).toHaveLength(2);
    links.forEach((link) => {
      expect(link).toHaveAccessibleName('Chore Reaper');
      expect(link).toHaveAttribute('href', RETURN_URL);
      expect(link).not.toHaveAttribute('target');
      expect(link).toHaveAttribute('rel', 'noreferrer');
      expect(link).toHaveClass('setup-page__app-link');
    });
  });

  it('renders exactly four copy buttons that copy the exact derived values', async () => {
    renderSteps();

    await expectCopyButtonsCopy({
      [strings.labelDefaultServer]: 'https://notifs.4irl.app',
      [strings.labelUsername]: PROPS.user,
      [strings.labelPersonalTopic]: PROPS.personalTopic,
      [strings.labelBroadcastTopic]: PROPS.broadcastTopic,
    });
  });

  it('never mentions Use another server: iPhone sets the default server in step 1', () => {
    const { container } = renderSteps();

    expect(container).not.toHaveTextContent('Use another server');
  });

  it('warns to paste the server exactly with no trailing slash', () => {
    renderSteps();

    expect(screen.getByText(strings.iosServerWarning)).toBeInTheDocument();
  });

  it('names the real iOS paths: Default server then Settings → Users → Add user', () => {
    const { container } = renderSteps();

    expect(container).toHaveTextContent(
      'ntfy → Settings → Default server → paste this address, then tap Save:',
    );
    expect(container).toHaveTextContent('Settings → Users → Add user.');
    expect(container).not.toHaveTextContent('Manage users');
  });

  it('describes the password in text and names the app in the note and the test step', () => {
    const { container } = renderSteps();

    expect(container).toHaveTextContent(
      "Password: the notification password you set in Chore Reaper. It's never shown on this page.",
    );
    expect(container).toHaveTextContent('Back in Chore Reaper, tap Send test notification.');
  });

  it('keeps the same visible text with a returnUrl', () => {
    const { container } = renderSteps({ returnUrl: RETURN_URL });

    expect(container).toHaveTextContent(
      "Password: the notification password you set in Chore Reaper. It's never shown on this page.",
    );
    expect(container).toHaveTextContent('Back in Chore Reaper, tap Send test notification.');
  });

  it('never renders an ntfy:// anchor, form control or form', () => {
    const { container } = renderSteps();

    expect(container.querySelector('a[href^="ntfy://"]')).toBeNull();
    expect(container.querySelector('input, textarea, form')).toBeNull();
  });

  it('renders the two iPhone screenshots in steps 1 and 2 with distinct alt text and sizes', () => {
    renderSteps();

    const steps = screen.getAllByRole('listitem');
    const serverShot = within(steps[0]).getByRole('img', {
      name: screenshots.iosDefaultServer.alt,
    });
    const userShot = within(steps[1]).getByRole('img', { name: screenshots.iosAddUser.alt });
    expect(screen.getAllByRole('img')).toHaveLength(2);
    expect(serverShot).toHaveAttribute('src', screenshots.iosDefaultServer.src);
    expect(userShot).toHaveAttribute('src', screenshots.iosAddUser.src);
    expect(serverShot.getAttribute('alt')).not.toBe(userShot.getAttribute('alt'));
  });
});
