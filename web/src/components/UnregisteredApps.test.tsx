import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { strings } from '../strings';
import { UnregisteredApps } from './UnregisteredApps';

afterEach(() => {
  cleanup();
});

describe('UnregisteredApps', () => {
  it('renders the heading and instructional hint', () => {
    render(<UnregisteredApps appIds={[]} subscriberCountByApp={new Map()} loading={false} />);

    expect(
      screen.getByRole('heading', { name: strings.unregisteredAppsHeading }),
    ).toBeInTheDocument();
    expect(screen.getByText(strings.unregisteredAppsHint)).toBeInTheDocument();
  });

  it('shows the empty state and no table when there are none', () => {
    render(<UnregisteredApps appIds={[]} subscriberCountByApp={new Map()} loading={false} />);

    expect(screen.getByText(strings.unregisteredAppsEmpty)).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('shows the loading state (not the empty state) while loading', () => {
    render(<UnregisteredApps appIds={[]} subscriberCountByApp={new Map()} loading={true} />);

    expect(screen.getByText(strings.unregisteredAppsLoading)).toBeInTheDocument();
    expect(screen.queryByText(strings.unregisteredAppsEmpty)).not.toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('lists each unprovisioned app_id with its subscriber count (defaulting to 0)', () => {
    render(
      <UnregisteredApps
        appIds={['urls4irl', 'tasktracker']}
        subscriberCountByApp={new Map([['urls4irl', 2]])}
        loading={false}
      />,
    );

    const urlsRow = screen
      .getByText('urls4irl', { selector: '.unregistered-apps__id' })
      .closest('tr');
    expect(within(urlsRow!).getByText('2')).toBeInTheDocument();

    const taskRow = screen
      .getByText('tasktracker', { selector: '.unregistered-apps__id' })
      .closest('tr');
    expect(within(taskRow!).getByText('0')).toBeInTheDocument();
  });
});
