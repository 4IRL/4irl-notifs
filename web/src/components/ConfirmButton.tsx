import { useCallback, useLayoutEffect, useRef, useState } from 'react';
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
  /** Class applied to the trigger. */
  className?: string;
  /** Rendered in the dialog body under the item box. */
  confirmNotice?: ReactNode;
  /** While true the trigger is aria-disabled, shows `busyLabel`, and never opens the dialog. */
  busy?: boolean;
  busyLabel?: string;
  /** Label of the dismiss button; defaults to `strings.cancelAction`. */
  dismissLabel?: string;
  /**
   * `aria-label` of the dismiss button (its visible text must be contained in it), so two
   * rows' dismiss buttons are distinguishable.
   */
  dismissAriaLabel?: string;
  /** `aria-label` of the trigger (its visible text must be contained in it). */
  triggerAriaLabel?: string;
  /** `aria-label` of the confirm button (its visible text must be contained in it). */
  confirmAriaLabel?: string;
  /** `aria-label` of the trigger only while `busy` (the visible busy text must be contained in it). */
  busyAriaLabel?: string;
  /**
   * Ref callback for the trigger button: called with the element when it mounts and with `null`
   * when it unmounts, so a parent can focus a row's trigger.
   */
  registerTrigger?: (element: HTMLButtonElement | null) => void;
  /** The dialog title: the action phrased as a question, e.g. "Delete destination?". */
  dialogTitle: string;
  /** The labelled box naming the item, e.g. `{label: 'Destination', value: 'hook1'}`. */
  dialogItem: { label: string; value: string };
  /** Tone of the Confirm button; defaults to `'danger'`. */
  tone?: 'danger' | 'primary';
  /** Stamped on the dialog so focus inside it resolves to its owning row. */
  ownerRow?: { attribute: string; id: string };
}

/**
 * A two-step confirmation control for destructive or consequential actions. The trigger stays
 * mounted and opens a shared `Modal` that names the action and the item, shows `confirmNotice`,
 * and has Confirm and Cancel. Cancel receives initial focus (the least destructive control).
 * Confirm closes the dialog first, then calls onConfirm exactly once; X, Escape, backdrop click,
 * hashchange, going busy and unmounting all close it without firing. Focus returns to the trigger.
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
  dialogTitle,
  dialogItem,
  tone = 'danger',
  ownerRow,
}: ConfirmButtonProps): JSX.Element {
  const [confirming, setConfirming] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const firedRef = useRef(false);
  const registerTriggerRef = useRef(registerTrigger);

  // Kept current without making an inline callback re-register the trigger.
  useLayoutEffect(() => {
    registerTriggerRef.current = registerTrigger;
  });

  // A stable ref callback, so re-renders never re-register the trigger.
  const triggerRefCallback = useCallback((element: HTMLButtonElement | null) => {
    triggerRef.current = element;
    registerTriggerRef.current?.(element);
  }, []);

  // An open dialog closes when the action becomes busy (state adjusted during
  // render, the supported alternative to an effect-driven reset).
  if (busy && confirming) {
    setConfirming(false);
  }

  const closeDialog = (): void => {
    setConfirming(false);
  };

  return (
    <>
      <button
        ref={triggerRefCallback}
        type="button"
        className={className}
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
