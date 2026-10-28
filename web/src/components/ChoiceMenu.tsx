import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import type { JSX, KeyboardEvent } from 'react';
import { createPortal } from 'react-dom';

import './RowActionsMenu.css';

export interface Choice<Value extends string> {
  value: Value;
  label: string;
}

interface ChoiceMenuProps<Value extends string> {
  /** The trigger's accessible name and the menu's. */
  label: string;
  choices: Choice<Value>[];
  /** The current choice: announced with `aria-checked`. */
  value: Value;
  onSelect: (value: Value) => void;
  /** Marks the trigger with an accent dot (a non-default choice is collapsed inside the menu). */
  active?: boolean;
  /** Id of an element that states the current choice; the trigger is described by it. */
  describedById?: string;
  /** The glyph drawn inside the trigger. */
  icon: JSX.Element;
}

const VIEWPORT_MARGIN = 8;
const TRIGGER_GAP = 4;

/**
 * A single-choice menu behind an icon button: the sibling of `RowActionsMenu` for radio choices.
 * It shares that menu's trigger and popover styles. The popover is a `role="menu"` of
 * `menuitemradio` items portalled into `document.body` and positioned (fixed) from the trigger.
 * It opens on click / Enter / Space / ArrowDown / ArrowUp (focus lands on the current choice),
 * moves with the arrow keys, Home and End, and closes on Escape (focus returns to the trigger),
 * Tab, a press or focus outside, or choosing an item (focus returns to the trigger).
 */
export function ChoiceMenu<Value extends string>({
  label,
  choices,
  value,
  onSelect,
  active = false,
  describedById,
  icon,
}: ChoiceMenuProps<Value>): JSX.Element {
  const [open, setOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const popoverRef = useRef<HTMLDivElement | null>(null);
  const menuId = useId();

  const close = useCallback(({ restoreFocus }: { restoreFocus: boolean }) => {
    setOpen(false);
    if (restoreFocus) {
      triggerRef.current?.focus({ preventScroll: true });
    }
  }, []);

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
    const left = Math.max(
      VIEWPORT_MARGIN,
      Math.min(anchor.right - width, viewportWidth - VIEWPORT_MARGIN - width),
    );
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

  useLayoutEffect(() => {
    if (open) {
      position();
    }
  });

  const items = useCallback(
    (): HTMLButtonElement[] => [
      ...(popoverRef.current?.querySelectorAll<HTMLButtonElement>('[role="menuitemradio"]') ?? []),
    ],
    [],
  );

  const focusOpening = useRef<'current' | 'last' | null>(null);

  function openMenu(target: 'current' | 'last') {
    focusOpening.current = target;
    setOpen(true);
  }

  // On open, focus the current choice (or the last item for ArrowUp).
  useEffect(() => {
    if (!open) {
      return;
    }
    const target = focusOpening.current;
    focusOpening.current = null;
    const buttons = items();
    const element =
      target === 'last'
        ? buttons.at(-1)
        : (buttons.find((button) => button.getAttribute('aria-checked') === 'true') ?? buttons[0]);
    element?.focus({ preventScroll: true });
  }, [open, items]);

  // Dismissal: a press or focus outside; a scroll or resize keeps the popover on the trigger.
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
    const onFocusIn = (event: FocusEvent) => {
      if (!inside(event.target)) {
        close({ restoreFocus: false });
      }
    };
    const onMove = () => position();
    document.addEventListener('pointerdown', onPointerDown, true);
    document.addEventListener('focusin', onFocusIn);
    document.addEventListener('scroll', onMove, true);
    window.addEventListener('resize', onMove);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true);
      document.removeEventListener('focusin', onFocusIn);
      document.removeEventListener('scroll', onMove, true);
      window.removeEventListener('resize', onMove);
    };
  }, [open, close, position]);

  function onMenuKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    if (event.key === 'Escape') {
      event.preventDefault();
      event.stopPropagation();
      close({ restoreFocus: true });
      return;
    }
    if (event.key === 'Tab') {
      close({ restoreFocus: true });
      return;
    }
    const buttons = items();
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
      if (open) {
        const buttons = items();
        (event.key === 'ArrowDown' ? buttons[0] : buttons.at(-1))?.focus({ preventScroll: true });
      } else {
        openMenu(event.key === 'ArrowDown' ? 'current' : 'last');
      }
    } else if (event.key === 'Escape' && open) {
      event.preventDefault();
      close({ restoreFocus: false });
    }
  }

  function choose(choice: Choice<Value>) {
    close({ restoreFocus: true });
    onSelect(choice.value);
  }

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        className="row-menu__trigger"
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        aria-describedby={active ? describedById : undefined}
        data-filter-active={active ? 'true' : undefined}
        onKeyDown={onTriggerKeyDown}
        onClick={() => {
          if (open) {
            close({ restoreFocus: false });
          } else {
            openMenu('current');
          }
        }}
      >
        {icon}
      </button>
      {open &&
        createPortal(
          <div
            ref={popoverRef}
            id={menuId}
            className="row-menu__popover"
            role="menu"
            aria-label={label}
            tabIndex={-1}
            onKeyDown={onMenuKeyDown}
          >
            {choices.map((choice) => (
              <button
                key={choice.value}
                type="button"
                role="menuitemradio"
                aria-checked={choice.value === value}
                tabIndex={-1}
                className="row-menu__item row-menu__item--default"
                onClick={() => choose(choice)}
              >
                {choice.label}
              </button>
            ))}
          </div>,
          document.body,
        )}
    </>
  );
}
