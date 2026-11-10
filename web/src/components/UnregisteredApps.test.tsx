import { cleanup, render, screen, within } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import type { HeldCounts } from '../heldCounts';
import { strings } from '../strings';
import { setCompactViewport } from '../test-viewport';
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

    const group = screen.getByRole('group', { name: strings.unregisteredAppsHeading });
    expect(within(group).getByRole('table')).toBeInTheDocument();

    const urlsRow = screen
      .getByText('urls4irl', { selector: '.unregistered-apps__id' })
      .closest('tr');
    expect(within(urlsRow!).getByText('2')).toBeInTheDocument();

    const taskRow = screen
      .getByText('tasktracker', { selector: '.unregistered-apps__id' })
      .closest('tr');
    expect(within(taskRow!).getByText('0')).toBeInTheDocument();
  });

  it('adds a Held column that takes the fill class, with the subscribers column losing it', () => {
    const held: HeldCounts = {
      status: 'ready',
      byApp: new Map([['ghost', 2]]),
      unassignedTotal: 0,
    };
    render(
      <UnregisteredApps
        appIds={['ghost']}
        subscriberCountByApp={new Map()}
        loading={false}
        held={held}
      />,
    );

    const subscribers = screen.getByRole('columnheader', { name: strings.columnSubscribers });
    const heldHeader = screen.getByRole('columnheader', { name: strings.columnHeld });
    expect(heldHeader).toHaveClass('admin-table__fill');
    expect(subscribers).not.toHaveClass('admin-table__fill');
    expect(screen.getByText('2 held')).toBeInTheDocument();
  });

  it('puts the fill class on the Held body cell, not the subscribers cell', () => {
    const held: HeldCounts = { status: 'ready', byApp: new Map(), unassignedTotal: 0 };
    render(
      <UnregisteredApps
        appIds={['ghost']}
        subscriberCountByApp={new Map()}
        loading={false}
        held={held}
      />,
    );
    const cells = screen.getByText('ghost').closest('tr')!.querySelectorAll('td');
    expect(cells).toHaveLength(3);
    expect(cells[1]).not.toHaveClass('admin-table__fill');
    expect(cells[2]).toHaveClass('admin-table__fill');
  });

  it('keeps the scroll group with the cell variant and no sub-line at compact width', () => {
    setCompactViewport(true);
    const held: HeldCounts = {
      status: 'ready',
      byApp: new Map([['ghost', 2]]),
      unassignedTotal: 0,
    };
    render(
      <UnregisteredApps
        appIds={['ghost']}
        subscriberCountByApp={new Map()}
        loading={false}
        held={held}
      />,
    );
    const group = screen.getByRole('group', { name: strings.unregisteredAppsHeading });
    expect(group).toHaveClass('admin-table__scroll');
    expect(
      within(group).getByRole('columnheader', { name: strings.columnHeld }),
    ).toBeInTheDocument();
    expect(within(group).getByText('2 held')).toBeInTheDocument();
    expect(screen.queryByText(strings.heldSubLabel, { exact: false })).not.toBeInTheDocument();
  });

  it('renders no Held column when held is undefined', () => {
    render(
      <UnregisteredApps appIds={['ghost']} subscriberCountByApp={new Map()} loading={false} />,
    );
    expect(
      screen.queryByRole('columnheader', { name: strings.columnHeld }),
    ).not.toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: strings.columnSubscribers })).toHaveClass(
      'admin-table__fill',
    );
  });

  describe('empty copy', () => {
    const readyHeld: HeldCounts = { status: 'ready', byApp: new Map(), unassignedTotal: 0 };

    function renderEmpty(props: { held?: HeldCounts; unionEvaluated?: boolean }) {
      render(
        <UnregisteredApps
          appIds={[]}
          subscriberCountByApp={new Map()}
          loading={false}
          {...props}
        />,
      );
    }

    it('claims no waiting messages when held is ready and the union was evaluated', () => {
      renderEmpty({ held: readyHeld, unionEvaluated: true });
      expect(screen.getByText(strings.unregisteredAppsEmptyHeldReady)).toBeInTheDocument();
    });

    it('stays claim-free when ready but unionEvaluated is false', () => {
      renderEmpty({ held: readyHeld, unionEvaluated: false });
      expect(screen.getByText(strings.unregisteredAppsEmpty)).toBeInTheDocument();
      expect(screen.queryByText(strings.unregisteredAppsEmptyHeldReady)).not.toBeInTheDocument();
    });

    it('stays claim-free when ready and unionEvaluated is omitted', () => {
      renderEmpty({ held: readyHeld });
      expect(screen.getByText(strings.unregisteredAppsEmpty)).toBeInTheDocument();
    });

    it('stays claim-free when unavailable even with unionEvaluated true', () => {
      renderEmpty({ held: { status: 'unavailable' }, unionEvaluated: true });
      expect(screen.getByText(strings.unregisteredAppsEmpty)).toBeInTheDocument();
    });

    it('stays claim-free when held is loading', () => {
      renderEmpty({ held: { status: 'loading' }, unionEvaluated: true });
      expect(screen.getByText(strings.unregisteredAppsEmpty)).toBeInTheDocument();
    });

    it('stays claim-free when held is undefined', () => {
      renderEmpty({ unionEvaluated: true });
      expect(screen.getByText(strings.unregisteredAppsEmpty)).toBeInTheDocument();
    });
  });
});
