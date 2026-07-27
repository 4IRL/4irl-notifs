import { useState } from 'react';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import type { AppSummary } from '../api/personClient';
import { strings } from '../strings';
import { AppCombobox } from './AppCombobox';

afterEach(() => {
  cleanup();
});

const registeredApps: AppSummary[] = [
  {
    appId: 'urls4irl',
    displayName: 'URLs4IRL',
    description: null,
    createdAt: '2026-07-25T10:00:00Z',
  },
  {
    appId: 'tasktracker',
    displayName: 'Task Tracker',
    description: null,
    createdAt: '2026-07-25T10:00:00Z',
  },
];

/** Controlled wrapper: AppCombobox is a controlled component, so the test owns
 *  the value and can observe every onChange via onValue. */
function Harness({
  apps = registeredApps,
  onValue,
}: {
  apps?: AppSummary[];
  onValue?: (value: string) => void;
}) {
  const [value, setValue] = useState('');
  return (
    <AppCombobox
      id="app-id"
      value={value}
      onChange={(next) => {
        setValue(next);
        onValue?.(next);
      }}
      apps={apps}
      placeholder={strings.appIdPlaceholder}
    />
  );
}

function input() {
  return screen.getByRole('combobox') as HTMLInputElement;
}

describe('AppCombobox', () => {
  it('filters registered apps as you type', async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(input(), 'task');

    expect(screen.getByRole('option', { name: /tasktracker/i })).toBeInTheDocument();
    expect(screen.queryByRole('option', { name: /urls4irl/i })).not.toBeInTheDocument();
  });

  it('selecting a registered option sets its app_id and closes the popover', async () => {
    const user = userEvent.setup();
    const onValue = vi.fn();
    render(<Harness onValue={onValue} />);

    await user.type(input(), 'url');
    fireEvent.mouseDown(screen.getByRole('option', { name: /urls4irl/i }));

    expect(input().value).toBe('urls4irl');
    expect(onValue).toHaveBeenLastCalledWith('urls4irl');
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  });

  it('offers "use unprovisioned app" for a valid unregistered id and selects the typed value', async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(input(), 'newapp');
    fireEvent.mouseDown(
      screen.getByRole('option', { name: strings.useUnprovisionedApp({ value: 'newapp' }) }),
    );

    expect(input().value).toBe('newapp');
  });

  it('does not offer "use unprovisioned app" for an invalid app_id', async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(input(), 'Bad-Id');

    expect(
      screen.queryByText(strings.useUnprovisionedApp({ value: 'Bad-Id' })),
    ).not.toBeInTheDocument();
  });

  it('does not offer "use unprovisioned app" for the reserved id "everyone"', async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(input(), 'everyone');

    expect(
      screen.queryByText(strings.useUnprovisionedApp({ value: 'everyone' })),
    ).not.toBeInTheDocument();
  });

  it('does not offer "use unprovisioned app" when the typed id is an exact registered app', async () => {
    const user = userEvent.setup();
    render(<Harness />);

    await user.type(input(), 'urls4irl');

    expect(screen.getByRole('option', { name: /urls4irl/i })).toBeInTheDocument();
    expect(
      screen.queryByText(strings.useUnprovisionedApp({ value: 'urls4irl' })),
    ).not.toBeInTheDocument();
  });

  it('works as free text when there are no registered apps', async () => {
    const user = userEvent.setup();
    render(<Harness apps={[]} />);

    await user.type(input(), 'freeapp');

    expect(input().value).toBe('freeapp');
    expect(
      screen.getByRole('option', { name: strings.useUnprovisionedApp({ value: 'freeapp' }) }),
    ).toBeInTheDocument();
  });

  it('ArrowDown moves the highlight through the options and wraps around', () => {
    render(<Harness />);
    const el = input();
    fireEvent.focus(el); // empty query → both registered apps listed

    fireEvent.keyDown(el, { key: 'ArrowDown' });
    expect(screen.getByRole('option', { name: /urls4irl/i })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    fireEvent.keyDown(el, { key: 'ArrowDown' });
    expect(screen.getByRole('option', { name: /tasktracker/i })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    fireEvent.keyDown(el, { key: 'ArrowDown' });
    expect(screen.getByRole('option', { name: /urls4irl/i })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('ArrowUp wraps from the first option to the last', () => {
    render(<Harness />);
    const el = input();
    fireEvent.focus(el);

    fireEvent.keyDown(el, { key: 'ArrowDown' }); // urls4irl (index 0)
    fireEvent.keyDown(el, { key: 'ArrowUp' }); // wraps to tasktracker (last)
    expect(screen.getByRole('option', { name: /tasktracker/i })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('Enter commits the highlighted option and closes the popover', () => {
    render(<Harness />);
    const el = input();
    fireEvent.focus(el);

    fireEvent.keyDown(el, { key: 'ArrowDown' }); // highlight urls4irl
    fireEvent.keyDown(el, { key: 'Enter' });

    expect(el.value).toBe('urls4irl');
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  });

  it('Enter with no highlighted option selects nothing (lets the form submit the typed value)', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const el = input();
    await user.type(el, 'newapp'); // typing resets highlight to -1

    fireEvent.keyDown(el, { key: 'Enter' });

    expect(el.value).toBe('newapp');
  });

  it('Escape closes the popover and keeps the typed value', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const el = input();
    await user.type(el, 'ur');
    expect(screen.getByRole('listbox')).toBeInTheDocument();

    fireEvent.keyDown(el, { key: 'Escape' });

    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    expect(el.value).toBe('ur');
  });

  it('closes the popover on blur without selecting anything', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const el = input();
    await user.type(el, 'ur');
    expect(screen.getByRole('listbox')).toBeInTheDocument();

    fireEvent.blur(el);

    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    expect(el.value).toBe('ur');
  });

  it('does not throw or mis-commit when the apps list shrinks past the highlighted index', () => {
    // Regression: an app removed via the sibling Apps table while this popover
    // is open (focus intact) shrinks `options`; a stale highlightedIndex must be
    // clamped so Enter never commits options[stale] === undefined.
    const alpha: AppSummary = {
      appId: 'alpha',
      displayName: 'Alpha',
      description: null,
      createdAt: '2026-07-25T10:00:00Z',
    };
    const alphabet: AppSummary = {
      appId: 'alphabet',
      displayName: 'Alphabet',
      description: null,
      createdAt: '2026-07-25T10:00:00Z',
    };
    const onChange = vi.fn();
    const { rerender } = render(
      <AppCombobox id="app-id" value="alpha" onChange={onChange} apps={[alpha, alphabet]} />,
    );
    const el = input();
    fireEvent.focus(el);
    fireEvent.keyDown(el, { key: 'ArrowDown' }); // index 0
    fireEvent.keyDown(el, { key: 'ArrowDown' }); // index 1 (last)

    // Registry shrinks (no blur — popover stays open); highlightedIndex is now stale.
    rerender(<AppCombobox id="app-id" value="alpha" onChange={onChange} apps={[alpha]} />);

    expect(() => fireEvent.keyDown(el, { key: 'Enter' })).not.toThrow();
    expect(onChange).not.toHaveBeenCalled();
  });
});
