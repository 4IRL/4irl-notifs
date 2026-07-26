import { useState } from 'react';
import type { JSX } from 'react';

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
}

/**
 * An inline, modal-free two-step confirmation control for destructive actions:
 * the first click swaps the trigger for a Confirm/Cancel pair; Confirm fires
 * onConfirm and reverts, Cancel just reverts. This is the single confirmation
 * pattern used across the admin UI (People delete, Remove app, Users delete +
 * deprovision, publisher-token rotation) — no modal/dialog system exists.
 */
export function ConfirmButton({
  triggerLabel,
  confirmLabel,
  onConfirm,
  className,
}: ConfirmButtonProps): JSX.Element {
  const [confirming, setConfirming] = useState(false);

  if (!confirming) {
    return (
      <button type="button" className={className} onClick={() => setConfirming(true)}>
        {triggerLabel}
      </button>
    );
  }

  return (
    <span className="confirm-button__group">
      <button
        type="button"
        className={className}
        onClick={() => {
          setConfirming(false);
          onConfirm();
        }}
      >
        {confirmLabel}
      </button>
      <button type="button" className="confirm-button__cancel" onClick={() => setConfirming(false)}>
        {strings.cancelAction}
      </button>
    </span>
  );
}
