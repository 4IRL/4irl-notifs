import { act, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ChoiceMenu } from './ChoiceMenu';
import type { Choice } from './ChoiceMenu';

type Mode = 'all' | 'down' | 'paused';

const LABEL = 'Filter checks';
const CHOICES: Choice<Mode>[] = [
  { value: 'all', label: 'All' },
  { value: 'down', label: 'Down' },
  { value: 'paused', label: 'Paused' },
];

function Harness({
  value = 'all',
  onSelect = vi.fn(),
}: {
  value?: Mode;
  onSelect?: (value: Mode) => void;
}) {
  return (
    <div>
      <ChoiceMenu
        label={LABEL}
        choices={CHOICES}
        value={value}
        onSelect={onSelect}
        icon={<span aria-hidden="true">F</span>}
      />
      <button type="button">after</button>
    </div>
  );
}

function trigger(): HTMLElement {
  return screen.getByRole('button', { name: LABEL });
}

function item(name: string): HTMLElement {
  return screen.getByRole('menuitemradio', { name });
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('ChoiceMenu opening', () => {
  it('opens on click and focuses the current choice', async () => {
    const user = userEvent.setup();
    render(<Harness value="down" />);

    await user.click(trigger());

    expect(screen.getByRole('menu', { name: LABEL })).toBeInTheDocument();
    expect(trigger()).toHaveAttribute('aria-expanded', 'true');
    expect(item('Down')).toHaveFocus();
    expect(item('Down')).toHaveAttribute('aria-checked', 'true');
    expect(item('All')).toHaveAttribute('aria-checked', 'false');
  });

  it('ArrowDown on the trigger opens onto the current choice', async () => {
    const user = userEvent.setup();
    render(<Harness value="paused" />);

    trigger().focus();
    await user.keyboard('{ArrowDown}');

    expect(item('Paused')).toHaveFocus();
  });

  it('ArrowUp on the trigger opens onto the last item', async () => {
    const user = userEvent.setup();
    render(<Harness value="all" />);

    trigger().focus();
    await user.keyboard('{ArrowUp}');

    expect(item('Paused')).toHaveFocus();
  });

  it('ArrowUp/ArrowDown on the trigger of an open menu move focus back into it', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(trigger());
    trigger().focus();

    await user.keyboard('{ArrowUp}');
    expect(item('Paused')).toHaveFocus();

    trigger().focus();
    await user.keyboard('{ArrowDown}');
    expect(item('All')).toHaveFocus();
  });
});

describe('ChoiceMenu keyboard', () => {
  it('wraps with the arrow keys and jumps with Home and End', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(trigger());

    await user.keyboard('{ArrowDown}');
    expect(item('Down')).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(item('Paused')).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(item('All')).toHaveFocus();
    await user.keyboard('{ArrowUp}');
    expect(item('Paused')).toHaveFocus();
    await user.keyboard('{Home}');
    expect(item('All')).toHaveFocus();
    await user.keyboard('{End}');
    expect(item('Paused')).toHaveFocus();
  });

  it('Escape closes the menu and returns focus to the trigger', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(trigger());

    await user.keyboard('{Escape}');

    expect(screen.queryByRole('menu')).toBeNull();
    expect(trigger()).toHaveFocus();
  });

  it('Tab closes the menu and puts focus on the trigger, leaving the default Tab to proceed', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(trigger());

    const proceeds = fireEvent.keyDown(item('All'), { key: 'Tab' });

    expect(proceeds).toBe(true);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(trigger()).toHaveFocus();
  });
});

describe('ChoiceMenu dismissal and choosing', () => {
  it('closes on a press outside without stealing focus back', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(trigger());

    await user.click(screen.getByRole('button', { name: 'after' }));

    expect(screen.queryByRole('menu')).toBeNull();
    expect(screen.getByRole('button', { name: 'after' })).toHaveFocus();
  });

  it('closes when focus moves outside', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(trigger());

    act(() => {
      screen.getByRole('button', { name: 'after' }).focus();
    });

    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('choosing an item reports it, closes the menu and returns focus to the trigger', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<Harness onSelect={onSelect} />);
    await user.click(trigger());

    await user.click(item('Down'));

    expect(onSelect).toHaveBeenCalledExactlyOnceWith('down');
    expect(screen.queryByRole('menu')).toBeNull();
    expect(trigger()).toHaveFocus();
  });
});
