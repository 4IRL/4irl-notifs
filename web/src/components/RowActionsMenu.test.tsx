import { act, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { RowActionsMenu } from './RowActionsMenu';
import type { RowMenuItem } from './RowActionsMenu';

const ROW = 'urls4irl/nightly';
const KEBAB = `Actions for ${ROW}`;

function buildItems(overrides: { select?: () => void; confirm?: () => void } = {}): RowMenuItem[] {
  return [
    { id: 'edit', label: 'Edit', onSelect: overrides.select ?? vi.fn() },
    { id: 'pause', label: 'Pause', disabled: true, onSelect: vi.fn() },
    {
      id: 'delete',
      label: 'Delete urls4irl/nightly',
      tone: 'danger',
      confirm: {
        confirmLabel: 'Confirm delete',
        confirmAriaLabel: 'Confirm delete urls4irl/nightly',
        cancelLabel: 'Cancel',
        cancelAriaLabel: 'Cancel delete urls4irl/nightly',
        notice: 'This cannot be undone.',
        dialogTitle: 'Delete nightly?',
        dialogItem: { label: 'Route', value: 'urls4irl/nightly' },
        onConfirm: overrides.confirm ?? vi.fn(),
      },
    },
  ];
}

interface HarnessProps {
  items?: RowMenuItem[];
  note?: string | null;
  error?: string;
  registerControl?: (params: { key: string; element: HTMLElement | null }) => void;
  rowLabel?: string;
}

function Harness({
  items = buildItems(),
  note = null,
  error,
  registerControl = vi.fn(),
  rowLabel = ROW,
}: HarnessProps) {
  return (
    <div>
      <div data-testid="scroller" style={{ overflow: 'auto' }}>
        <table>
          <tbody>
            <tr>
              <td>
                <RowActionsMenu
                  rowLabel={rowLabel}
                  items={items}
                  note={note}
                  errors={error === undefined ? undefined : [error]}
                  focusKeys={['edit:1', 'pause:1']}
                  registerControl={registerControl}
                />
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <button type="button">after</button>
    </div>
  );
}

function kebab(): HTMLElement {
  return screen.getByRole('button', { name: KEBAB });
}

afterEach(() => {
  vi.restoreAllMocks();
});

/** jsdom does not scroll: set the offsets a real scroll would have left, then fire the event. */
function scrollTo({
  element,
  left = 0,
  top = 0,
}: {
  element: HTMLElement;
  left?: number;
  top?: number;
}) {
  Object.defineProperty(element, 'scrollLeft', { value: left, configurable: true });
  Object.defineProperty(element, 'scrollTop', { value: top, configurable: true });
  fireEvent.scroll(element);
}

describe('RowActionsMenu kebab', () => {
  it('is a labelled menu button and renders no menu while closed', () => {
    render(<Harness />);

    const trigger = kebab();
    expect(trigger).toHaveAttribute('aria-haspopup', 'menu');
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    expect(trigger).toHaveAttribute('aria-controls');
    expect(screen.queryByRole('menu')).toBeNull();
    // The cell holds the kebab and nothing else visible.
    expect(trigger.parentElement?.children).toHaveLength(1);
  });

  it('registers under every focus key and unregisters on unmount', () => {
    const registerControl = vi.fn();
    const { unmount } = render(<Harness registerControl={registerControl} />);

    expect(registerControl).toHaveBeenCalledWith({ key: 'edit:1', element: kebab() });
    expect(registerControl).toHaveBeenCalledWith({ key: 'pause:1', element: kebab() });

    registerControl.mockClear();
    unmount();
    expect(registerControl).toHaveBeenCalledWith({ key: 'edit:1', element: null });
    expect(registerControl).toHaveBeenCalledWith({ key: 'pause:1', element: null });
  });
});

describe('RowActionsMenu opening', () => {
  it('opens on click into a portalled role=menu and focuses the first enabled item', async () => {
    const user = userEvent.setup();
    const { container } = render(<Harness />);

    await user.click(kebab());

    const menu = screen.getByRole('menu', { name: KEBAB });
    expect(kebab()).toHaveAttribute('aria-expanded', 'true');
    expect(kebab()).toHaveAttribute('aria-controls', menu.id);
    // Rendered outside the table (and its scrolling wrapper), straight under <body>.
    expect(container.contains(menu)).toBe(false);
    expect(menu.parentElement).toBe(document.body);
    expect(screen.getAllByRole('menuitem').map((item) => item.textContent)).toEqual([
      'Edit',
      'Pause',
      'Delete urls4irl/nightly',
    ]);
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toHaveFocus();
    expect(screen.getByRole('menuitem', { name: 'Pause' })).toHaveAttribute(
      'aria-disabled',
      'true',
    );
  });

  it('skips a disabled first item and closes again on a second click', async () => {
    const user = userEvent.setup();
    const items: RowMenuItem[] = [
      { id: 'a', label: 'Alpha', disabled: true },
      { id: 'b', label: 'Beta', onSelect: vi.fn() },
    ];
    render(<Harness items={items} />);

    await user.click(kebab());
    expect(screen.getByRole('menuitem', { name: 'Beta' })).toHaveFocus();

    await user.click(kebab());
    expect(screen.queryByRole('menu')).toBeNull();
    expect(kebab()).toHaveAttribute('aria-expanded', 'false');
  });

  it('opens from the keyboard: Enter, Space, ArrowDown (first) and ArrowUp (last)', async () => {
    const user = userEvent.setup();
    render(<Harness />);

    kebab().focus();
    await user.keyboard('{Enter}');
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toHaveFocus();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('menu')).toBeNull();

    await user.keyboard(' ');
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toHaveFocus();
    await user.keyboard('{Escape}');

    await user.keyboard('{ArrowDown}');
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toHaveFocus();
    await user.keyboard('{Escape}');

    await user.keyboard('{ArrowUp}');
    expect(screen.getByRole('menuitem', { name: 'Delete urls4irl/nightly' })).toHaveFocus();
  });

  it('keeps at most one menu open', async () => {
    const user = userEvent.setup();
    render(
      <>
        <Harness rowLabel="first/one" />
        <Harness rowLabel="second/two" />
      </>,
    );

    screen.getByRole('button', { name: 'Actions for first/one' }).focus();
    await user.keyboard('{Enter}');
    expect(screen.getAllByRole('menu')).toHaveLength(1);

    screen.getByRole('button', { name: 'Actions for second/two' }).focus();
    await user.keyboard('{Enter}');

    expect(screen.getAllByRole('menu')).toHaveLength(1);
    expect(screen.getByRole('menu', { name: 'Actions for second/two' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Actions for first/one' })).toHaveAttribute(
      'aria-expanded',
      'false',
    );
  });
});

describe('RowActionsMenu keyboard', () => {
  it('moves with ArrowDown/ArrowUp (wrapping), Home and End; disabled items stay reachable', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(kebab());

    const edit = screen.getByRole('menuitem', { name: 'Edit' });
    const pause = screen.getByRole('menuitem', { name: 'Pause' });
    const del = screen.getByRole('menuitem', { name: 'Delete urls4irl/nightly' });

    await user.keyboard('{ArrowDown}');
    expect(pause).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(del).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(edit).toHaveFocus();
    await user.keyboard('{ArrowUp}');
    expect(del).toHaveFocus();
    await user.keyboard('{Home}');
    expect(edit).toHaveFocus();
    await user.keyboard('{End}');
    expect(del).toHaveFocus();
  });

  it('Escape closes the menu and returns focus to the kebab', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(kebab());

    await user.keyboard('{Escape}');

    expect(screen.queryByRole('menu')).toBeNull();
    expect(kebab()).toHaveFocus();
  });

  // The menu is portalled to the end of <body>, so Tab must not walk on from inside it: it
  // closes the menu and puts focus on the kebab *before* the browser's default Tab action,
  // which then continues from the kebab (the e2e suite checks where it lands; user-event
  // resolves the default destination before the handler ran, so it cannot here).
  it('Tab closes the menu and puts focus on the kebab for the default Tab to continue from', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(kebab());

    // fireEvent returns false when the default action was prevented: Tab must carry on.
    const proceeds = fireEvent.keyDown(screen.getByRole('menuitem', { name: 'Edit' }), {
      key: 'Tab',
    });

    expect(proceeds).toBe(true);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(kebab()).toHaveFocus();
  });

  it('Shift+Tab does the same', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(kebab());

    fireEvent.keyDown(screen.getByRole('menuitem', { name: 'Edit' }), {
      key: 'Tab',
      shiftKey: true,
    });

    expect(screen.queryByRole('menu')).toBeNull();
    expect(kebab()).toHaveFocus();
  });

  it('ArrowDown on the kebab of an already open menu moves focus back into it', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(kebab());
    // Focus returns to the kebab without the menu closing (e.g. a focus-registry hand-off).
    kebab().focus();
    expect(screen.getByRole('menu')).toBeInTheDocument();

    await user.keyboard('{ArrowUp}');

    expect(screen.getByRole('menuitem', { name: 'Delete urls4irl/nightly' })).toHaveFocus();
  });
});

describe('RowActionsMenu dismissal', () => {
  it('closes on a press outside without stealing focus back', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(kebab());

    await user.click(screen.getByRole('button', { name: 'after' }));

    expect(screen.queryByRole('menu')).toBeNull();
    expect(screen.getByRole('button', { name: 'after' })).toHaveFocus();
  });

  it('closes when focus moves to another control, so no menu is left open and unreachable', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(kebab());

    act(() => {
      screen.getByRole('button', { name: 'after' }).focus();
    });

    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('a press on the menu padding keeps focus inside it, still keyboard operable', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(kebab());

    await user.click(screen.getByRole('menu'));

    expect(screen.getByRole('menu')).toHaveFocus();
    await user.keyboard('{ArrowDown}');
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toHaveFocus();
    await user.keyboard('{Escape}');
    expect(kebab()).toHaveFocus();
  });

  it('a press inside the menu does not close it', async () => {
    const user = userEvent.setup();
    render(<Harness note="Managed elsewhere." />);
    await user.click(kebab());

    await user.click(screen.getByText('Managed elsewhere.'));

    expect(screen.getByRole('menu')).toBeInTheDocument();
  });

  it('closes when the table wrapper scrolls and keeps focus out of <body>', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(kebab());
    expect(screen.getByRole('menuitem', { name: 'Edit' })).toHaveFocus();

    scrollTo({ element: screen.getByTestId('scroller'), left: 30 });

    expect(screen.queryByRole('menu')).toBeNull();
    expect(kebab()).toHaveFocus();
  });

  it('closes when the page scrolls, but not when the menu itself scrolls', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(kebab());

    scrollTo({ element: screen.getByRole('menu'), top: 20 });
    expect(screen.getByRole('menu')).toBeInTheDocument();

    vi.spyOn(window, 'scrollY', 'get').mockReturnValue(40);
    fireEvent.scroll(document);
    expect(screen.queryByRole('menu')).toBeNull();
  });

  it('ignores a scroll event whose scroller has not moved since the menu opened (a late report)', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(kebab());

    fireEvent.scroll(screen.getByTestId('scroller'));
    fireEvent.scroll(document);

    expect(screen.getByRole('menu')).toBeInTheDocument();
  });
});

describe('RowActionsMenu content', () => {
  it('shows the owner note first, as plain text, and the row error with a hidden alert', async () => {
    const user = userEvent.setup();
    render(<Harness note="Managed by x's deploy." error="Could not delete: boom" />);

    // Announced while the menu is closed, and flagged on the kebab.
    expect(screen.getByRole('alert')).toHaveTextContent('Could not delete: boom');
    expect(kebab()).toHaveAttribute('data-has-error', 'true');
    expect(kebab()).toHaveAccessibleDescription('Could not delete: boom');

    await user.click(kebab());

    const menu = screen.getByRole('menu');
    const [first, second] = Array.from(menu.children);
    expect(first).toHaveAttribute('role', 'presentation');
    expect(first).toHaveTextContent("Managed by x's deploy.");
    expect(second).toHaveAttribute('role', 'presentation');
    expect(second).toHaveTextContent('Could not delete: boom');
    // Neither is a menu item.
    expect(screen.getAllByRole('menuitem')).toHaveLength(3);
  });

  it('a plain item closes the menu, returns focus to the kebab, then runs', async () => {
    const user = userEvent.setup();
    const select = vi.fn(() => {
      // Focus is already back on the kebab by the time the action runs.
      expect(kebab()).toHaveFocus();
    });
    render(<Harness items={buildItems({ select })} />);
    await user.click(kebab());

    await user.click(screen.getByRole('menuitem', { name: 'Edit' }));

    expect(select).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('menu')).toBeNull();
    expect(kebab()).toHaveFocus();
  });

  it('activates an item with Enter and Space', async () => {
    const user = userEvent.setup();
    const select = vi.fn();
    render(<Harness items={buildItems({ select })} />);
    await user.click(kebab());

    await user.keyboard('{Enter}');
    expect(select).toHaveBeenCalledTimes(1);

    await user.keyboard('{ArrowDown}{Escape}');
    await user.click(kebab());
    await user.keyboard(' ');
    expect(select).toHaveBeenCalledTimes(2);
  });

  it('a disabled item does nothing and leaves the menu open', async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    render(<Harness items={[{ id: 'p', label: 'Pause', disabled: true, onSelect }]} />);
    await user.click(kebab());

    await user.click(screen.getByRole('menuitem', { name: 'Pause' }));

    expect(onSelect).not.toHaveBeenCalled();
    expect(screen.getByRole('menu')).toBeInTheDocument();
  });

  it('exposes aria-expanded on an item that opens an editor', async () => {
    const user = userEvent.setup();
    render(<Harness items={[{ id: 'e', label: 'Close', expanded: true }]} />);
    await user.click(kebab());

    expect(screen.getByRole('menuitem', { name: 'Close' })).toHaveAttribute(
      'aria-expanded',
      'true',
    );
  });
});

describe('RowActionsMenu confirm dialog', () => {
  async function openConfirm(items: RowMenuItem[] = []) {
    const user = userEvent.setup();
    const confirm = vi.fn();
    const view = render(<Harness items={items.length > 0 ? items : buildItems({ confirm })} />);
    await user.click(kebab());
    await user.click(screen.getByRole('menuitem', { name: 'Delete urls4irl/nightly' }));
    return { user, confirm, ...view };
  }

  it('closes the menu and opens a dialog with the title, item, notice and named buttons', async () => {
    const { confirm } = await openConfirm();

    expect(screen.queryByRole('menu')).toBeNull();
    expect(screen.queryByRole('group')).toBeNull();
    const dialog = screen.getByRole('dialog', { name: 'Delete nightly?' });
    expect(dialog).toHaveTextContent('Route');
    expect(dialog).toHaveTextContent('urls4irl/nightly');
    expect(dialog).toHaveTextContent('This cannot be undone.');
    expect(screen.getByRole('button', { name: 'Confirm delete urls4irl/nightly' })).toHaveClass(
      'modal__button--danger',
    );
    expect(screen.getByRole('button', { name: 'Cancel delete urls4irl/nightly' })).toHaveFocus();
    expect(confirm).not.toHaveBeenCalled();
  });

  it('uses the primary tone when the item asks for it', async () => {
    const items = buildItems().map((item) =>
      item.confirm === undefined
        ? item
        : { ...item, confirm: { ...item.confirm, tone: 'primary' as const } },
    );
    await openConfirm(items);

    expect(screen.getByRole('button', { name: 'Confirm delete urls4irl/nightly' })).toHaveClass(
      'modal__button--primary',
    );
  });

  it('Confirm runs the handler once, closes the dialog and focuses the kebab', async () => {
    const { user, confirm } = await openConfirm();

    await user.click(screen.getByRole('button', { name: 'Confirm delete urls4irl/nightly' }));

    expect(confirm).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.queryByRole('menu')).toBeNull();
    expect(kebab()).toHaveFocus();
  });

  it('can be opened and confirmed again: one call per confirmation', async () => {
    const { user, confirm } = await openConfirm();
    await user.click(screen.getByRole('button', { name: 'Confirm delete urls4irl/nightly' }));

    await user.click(kebab());
    await user.click(screen.getByRole('menuitem', { name: 'Delete urls4irl/nightly' }));
    await user.click(screen.getByRole('button', { name: 'Confirm delete urls4irl/nightly' }));

    expect(confirm).toHaveBeenCalledTimes(2);
  });

  it('a double activation fires once', async () => {
    const { confirm } = await openConfirm();
    const confirmButton = screen.getByRole('button', { name: 'Confirm delete urls4irl/nightly' });

    act(() => {
      fireEvent.click(confirmButton);
      fireEvent.click(confirmButton);
    });

    expect(confirm).toHaveBeenCalledTimes(1);
  });

  it('Cancel, the X and Escape fire nothing and return focus to the kebab', async () => {
    const dismissals: Array<(user: ReturnType<typeof userEvent.setup>) => Promise<void>> = [
      (user) => user.click(screen.getByRole('button', { name: 'Cancel delete urls4irl/nightly' })),
      (user) => user.click(screen.getByRole('button', { name: 'Close' })),
      (user) => user.keyboard('{Escape}'),
    ];
    for (const dismiss of dismissals) {
      const { user, confirm, unmount } = await openConfirm();

      await dismiss(user);

      expect(screen.queryByRole('dialog')).toBeNull();
      expect(confirm).not.toHaveBeenCalled();
      expect(kebab()).toHaveFocus();
      unmount();
    }
  });

  it('is not closed by the menu old focusin, scroll or pointerdown listeners', async () => {
    await openConfirm();

    fireEvent.pointerDown(screen.getByText('after'));
    scrollTo({ element: screen.getByTestId('scroller'), top: 40 });
    act(() => {
      screen.getByText('after').focus();
    });

    // The menu is gone, so none of its outside-press handlers is left to close the dialog.
    expect(screen.queryByRole('menu')).toBeNull();
    expect(screen.getByRole('dialog', { name: 'Delete nightly?' })).toBeInTheDocument();
  });

  it('closes without firing when the chosen item goes busy, and focuses the kebab', async () => {
    const { confirm, rerender } = await openConfirm();

    const busy = buildItems({ confirm }).map((item) =>
      item.id === 'delete' ? { ...item, disabled: true } : item,
    );
    rerender(<Harness items={busy} />);

    expect(screen.queryByRole('dialog')).toBeNull();
    expect(confirm).not.toHaveBeenCalled();
    expect(kebab()).toHaveFocus();
  });

  it('closes without firing when the chosen item disappears', async () => {
    const { confirm, rerender } = await openConfirm();

    rerender(<Harness items={buildItems({ confirm }).filter((item) => item.id !== 'delete')} />);

    expect(screen.queryByRole('dialog')).toBeNull();
    expect(confirm).not.toHaveBeenCalled();
    expect(kebab()).toHaveFocus();
  });

  it('closes without firing when the menu unmounts', async () => {
    const { confirm, unmount } = await openConfirm();

    unmount();

    expect(screen.queryByRole('dialog')).toBeNull();
    expect(confirm).not.toHaveBeenCalled();
  });

  it('reads the dialog text from the live item, not a snapshot', async () => {
    const { confirm, rerender } = await openConfirm();

    const changed = buildItems({ confirm }).map((item) =>
      item.confirm === undefined
        ? item
        : {
            ...item,
            confirm: {
              ...item.confirm,
              notice: 'Changed notice.',
              dialogItem: { label: 'Route', value: 'route 99' },
            },
          },
    );
    rerender(<Harness items={changed} />);

    const dialog = screen.getByRole('dialog', { name: 'Delete nightly?' });
    expect(dialog).toHaveTextContent('Changed notice.');
    expect(dialog).toHaveTextContent('route 99');
  });

  it('describes the menu by its note and errors, and announces each error once', async () => {
    const user = userEvent.setup();
    const props = {
      rowLabel: ROW,
      items: buildItems(),
      note: 'Managed by x.',
      focusKeys: [],
      registerControl: vi.fn(),
    };
    const { rerender } = render(<RowActionsMenu {...props} errors={['first failure']} />);
    await user.click(kebab());
    expect(screen.getByRole('menu')).toHaveAccessibleDescription('Managed by x. first failure');

    rerender(<RowActionsMenu {...props} errors={['first failure', 'second failure']} />);

    // Both stay shown; the first alert node is the same element, so only the new one is announced.
    expect(screen.getAllByRole('alert').map((alert) => alert.textContent)).toEqual([
      'first failure',
      'second failure',
    ]);
    expect(screen.getByRole('menu')).toHaveTextContent('first failure');
    expect(screen.getByRole('menu')).toHaveTextContent('second failure');
  });
});

describe('RowActionsMenu placement', () => {
  const VIEWPORT = { width: 390, height: 800 };
  const POPOVER = { width: 200, height: 150 };

  function stubGeometry(anchor: { left: number; right: number; top: number; bottom: number }) {
    vi.spyOn(document.documentElement, 'clientWidth', 'get').mockReturnValue(VIEWPORT.width);
    vi.spyOn(document.documentElement, 'clientHeight', 'get').mockReturnValue(VIEWPORT.height);
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockImplementation(function (
      this: HTMLElement,
    ) {
      return this.classList.contains('row-menu__popover') ? POPOVER.width : 0;
    });
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockImplementation(function (
      this: HTMLElement,
    ) {
      return this.classList.contains('row-menu__popover') ? POPOVER.height : 0;
    });
    vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (
      this: HTMLElement,
    ) {
      return this.classList.contains('row-menu__trigger')
        ? ({
            ...anchor,
            x: anchor.left,
            y: anchor.top,
            width: anchor.right - anchor.left,
            height: anchor.bottom - anchor.top,
            toJSON: () => ({}),
          } as DOMRect)
        : new DOMRect();
    });
  }

  async function openAndMeasure(anchor: {
    left: number;
    right: number;
    top: number;
    bottom: number;
  }) {
    stubGeometry(anchor);
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(kebab());
    const menu = screen.getByRole('menu');
    return { left: parseFloat(menu.style.left), top: parseFloat(menu.style.top) };
  }

  it('right-aligns to the kebab and opens below it', async () => {
    const { left, top } = await openAndMeasure({ left: 250, right: 300, top: 100, bottom: 144 });

    expect(left).toBe(100);
    expect(top).toBe(148);
  });

  it('stays inside the viewport for a kebab at the right edge', async () => {
    const { left } = await openAndMeasure({ left: 346, right: 390, top: 100, bottom: 144 });

    expect(left).toBe(VIEWPORT.width - 8 - POPOVER.width);
  });

  it('stays inside the viewport for a kebab near the left edge', async () => {
    const { left } = await openAndMeasure({ left: 0, right: 44, top: 100, bottom: 144 });

    expect(left).toBe(8);
  });

  it('flips above the kebab when there is no room below (the last row)', async () => {
    const { top } = await openAndMeasure({ left: 346, right: 390, top: 700, bottom: 744 });

    expect(top).toBe(700 - 4 - POPOVER.height);
  });

  it('pins inside the viewport when it fits neither above nor below', async () => {
    const original = POPOVER.height;
    POPOVER.height = 790;
    try {
      const { top } = await openAndMeasure({ left: 346, right: 390, top: 400, bottom: 444 });
      expect(top).toBe(8);
    } finally {
      POPOVER.height = original;
    }
  });
});
