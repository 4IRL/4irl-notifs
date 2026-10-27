import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import type { JSX, KeyboardEvent } from 'react';
import { createPortal } from 'react-dom';

import { strings } from '../strings';
import { ConfirmDialog } from './ConfirmDialog';
import './RowActionsMenu.css';

/** A confirmed action: choosing it closes the menu and opens the shared confirm dialog. */
export interface RowMenuConfirm {
  onConfirm: () => void;
  confirmLabel: string;
  confirmAriaLabel?: string;
  cancelLabel: string;
  cancelAriaLabel?: string;
  /** Explains the consequence; shown in the dialog body. */
  notice?: string;
  /** The dialog title: the action phrased as a question, e.g. "Delete destination?". */
  dialogTitle: string;
  /** The labelled box naming the item, e.g. `{label: 'Destination', value: 'hook1'}`. */
  dialogItem: { label: string; value: string };
  /** Tone of the Confirm button; defaults to `'danger'`. */
  tone?: 'danger' | 'primary';
}

export interface RowMenuItem {
  /** Stable within the row. */
  id: string;
  /** Visible text; also the accessible name unless `ariaLabel` is given (it must contain it). */
  label: string;
  ariaLabel?: string;
  tone?: 'default' | 'primary' | 'danger';
  /** `aria-disabled`: the item stays in the menu and focusable, but activating it does nothing. */
  disabled?: boolean;
  /** For an item that opens an inline editor. */
  expanded?: boolean;
  /** A plain action: the menu closes, then this runs. */
  onSelect?: () => void;
  /** A destructive action: takes the place of `onSelect`. */
  confirm?: RowMenuConfirm;
}

interface RowActionsMenuProps {
  /** Names the row in the kebab's accessible name ("Actions for <rowLabel>"). */
  rowLabel: string;
  items: RowMenuItem[];
  /** Non-interactive first line of the menu (the owner note). */
  note?: string | null;
  /**
   * The row's server errors: flagged on the kebab, shown in the menu, and announced through
   * a visually hidden alert each (an error already shown is not announced again).
   */
  errors?: string[];
  /** Every focus-registry key the row's inline controls would have, so focus lands on the kebab. */
  focusKeys: string[];
  registerControl: (params: { key: string; element: HTMLElement | null }) => void;
}

type FocusTarget = { kind: 'first' } | { kind: 'last' };

const VIEWPORT_MARGIN = 8;
const TRIGGER_GAP = 4;
const NO_ERRORS: string[] = [];

/** The menu open right now, if any: opening one closes it, so at most one is ever open. */
let closeActiveMenu: (() => void) | null = null;

function KebabGlyph(): JSX.Element {
  return (
    <svg
      className="row-menu__glyph"
      viewBox="0 0 16 16"
      width="16"
      height="16"
      aria-hidden="true"
      focusable="false"
    >
      <circle cx="8" cy="3" r="1.5" fill="currentColor" />
      <circle cx="8" cy="8" r="1.5" fill="currentColor" />
      <circle cx="8" cy="13" r="1.5" fill="currentColor" />
    </svg>
  );
}

/**
 * The narrow-layout replacement for a table row's inline buttons: one three-dot button that
 * opens a menu of the row's actions. The menu is a `role="menu"` popover rendered through a
 * portal into `document.body` and positioned (fixed) from the button's rectangle, so the
 * table's scrolling wrapper can never clip it. It opens on click / Enter / Space (or
 * ArrowDown / ArrowUp on the button), focuses its first enabled item, and closes on Escape
 * (focus returns to the button), Tab, a press outside, a scroll, or choosing an item. A
 * confirm item closes the menu and opens the shared `Modal` dialog (the menu closes first, so
 * the two never fight over focus or scrolling); closing the dialog returns focus to the button.
 */
export function RowActionsMenu({
  rowLabel,
  items,
  note = null,
  errors = NO_ERRORS,
  focusKeys,
  registerControl,
}: RowActionsMenuProps): JSX.Element {
  const [open, setOpen] = useState(false);
  // The chosen confirm item's id (not the item): resolved against the live items on each render.
  const [dialogId, setDialogId] = useState<string | null>(null);
  const firedRef = useRef(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const popoverRef = useRef<HTMLDivElement | null>(null);
  const focusTarget = useRef<FocusTarget | null>(null);
  const menuId = useId();
  const errorId = useId();
  const noteId = useId();
  const menuErrorId = useId();

  /** Closes the menu; `restoreFocus` puts focus back on the kebab (never after a click elsewhere). */
  const close = useCallback(({ restoreFocus }: { restoreFocus: boolean }) => {
    setOpen(false);
    if (restoreFocus) {
      triggerRef.current?.focus({ preventScroll: true });
    }
  }, []);

  function openMenu(target: FocusTarget) {
    focusTarget.current = target;
    setOpen(true);
  }

  // At most one menu is open: opening this one closes whichever was open.
  useEffect(() => {
    if (!open) {
      return;
    }
    const mine = () => {
      setOpen(false);
    };
    const previous = closeActiveMenu;
    closeActiveMenu = mine;
    if (previous !== null && previous !== mine) {
      previous();
    }
    return () => {
      if (closeActiveMenu === mine) {
        closeActiveMenu = null;
      }
    };
  }, [open]);

  const dialogCandidate = dialogId === null ? undefined : items.find((it) => it.id === dialogId);
  // A dialog whose item vanished or went busy (the action started elsewhere) closes without
  // firing rather than offering a Confirm that would double-submit.
  const dialogStep =
    dialogCandidate?.confirm !== undefined && dialogCandidate.disabled !== true
      ? dialogCandidate
      : undefined;
  if (dialogId !== null && dialogStep === undefined) {
    setDialogId(null);
  }

  const position = useCallback(() => {
    const trigger = triggerRef.current;
    const popover = popoverRef.current;
    if (trigger === null || popover === null) {
      return;
    }
    const anchor = trigger.getBoundingClientRect();
    const viewportWidth = document.documentElement.clientWidth;
    const viewportHeight = document.documentElement.clientHeight;
    const { offsetWidth: width, offsetHeight: height } = popover;
    // Right-aligned to the kebab, kept inside the viewport.
    const left = Math.max(
      VIEWPORT_MARGIN,
      Math.min(anchor.right - width, viewportWidth - VIEWPORT_MARGIN - width),
    );
    // Below the kebab; above it when there is no room; pinned inside the viewport otherwise.
    let top = anchor.bottom + TRIGGER_GAP;
    if (top + height > viewportHeight - VIEWPORT_MARGIN) {
      const above = anchor.top - TRIGGER_GAP - height;
      top =
        above >= VIEWPORT_MARGIN
          ? above
          : Math.max(VIEWPORT_MARGIN, viewportHeight - VIEWPORT_MARGIN - height);
    }
    popover.style.left = `${left}px`;
    popover.style.top = `${top}px`;
  }, []);

  // Positioned before paint after every render while open (the items and errors can change
  // its size), and again when the viewport resizes.
  useLayoutEffect(() => {
    if (open) {
      position();
    }
  });

  /** Moves focus to the button `target` names inside the open popover. */
  const focusInPopover = useCallback((target: FocusTarget) => {
    const popover = popoverRef.current;
    if (popover === null) {
      return;
    }
    const buttons = [...popover.querySelectorAll<HTMLButtonElement>('button')];
    let element: HTMLButtonElement | undefined;
    switch (target.kind) {
      case 'first':
        element = buttons.find((b) => b.getAttribute('aria-disabled') !== 'true') ?? buttons[0];
        break;
      case 'last':
        element =
          [...buttons].reverse().find((b) => b.getAttribute('aria-disabled') !== 'true') ??
          buttons.at(-1);
        break;
    }
    element?.focus({ preventScroll: true });
  }, []);

  // Moves focus where the last interaction asked: first/last item on open. With nothing asked
  // and focus lost to <body>, it lands back in the menu so the keyboard is never stranded.
  useEffect(() => {
    if (!open || popoverRef.current === null) {
      return;
    }
    const target = focusTarget.current;
    if (target !== null) {
      focusTarget.current = null;
      focusInPopover(target);
    } else if (document.activeElement === null || document.activeElement === document.body) {
      focusInPopover({ kind: 'first' });
    }
  });

  // Dismissal: a press outside, a scroll anywhere (the wrapper, the page), a resize repositions.
  useEffect(() => {
    if (!open) {
      return;
    }
    const inside = (node: EventTarget | null) =>
      node instanceof Node &&
      (popoverRef.current?.contains(node) === true || triggerRef.current?.contains(node) === true);
    const onPointerDown = (event: PointerEvent) => {
      if (!inside(event.target)) {
        close({ restoreFocus: false });
      }
    };
    // Where every scroller above the kebab stood when the menu opened. A scroll event only
    // dismisses the menu when its scroller has really moved since: browsers report a scroll
    // that happened just before (scrolling the kebab into view, say) a frame late.
    const scrollersAtOpen = new Map<EventTarget, { left: number; top: number }>();
    for (
      let node = triggerRef.current?.parentElement ?? null;
      node !== null;
      node = node.parentElement
    ) {
      scrollersAtOpen.set(node, { left: node.scrollLeft, top: node.scrollTop });
    }
    scrollersAtOpen.set(document, { left: window.scrollX, top: window.scrollY });
    const hasMoved = (scroller: EventTarget | null): boolean => {
      const before = scroller === null ? undefined : scrollersAtOpen.get(scroller);
      if (before === undefined) {
        // A scroller this menu knows nothing about: dismiss, fail closed.
        return true;
      }
      const now =
        scroller === document
          ? { left: window.scrollX, top: window.scrollY }
          : { left: (scroller as Element).scrollLeft, top: (scroller as Element).scrollTop };
      return now.left !== before.left || now.top !== before.top;
    };
    const onScroll = (event: Event) => {
      if (popoverRef.current?.contains(event.target as Node) === true || !hasMoved(event.target)) {
        return;
      }
      // A scroll only hands focus back when it was inside the menu, so it is never lost to <body>.
      const focusWasInside = popoverRef.current?.contains(document.activeElement) === true;
      close({ restoreFocus: focusWasInside });
    };
    // Focus that moves anywhere else (another row's kebab after its action finished, say)
    // leaves this menu orphaned and dead to the keyboard, so it closes.
    const onFocusIn = (event: FocusEvent) => {
      if (!inside(event.target)) {
        close({ restoreFocus: false });
      }
    };
    const onResize = () => position();
    document.addEventListener('pointerdown', onPointerDown, true);
    document.addEventListener('focusin', onFocusIn);
    document.addEventListener('scroll', onScroll, true);
    window.addEventListener('resize', onResize);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true);
      document.removeEventListener('focusin', onFocusIn);
      document.removeEventListener('scroll', onScroll, true);
      window.removeEventListener('resize', onResize);
    };
  }, [open, close, position]);

  function onMenuKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const popover = popoverRef.current;
    if (popover === null) {
      return;
    }
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      close({ restoreFocus: true });
      return;
    }
    if (event.key === 'Tab') {
      // Hand focus back to the kebab and let the browser's Tab continue from there.
      close({ restoreFocus: true });
      return;
    }
    // The arrow keys move between the items and never scroll the page (a scroll would close
    // the menu).
    const buttons = [...popover.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')];
    if (buttons.length === 0) {
      return;
    }
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
    let next: number | null = null;
    switch (event.key) {
      case 'ArrowDown':
      case 'ArrowRight':
        next = (current + 1) % buttons.length;
        break;
      case 'ArrowUp':
      case 'ArrowLeft':
        next = (current <= 0 ? buttons.length : current) - 1;
        break;
      case 'Home':
        next = 0;
        break;
      case 'End':
        next = buttons.length - 1;
        break;
      default:
        break;
    }
    if (next !== null) {
      event.preventDefault();
      buttons[next]?.focus({ preventScroll: true });
    }
  }

  function onTriggerKeyDown(event: KeyboardEvent<HTMLButtonElement>) {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      const target: FocusTarget = { kind: event.key === 'ArrowDown' ? 'first' : 'last' };
      if (open) {
        // Already open (focus came back to the kebab): no render follows, so move focus now.
        focusInPopover(target);
      } else {
        openMenu(target);
      }
    } else if (event.key === 'Escape' && open) {
      event.preventDefault();
      close({ restoreFocus: false });
    }
  }

  function choose(item: RowMenuItem) {
    if (item.disabled === true) {
      return;
    }
    if (item.confirm !== undefined) {
      // The menu closes first and without taking focus back; the dialog returns focus to the
      // kebab when it closes.
      firedRef.current = false;
      setOpen(false);
      setDialogId(item.id);
      return;
    }
    close({ restoreFocus: true });
    item.onSelect?.();
  }

  function confirmChosen(item: RowMenuItem) {
    if (firedRef.current) {
      return;
    }
    firedRef.current = true;
    setDialogId(null);
    item.confirm?.onConfirm();
  }

  const name = strings.rowActionsFor({ row: rowLabel });
  const kebabKeys = focusKeys.join('|');

  return (
    <>
      <button
        ref={(element) => {
          triggerRef.current = element;
          for (const key of kebabKeys === '' ? [] : kebabKeys.split('|')) {
            registerControl({ key, element });
          }
        }}
        type="button"
        className="row-menu__trigger"
        aria-label={name}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={menuId}
        aria-describedby={errors.length === 0 ? undefined : errorId}
        data-has-error={errors.length === 0 ? undefined : 'true'}
        onKeyDown={onTriggerKeyDown}
        onClick={() => {
          if (open) {
            close({ restoreFocus: false });
          } else {
            openMenu({ kind: 'first' });
          }
        }}
      >
        <KebabGlyph />
      </button>
      {errors.length > 0 && (
        <span id={errorId} className="row-menu__sr">
          {errors.map((message) => (
            <span key={message} role="alert">
              {message}
            </span>
          ))}
        </span>
      )}
      {open &&
        createPortal(
          <div
            ref={popoverRef}
            id={menuId}
            className="row-menu__popover"
            role="menu"
            aria-label={name}
            aria-describedby={
              [note === null ? null : noteId, errors.length === 0 ? null : menuErrorId]
                .filter((id): id is string => id !== null)
                .join(' ') || undefined
            }
            // Focusable so a press on its padding keeps focus (and the keyboard) in the menu.
            tabIndex={-1}
            onKeyDown={onMenuKeyDown}
          >
            {note !== null && (
              <div id={noteId} role="presentation" className="row-menu__note">
                {note}
              </div>
            )}
            {errors.length > 0 && (
              <div id={menuErrorId} role="presentation" className="row-menu__error">
                {errors.map((message) => (
                  <p key={message}>{message}</p>
                ))}
              </div>
            )}
            {items.map((item) => (
              <button
                key={item.id}
                type="button"
                role="menuitem"
                tabIndex={-1}
                data-item-id={item.id}
                className={`row-menu__item row-menu__item--${item.tone ?? 'default'}`}
                aria-label={item.ariaLabel}
                aria-disabled={item.disabled === true ? 'true' : undefined}
                aria-expanded={item.expanded}
                onClick={() => choose(item)}
              >
                {item.label}
              </button>
            ))}
          </div>,
          document.body,
        )}
      {dialogStep?.confirm !== undefined && (
        <ConfirmDialog
          open
          title={dialogStep.confirm.dialogTitle}
          onDismiss={() => setDialogId(null)}
          returnFocusRef={triggerRef}
          item={dialogStep.confirm.dialogItem}
          notice={dialogStep.confirm.notice}
          cancel={{
            label: dialogStep.confirm.cancelLabel,
            ariaLabel: dialogStep.confirm.cancelAriaLabel,
          }}
          confirm={{
            label: dialogStep.confirm.confirmLabel,
            ariaLabel: dialogStep.confirm.confirmAriaLabel,
            tone: dialogStep.confirm.tone,
            onClick: () => confirmChosen(dialogStep),
          }}
        />
      )}
    </>
  );
}
