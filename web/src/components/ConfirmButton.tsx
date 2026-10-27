import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { JSX, ReactNode } from 'react';

import { strings } from '../strings';
import { ConfirmDialog } from './ConfirmDialog';
import './ConfirmButton.css';

/**
 * Props for ConfirmButton. `triggerLabel`/`confirmLabel` are BOTH the visible
 * text and the accessible name of their button, so callers control exact
 * button names (used by tests and screen readers). The optional `*AriaLabel`
 * props give a button a longer accessible name that contains its visible text (WCAG 2.5.3
 * Label in Name), so a row's buttons can read as the bare verb while keeping the row's name.
 */
interface ConfirmButtonProps {
  triggerLabel: string;
  confirmLabel: string;
  onConfirm: () => void;
  /** Class applied to the trigger and the confirm button (e.g. a danger style). */
  className?: string;
  /** Rendered next to the Confirm/Cancel pair, or in the dialog body, only while confirming. */
  confirmNotice?: ReactNode;
  /** While true the trigger is aria-disabled, shows `busyLabel`, and never opens the pair. */
  busy?: boolean;
  busyLabel?: string;
  /** Label of the dismiss button; defaults to `strings.cancelAction`. */
  dismissLabel?: string;
  /**
   * `aria-label` of the dismiss button (its visible text must be contained in it), so two
   * rows mid-confirm at once have distinguishable dismiss buttons.
   */
  dismissAriaLabel?: string;
  /** `aria-label` of the trigger (its visible text must be contained in it). */
  triggerAriaLabel?: string;
  /** `aria-label` of the confirm button (its visible text must be contained in it). */
  confirmAriaLabel?: string;
  /** `aria-label` of the trigger only while `busy` (the visible busy text must be contained in it). */
  busyAriaLabel?: string;
  /**
   * Ref callback for the trigger button: called with the element when it
   * mounts and with `null` when it unmounts (e.g. while the pair is showing),
   * so a parent can focus a row's trigger.
   */
  registerTrigger?: (element: HTMLButtonElement | null) => void;
  /**
   * Called with `true` when the Confirm/Cancel pair opens and with `false` when it closes
   * (Cancel, Confirm, Escape, going busy, or this button unmounting while open), before the
   * browser paints, so an owner can hide the row's other buttons while the pair shows.
   */
  onConfirmingChange?: (confirming: boolean) => void;
  /**
   * Hides the trigger (the `hidden` attribute: it stays mounted, so refs and focus keys still
   * resolve) while another control of the same row has its pair open.
   */
  hidden?: boolean;
  /**
   * Dialog mode: the dialog title, the action phrased as a question, e.g. "Delete destination?".
   * With `dialogTitle` and `dialogItem` set the confirmation opens a shared `Modal` instead of the
   * inline pair.
   */
  dialogTitle?: string;
  /** Dialog mode: the labelled box naming the item, e.g. `{label: 'Destination', value: 'hook1'}`. */
  dialogItem?: { label: string; value: string };
  /** Dialog mode: tone of the Confirm button; defaults to `'danger'`. */
  tone?: 'danger' | 'primary';
  /** Dialog mode: stamped on the dialog so focus inside it resolves to its owning row. */
  ownerRow?: { attribute: string; id: string };
}

/**
 * A two-step confirmation control for destructive or consequential actions.
 *
 * Dialog mode (`dialogTitle` and `dialogItem`): the trigger stays mounted and opens a shared
 * `Modal` that names the action and the item, shows `confirmNotice`, and has Confirm and Cancel.
 * Cancel receives initial focus (the least destructive control). Confirm closes the dialog first,
 * then calls onConfirm exactly once; X, Escape, backdrop click, hashchange, going busy and
 * unmounting all close it without firing. Focus returns to the trigger.
 *
 * Inline mode (otherwise): the first click swaps the trigger for a Confirm/Cancel pair; Confirm
 * fires onConfirm and reverts, Cancel just reverts. Confirm receives focus when the pair appears;
 * after Cancel, Confirm or Escape the trigger regains it.
 */
export function ConfirmButton({
  triggerLabel,
  confirmLabel,
  onConfirm,
  className,
  confirmNotice,
  busy = false,
  busyLabel,
  dismissLabel = strings.cancelAction,
  dismissAriaLabel,
  triggerAriaLabel,
  confirmAriaLabel,
  busyAriaLabel,
  registerTrigger,
  onConfirmingChange,
  hidden = false,
  dialogTitle,
  dialogItem,
  tone = 'danger',
  ownerRow,
}: ConfirmButtonProps): JSX.Element {
  const [confirming, setConfirming] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const confirmRef = useRef<HTMLButtonElement | null>(null);
  const restoreTriggerFocus = useRef(false);
  const firedRef = useRef(false);
  const onConfirmingChangeRef = useRef(onConfirmingChange);
  const registerTriggerRef = useRef(registerTrigger);
  const dialogMode = dialogTitle !== undefined && dialogItem !== undefined;

  // Kept current without making an inline callback re-fire the notifications below.
  useLayoutEffect(() => {
    onConfirmingChangeRef.current = onConfirmingChange;
    registerTriggerRef.current = registerTrigger;
  });

  // A stable ref callback, so re-renders never re-register the trigger.
  const triggerRefCallback = useCallback((element: HTMLButtonElement | null) => {
    triggerRef.current = element;
    registerTriggerRef.current?.(element);
  }, []);

  // The owner hears of every open and close before paint, so the row's other buttons never show
  // beside an open pair. An unmount while open (the row was removed) closes it for the owner too.
  useLayoutEffect(() => {
    onConfirmingChangeRef.current?.(confirming);
  }, [confirming]);
  useLayoutEffect(
    () => () => {
      onConfirmingChangeRef.current?.(false);
    },
    [],
  );

  // An open pair or dialog closes when the action becomes busy (state adjusted during
  // render, the supported alternative to an effect-driven reset).
  if (busy && confirming) {
    setConfirming(false);
  }

  // Inline mode moves focus itself; the dialog owns its focus.
  useEffect(() => {
    if (dialogMode) {
      return;
    }
    if (confirming) {
      confirmRef.current?.focus();
    } else if (restoreTriggerFocus.current) {
      restoreTriggerFocus.current = false;
      triggerRef.current?.focus();
    }
  }, [confirming, dialogMode]);

  const closeDialog = (): void => {
    setConfirming(false);
  };

  if (dialogMode) {
    return (
      <>
        <button
          ref={triggerRefCallback}
          type="button"
          className={className}
          hidden={hidden}
          aria-label={busy ? busyAriaLabel : triggerAriaLabel}
          aria-disabled={busy ? 'true' : undefined}
          onClick={() => {
            if (busy) {
              return;
            }
            firedRef.current = false;
            setConfirming(true);
          }}
        >
          {busy && busyLabel !== undefined ? busyLabel : triggerLabel}
        </button>
        <ConfirmDialog
          open={confirming}
          title={dialogTitle}
          onDismiss={closeDialog}
          ownerRow={ownerRow}
          returnFocusRef={triggerRef}
          item={dialogItem}
          notice={confirmNotice}
          cancel={{ label: dismissLabel, ariaLabel: dismissAriaLabel }}
          confirm={{
            label: confirmLabel,
            ariaLabel: confirmAriaLabel,
            tone,
            onClick: () => {
              if (firedRef.current) {
                return;
              }
              firedRef.current = true;
              closeDialog();
              onConfirm();
            },
          }}
        />
      </>
    );
  }

  if (!confirming) {
    return (
      <button
        ref={triggerRefCallback}
        type="button"
        className={className}
        hidden={hidden}
        aria-label={busy ? busyAriaLabel : triggerAriaLabel}
        aria-disabled={busy ? 'true' : undefined}
        onClick={() => {
          if (busy) {
            return;
          }
          setConfirming(true);
        }}
      >
        {busy && busyLabel !== undefined ? busyLabel : triggerLabel}
      </button>
    );
  }

  return (
    <span
      className="confirm-button__group"
      onKeyDown={(event) => {
        // Escape backs out like Cancel. It is handled here, so it does not also reach an
        // enclosing editor or menu.
        if (event.key !== 'Escape') {
          return;
        }
        event.stopPropagation();
        restoreTriggerFocus.current = true;
        setConfirming(false);
      }}
    >
      <button
        ref={confirmRef}
        type="button"
        className={className}
        aria-label={confirmAriaLabel}
        onClick={() => {
          restoreTriggerFocus.current = true;
          setConfirming(false);
          onConfirm();
        }}
      >
        {confirmLabel}
      </button>
      <button
        type="button"
        className="confirm-button__cancel"
        aria-label={dismissAriaLabel}
        onClick={() => {
          restoreTriggerFocus.current = true;
          setConfirming(false);
        }}
      >
        {dismissLabel}
      </button>
      {confirmNotice !== undefined && (
        <span className="confirm-button__notice">{confirmNotice}</span>
      )}
    </span>
  );
}
