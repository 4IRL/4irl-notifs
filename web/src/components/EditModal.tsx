import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import type { JSX, ReactNode, RefObject } from 'react';

import { strings } from '../strings';
import { Modal } from './Modal';
import './EditModal.css';

/** An in-dialog confirmation (replaces a stacked ConfirmDialog inside an edit form). */
export interface ConfirmStep {
  message: string;
  confirmLabel: string;
  cancelLabel: string;
  onConfirm: () => void;
  onCancel: () => void;
}

export interface EditModalProps {
  open: boolean;
  title: string;
  /** Unguarded close; called by Discard changes, by a clean requestClose and by the Close button. */
  onClose: () => void;
  /** True when the form has unsaved edits: a dismissal then shows the discard guard. */
  dirty: boolean;
  busy: boolean;
  guardMessage?: string;
  returnFocusRef: RefObject<HTMLElement | null>;
  fallbackFocusRef?: RefObject<HTMLElement | null>;
  initialFocusRef?: RefObject<HTMLElement | null>;
  discardLabel?: string;
  keepLabel?: string;
  confirmStep?: ConfirmStep | null;
  /** The edited row vanished after a refresh: show a notice and disable the form. */
  rowGone?: boolean;
  ownerRow?: { attribute: string; id: string };
  restoreFocus?: boolean;
  children: ReactNode | ((api: { requestClose: () => void }) => ReactNode);
}

type BarMode = 'confirm' | 'discard' | null;

const FIELD_SELECTOR =
  'input:not([disabled]), select:not([disabled]), textarea:not([disabled]), button:not([disabled])';

export function EditModal({
  open,
  title,
  onClose,
  dirty,
  busy,
  guardMessage,
  returnFocusRef,
  fallbackFocusRef,
  initialFocusRef,
  discardLabel = strings.discardChanges,
  keepLabel = strings.keepEditing,
  confirmStep = null,
  rowGone = false,
  ownerRow,
  restoreFocus,
  children,
}: EditModalProps): JSX.Element {
  const [confirmingDiscard, setConfirmingDiscard] = useState(false);
  // Close reset: Modal returns null when closed but does not touch this state.
  if (!open && confirmingDiscard) setConfirmingDiscard(false);

  const messageId = useId();
  const contentRef = useRef<HTMLDivElement | null>(null);
  const keepRef = useRef<HTMLButtonElement | null>(null);
  const cancelRef = useRef<HTMLButtonElement | null>(null);
  const goneRef = useRef<HTMLParagraphElement | null>(null);
  const recordedRef = useRef<HTMLElement | null>(null);
  const previousMode = useRef<BarMode>(null);

  const mode: BarMode = !open
    ? null
    : confirmStep
      ? 'confirm'
      : confirmingDiscard
        ? 'discard'
        : null;

  // Runs after the commit that added or removed `inert`, so focus() is not a no-op.
  useLayoutEffect(() => {
    const previous = previousMode.current;
    previousMode.current = mode;
    if (previous === mode) return;
    if (mode !== null) {
      if (previous === null) {
        const active = document.activeElement;
        recordedRef.current = active instanceof HTMLElement ? active : null;
      }
      (mode === 'confirm' ? cancelRef : keepRef).current?.focus();
      return;
    }
    if (!open) {
      recordedRef.current = null;
      return;
    }
    const recorded = recordedRef.current;
    recordedRef.current = null;
    if (rowGone) {
      goneRef.current?.focus();
    } else if (recorded?.isConnected) {
      recorded.focus();
    } else {
      contentRef.current?.querySelector<HTMLElement>(FIELD_SELECTOR)?.focus();
    }
  }, [mode, open, rowGone]);

  // The notice must not steal focus from Keep editing / Cancel; the restore above handles that case.
  // Passive, so it runs after Modal's open effect (children's passive effects run first) and
  // wins when the dialog opens with the row already gone and no bar showing.
  useEffect(() => {
    if (open && rowGone && mode === null) goneRef.current?.focus();
  }, [open, rowGone, mode]);

  const requestClose = () => {
    if (busy) return;
    if (confirmStep) {
      confirmStep.onCancel();
      return;
    }
    if (confirmingDiscard) {
      setConfirmingDiscard(false);
      return;
    }
    if (dirty) {
      setConfirmingDiscard(true);
      return;
    }
    onClose();
  };

  let actions: ReactNode = null;
  if (confirmStep) {
    actions = (
      <div className="edit-modal__bar">
        <p id={messageId} role="alert" className="modal__alert edit-modal__message">
          {confirmStep.message}
        </p>
        <div className="edit-modal__buttons">
          <button
            type="button"
            className="modal__button modal__button--danger"
            aria-disabled={busy ? true : undefined}
            onClick={() => {
              if (busy) return;
              confirmStep.onConfirm();
            }}
          >
            {confirmStep.confirmLabel}
          </button>
          <button
            type="button"
            ref={cancelRef}
            aria-describedby={messageId}
            className="modal__button modal__button--cancel"
            aria-disabled={busy ? true : undefined}
            onClick={() => {
              if (busy) return;
              confirmStep.onCancel();
            }}
          >
            {confirmStep.cancelLabel}
          </button>
        </div>
      </div>
    );
  } else if (confirmingDiscard) {
    actions = (
      <div className="edit-modal__bar">
        <p id={messageId} role="alert" className="modal__alert edit-modal__message">
          {guardMessage ?? strings.discardChangesMessage}
        </p>
        <div className="edit-modal__buttons">
          <button
            type="button"
            className="modal__button modal__button--danger"
            aria-disabled={busy ? true : undefined}
            onClick={() => {
              if (busy) return;
              setConfirmingDiscard(false);
              onClose();
            }}
          >
            {discardLabel}
          </button>
          <button
            type="button"
            ref={keepRef}
            aria-describedby={messageId}
            className="modal__button modal__button--primary"
            onClick={() => setConfirmingDiscard(false)}
          >
            {keepLabel}
          </button>
        </div>
      </div>
    );
  } else if (rowGone) {
    actions = (
      <button
        type="button"
        className="modal__button modal__button--cancel"
        aria-disabled={busy ? true : undefined}
        onClick={() => requestClose()}
      >
        {strings.closeAction}
      </button>
    );
  }

  const content = typeof children === 'function' ? children({ requestClose }) : children;

  return (
    <Modal
      open={open}
      title={title}
      size="wide"
      onDismiss={requestClose}
      dismissible={!busy}
      busy={busy}
      ownerRow={ownerRow}
      initialFocusRef={initialFocusRef}
      returnFocusRef={returnFocusRef}
      fallbackFocusRef={fallbackFocusRef}
      restoreFocus={restoreFocus}
      actions={actions}
    >
      {rowGone ? (
        <p ref={goneRef} role="alert" tabIndex={-1} className="modal__alert">
          {strings.editRowGone}
        </p>
      ) : null}
      <div ref={contentRef} className="edit-modal" inert={mode !== null || rowGone}>
        <fieldset className="edit-modal__fieldset" disabled={rowGone}>
          {content}
        </fieldset>
      </div>
    </Modal>
  );
}
