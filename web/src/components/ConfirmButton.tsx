import { useEffect, useRef, useState } from 'react';
import type { JSX, ReactNode } from 'react';

import { strings } from '../strings';
import './ConfirmButton.css';

/**
 * Props for ConfirmButton. `triggerLabel`/`confirmLabel` are BOTH the visible
 * text and the accessible name of their button, so callers control exact
 * button names (used by tests and screen readers).
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
        ref={triggerRef}
        type="button"
        className={className}
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
        {strings.cancelAction}
      </button>
      {confirmNotice !== undefined && (
        <span className="confirm-button__notice">{confirmNotice}</span>
      )}
    </span>
  );
}
