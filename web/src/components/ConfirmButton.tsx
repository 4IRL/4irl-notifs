import { useEffect, useRef, useState } from 'react';
import type { JSX, ReactNode } from 'react';

import { strings } from '../strings';
import './ConfirmButton.css';

/**
 * Props for ConfirmButton. `triggerLabel`/`confirmLabel` are BOTH the visible
 * text and the accessible name of their button, so callers control exact
 * button names (used by tests and screen readers). The optional `*AriaLabel`
 * props give a button a longer accessible name that starts with its visible text.
 */
interface ConfirmButtonProps {
  triggerLabel: string;
  confirmLabel: string;
  onConfirm: () => void;
  /** Class applied to the trigger and the confirm button (e.g. a danger style). */
  className?: string;
  /** Rendered next to the Confirm/Cancel pair, only while the pair is showing. */
  confirmNotice?: ReactNode;
  /** While true the trigger is aria-disabled, shows `busyLabel`, and never opens the pair. */
  busy?: boolean;
  busyLabel?: string;
  /** Label of the dismiss button; defaults to `strings.cancelAction`. */
  dismissLabel?: string;
  /** `aria-label` of the trigger (its visible text must be a prefix of it). */
  triggerAriaLabel?: string;
  /** `aria-label` of the confirm button (its visible text must be a prefix of it). */
  confirmAriaLabel?: string;
  /** `aria-label` of the trigger only while `busy` (the visible busy text must be a prefix). */
  busyAriaLabel?: string;
  /**
   * Ref callback for the trigger button: called with the element when it
   * mounts and with `null` when it unmounts (e.g. while the pair is showing),
   * so a parent can focus a row's trigger.
   */
  registerTrigger?: (element: HTMLButtonElement | null) => void;
}

/**
 * An inline, modal-free two-step confirmation control for destructive actions:
 * the first click swaps the trigger for a Confirm/Cancel pair; Confirm fires
 * onConfirm and reverts, Cancel just reverts. This is the single confirmation
 * pattern used across the admin UI (People delete, Remove app, Users delete +
 * deprovision, publisher-token rotation, destination actions) — no
 * modal/dialog system exists.
 *
 * Focus: Confirm receives focus when the pair appears; after Cancel or Confirm
 * the trigger regains it (so a busy, aria-disabled trigger holds focus for the
 * whole in-flight period).
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
  triggerAriaLabel,
  confirmAriaLabel,
  busyAriaLabel,
  registerTrigger,
}: ConfirmButtonProps): JSX.Element {
  const [confirming, setConfirming] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const confirmRef = useRef<HTMLButtonElement | null>(null);
  const restoreTriggerFocus = useRef(false);

  // An open pair closes when the action becomes busy (state adjusted during
  // render, the supported alternative to an effect-driven reset).
  if (busy && confirming) {
    setConfirming(false);
  }

  useEffect(() => {
    if (confirming) {
      confirmRef.current?.focus();
    } else if (restoreTriggerFocus.current) {
      restoreTriggerFocus.current = false;
      triggerRef.current?.focus();
    }
  }, [confirming]);

  if (!confirming) {
    return (
      <button
        ref={(element) => {
          triggerRef.current = element;
          registerTrigger?.(element);
        }}
        type="button"
        className={className}
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
    <span className="confirm-button__group">
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
