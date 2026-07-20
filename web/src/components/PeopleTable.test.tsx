import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { strings } from '../strings';
import { PeopleTable } from './PeopleTable';

afterEach(() => {
  cleanup();
});

describe('PeopleTable', () => {
  it('renders the people heading', () => {
    render(<PeopleTable people={[]} loading={false} error={null} />);

    expect(screen.getByRole('heading', { name: strings.peopleHeading })).toBeInTheDocument();
  });

  it('shows the loading copy and no table rows when loading', () => {
    render(<PeopleTable people={[]} loading={true} error={null} />);

    expect(screen.getByText(strings.peopleLoading)).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('shows the empty-state copy when there are no people', () => {
    render(<PeopleTable people={[]} loading={false} error={null} />);

    expect(screen.getByText(strings.peopleEmpty)).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('shows an alert with the error message when loading fails', () => {
    render(<PeopleTable people={[]} loading={false} error="Could not load people." />);

    expect(screen.getByRole('alert')).toHaveTextContent('Could not load people.');
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('renders a table with column headers and a row per person', () => {
    const people = [
      {
        personHash: '76gzqgp4byjl6dje',
        email: 'alice@example.com',
        createdAt: '2026-07-19T18:12:03Z',
      },
      {
        personHash: 'c2wpnf5d55mzzwrg',
        email: 'smoketest@example.com',
        createdAt: '2026-07-19T18:14:41Z',
      },
    ];

    render(<PeopleTable people={people} loading={false} error={null} />);

    expect(
      screen.getByRole('columnheader', { name: strings.columnPersonHash }),
    ).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: strings.columnEmail })).toBeInTheDocument();
    expect(screen.getByRole('columnheader', { name: strings.columnCreated })).toBeInTheDocument();

    const rows = screen.getAllByRole('row');
    // Header row + one row per person.
    expect(rows).toHaveLength(3);

    expect(
      screen.getByText('76gzqgp4byjl6dje', { selector: '.people-table__hash' }),
    ).toBeInTheDocument();
    expect(screen.getByText('alice@example.com')).toBeInTheDocument();
    expect(screen.getByText('2026-07-19T18:12:03Z')).toBeInTheDocument();
    expect(screen.getByText('smoketest@example.com')).toBeInTheDocument();
  });
});
