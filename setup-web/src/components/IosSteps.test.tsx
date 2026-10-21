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

function renderSteps() {
  return render(<IosSteps {...PROPS} />);
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
    });
    expect(screen.getByRole('list')).toBeInTheDocument();
  });

  it('renders no links: the install card owns the store downloads', () => {
    renderSteps();

    expect(screen.queryByRole('link')).not.toBeInTheDocument();
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

    expect(screen.getByText(strings.passwordNote('Chore Reaper'))).toBeInTheDocument();
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
