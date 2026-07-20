import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { strings } from '../strings';
import { UsersTable } from './UsersTable';

afterEach(() => {
  cleanup();
});

describe('UsersTable', () => {
  it('renders the users heading', () => {
    render(
      <UsersTable
        users={[]}
        loading={false}
        emailByPersonHash={new Map()}
        onDeprovision={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    expect(screen.getByRole('heading', { name: strings.usersHeading })).toBeInTheDocument();
  });

  it('shows the loading copy and no table rows when loading', () => {
    render(
      <UsersTable
        users={[]}
        loading={true}
        emailByPersonHash={new Map()}
        onDeprovision={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    expect(screen.getByText(strings.usersLoading)).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('shows the empty-state copy and no table body rows when there are no users', () => {
    render(
      <UsersTable
        users={[]}
        loading={false}
        emailByPersonHash={new Map()}
        onDeprovision={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    expect(screen.getByText(strings.usersEmpty)).toBeInTheDocument();
    expect(screen.queryAllByRole('row')).toHaveLength(0);
  });

  it('renders a table with column headers and a row per user', () => {
    const users = [
      {
        userId: 'alice',
        apps: ['urls4irl', 'chores4irl'],
        topicPatterns: ['u4i-alice', 'c4i-alice'],
      },
      { userId: 'bob', apps: ['urls4irl'], topicPatterns: ['u4i-bob'] },
    ];

    render(
      <UsersTable
        users={users}
        loading={false}
        emailByPersonHash={new Map()}
        onDeprovision={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    expect(screen.getByRole('columnheader', { name: strings.columnUser })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: strings.columnApps })).toBeInTheDocument();
    expect(
      screen.getByRole('columnheader', { name: strings.columnTopicPatterns }),
    ).toBeInTheDocument();

    const rows = screen.getAllByRole('row');
    // Header row + one row per user.
    expect(rows).toHaveLength(3);

    expect(screen.getByText('alice')).toBeInTheDocument();
    expect(screen.getByText('bob')).toBeInTheDocument();

    expect(screen.getAllByText('urls4irl', { selector: '.users-table__chip' })).toHaveLength(2);
    expect(screen.getByText('chores4irl', { selector: '.users-table__chip' })).toBeInTheDocument();

    expect(screen.getByText('u4i-alice, c4i-alice')).toBeInTheDocument();
    expect(screen.getByText('u4i-bob')).toBeInTheDocument();
  });

  it('shows the email for a user whose personHash resolves via emailByPersonHash', () => {
    const users = [
      {
        userId: 'u_76gzqgp4byjl6dje',
        apps: ['urls4irl'],
        topicPatterns: ['urls4irl-76gzqgp4byjl6dje-*'],
      },
    ];
    const emailByPersonHash = new Map([['76gzqgp4byjl6dje', 'alice@example.com']]);

    render(
      <UsersTable
        users={users}
        loading={false}
        emailByPersonHash={emailByPersonHash}
        onDeprovision={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    expect(screen.getByText('alice@example.com')).toBeInTheDocument();
    expect(screen.queryByText('u_76gzqgp4byjl6dje')).not.toBeInTheDocument();
  });

  it('falls back to the raw userId when the personHash has no entry in emailByPersonHash', () => {
    const users = [{ userId: 'u_missinghash1234', apps: [], topicPatterns: [] }];

    render(
      <UsersTable
        users={users}
        loading={false}
        emailByPersonHash={new Map()}
        onDeprovision={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    expect(screen.getByText('u_missinghash1234')).toBeInTheDocument();
  });

  it('falls back to the raw userId for a publisher identity with no u_ prefix', () => {
    const users = [{ userId: 'urls4irl-publisher', apps: [], topicPatterns: [] }];

    render(
      <UsersTable
        users={users}
        loading={false}
        emailByPersonHash={new Map([['publisher', 'should-not-match@example.com']])}
        onDeprovision={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    expect(screen.getByText('urls4irl-publisher')).toBeInTheDocument();
  });

  it('calls onDeprovision with the single app when the user belongs to exactly one app', async () => {
    const user = userEvent.setup();
    const onDeprovision = vi.fn();
    const users = [{ userId: 'bob', apps: ['urls4irl'], topicPatterns: ['u4i-bob'] }];

    render(
      <UsersTable
        users={users}
        loading={false}
        emailByPersonHash={new Map()}
        onDeprovision={onDeprovision}
        onDelete={vi.fn()}
      />,
    );

    await user.click(screen.getByRole('button', { name: `${strings.deprovisionAction} urls4irl` }));

    expect(onDeprovision).toHaveBeenCalledTimes(1);
    expect(onDeprovision).toHaveBeenCalledWith({ userId: 'bob', appId: 'urls4irl' });
  });

  it('renders one Deprovision button per app for a multi-app user, each calling onDeprovision for its app', async () => {
    const user = userEvent.setup();
    const onDeprovision = vi.fn();
    const users = [
      {
        userId: 'alice',
        apps: ['urls4irl', 'chores4irl'],
        topicPatterns: ['u4i-alice', 'c4i-alice'],
      },
    ];

    render(
      <UsersTable
        users={users}
        loading={false}
        emailByPersonHash={new Map()}
        onDeprovision={onDeprovision}
        onDelete={vi.fn()}
      />,
    );

    await user.click(screen.getByRole('button', { name: `${strings.deprovisionAction} urls4irl` }));
    await user.click(
      screen.getByRole('button', { name: `${strings.deprovisionAction} chores4irl` }),
    );

    expect(onDeprovision).toHaveBeenCalledTimes(2);
    expect(onDeprovision).toHaveBeenNthCalledWith(1, { userId: 'alice', appId: 'urls4irl' });
    expect(onDeprovision).toHaveBeenNthCalledWith(2, { userId: 'alice', appId: 'chores4irl' });
  });

  it('renders no Deprovision button for a user with zero apps', () => {
    const users = [{ userId: 'carol', apps: [], topicPatterns: [] }];

    render(
      <UsersTable
        users={users}
        loading={false}
        emailByPersonHash={new Map()}
        onDeprovision={vi.fn()}
        onDelete={vi.fn()}
      />,
    );

    expect(
      screen.queryByRole('button', { name: new RegExp(`^${strings.deprovisionAction}`) }),
    ).not.toBeInTheDocument();
  });

  it('calls onDelete with the userId when the Delete button is clicked', async () => {
    const user = userEvent.setup();
    const onDelete = vi.fn();
    const users = [{ userId: 'bob', apps: ['urls4irl'], topicPatterns: ['u4i-bob'] }];

    render(
      <UsersTable
        users={users}
        loading={false}
        emailByPersonHash={new Map()}
        onDeprovision={vi.fn()}
        onDelete={onDelete}
      />,
    );

    await user.click(screen.getByRole('button', { name: `${strings.deleteAction} bob` }));

    expect(onDelete).toHaveBeenCalledTimes(1);
    expect(onDelete).toHaveBeenCalledWith({ userId: 'bob' });
  });

  it('distinguishes delete buttons for different users by accessible name', async () => {
    const user = userEvent.setup();
    const onDelete = vi.fn();
    const users = [
      { userId: 'alice', apps: ['urls4irl'], topicPatterns: ['u4i-alice'] },
      { userId: 'bob', apps: ['urls4irl'], topicPatterns: ['u4i-bob'] },
    ];

    render(
      <UsersTable
        users={users}
        loading={false}
        emailByPersonHash={new Map()}
        onDeprovision={vi.fn()}
        onDelete={onDelete}
      />,
    );

    const aliceDelete = screen.getByRole('button', { name: `${strings.deleteAction} alice` });
    const bobDelete = screen.getByRole('button', { name: `${strings.deleteAction} bob` });

    expect(aliceDelete).not.toBe(bobDelete);

    await user.click(bobDelete);

    expect(onDelete).toHaveBeenCalledTimes(1);
    expect(onDelete).toHaveBeenCalledWith({ userId: 'bob' });
  });

  it('uses the resolved email in the Delete button accessible name so the hash does not leak', async () => {
    const user = userEvent.setup();
    const onDelete = vi.fn();
    const users = [{ userId: 'u_76gzqgp4byjl6dje', apps: [], topicPatterns: [] }];
    const emailByPersonHash = new Map([['76gzqgp4byjl6dje', 'alice@example.com']]);

    render(
      <UsersTable
        users={users}
        loading={false}
        emailByPersonHash={emailByPersonHash}
        onDeprovision={vi.fn()}
        onDelete={onDelete}
      />,
    );

    await user.click(
      screen.getByRole('button', { name: `${strings.deleteAction} alice@example.com` }),
    );

    expect(onDelete).toHaveBeenCalledTimes(1);
    expect(onDelete).toHaveBeenCalledWith({ userId: 'u_76gzqgp4byjl6dje' });
  });
});
