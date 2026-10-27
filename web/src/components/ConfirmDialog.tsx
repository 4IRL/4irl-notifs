import { useRef } from 'react';
import type { JSX, ReactNode, RefObject } from 'react';

import { Modal } from './Modal';
import type { ModalProps } from './Modal';

/** The labelled box naming the item a dialog acts on, e.g. `{label: 'Destination', value: 'hook1'}`. */
export function ConfirmItem({ label, value }: { label: string; value: string }): JSX.Element {
  return (
    <div className="modal__item">
      <span className="modal__item-label">{label}</span>
      <span>{value}</span>
    </div>
  );
}

/** The warn notice explaining a dialog's consequence. */
export function ConfirmNotice({ children }: { children: ReactNode }): JSX.Element {
  return <p className="modal__notice modal__notice--warn">{children}</p>;
}

interface ConfirmButtonSpec {
  label: string;
  ariaLabel?: string;
  /** `aria-disabled`: the button stays focusable but its handler is the caller's to guard. */
  disabled?: boolean;
  onClick: () => void;
}

interface ConfirmActionsProps {
  cancel: ConfirmButtonSpec;
  /** Receives the Cancel button, so a dialog can focus it first. */
  cancelRef?: RefObject<HTMLButtonElement | null>;
  confirm: ConfirmButtonSpec & { tone?: 'danger' | 'primary' };
}

/** The Cancel / Confirm pair of a confirmation dialog. */
export function ConfirmActions({ cancel, cancelRef, confirm }: ConfirmActionsProps): JSX.Element {
  return (
    <>
      <button
        ref={cancelRef}
        type="button"
        className="modal__button modal__button--cancel"
        aria-label={cancel.ariaLabel}
        aria-disabled={cancel.disabled === true ? 'true' : undefined}
        onClick={cancel.onClick}
      >
        {cancel.label}
      </button>
      <button
        type="button"
        className={`modal__button modal__button--${confirm.tone ?? 'danger'}`}
        aria-label={confirm.ariaLabel}
        aria-disabled={confirm.disabled === true ? 'true' : undefined}
        onClick={confirm.onClick}
      >
        {confirm.label}
      </button>
    </>
  );
}

interface ConfirmDialogProps extends Omit<
  ModalProps,
  'actions' | 'children' | 'initialFocusRef' | 'onDismiss'
> {
  /** Called by Cancel, X, Escape and an allowed backdrop click. */
  onDismiss: () => void;
  item: { label: string; value: string };
  notice?: ReactNode;
  cancel: Omit<ConfirmButtonSpec, 'onClick'>;
  confirm: ConfirmButtonSpec & { tone?: 'danger' | 'primary' };
}

/**
 * A confirmation dialog on the shared `Modal`: the item box, an optional warn notice, and a
 * Cancel / Confirm pair. Cancel receives initial focus (the least destructive control).
 */
export function ConfirmDialog({
  item,
  notice,
  cancel,
  confirm,
  onDismiss,
  ...modalProps
}: ConfirmDialogProps): JSX.Element {
  const cancelRef = useRef<HTMLButtonElement | null>(null);
  return (
    <Modal
      {...modalProps}
      onDismiss={onDismiss}
      initialFocusRef={cancelRef}
      actions={
        <ConfirmActions
          cancel={{ ...cancel, onClick: onDismiss }}
          cancelRef={cancelRef}
          confirm={confirm}
        />
      }
    >
      <ConfirmItem label={item.label} value={item.value} />
      {notice !== undefined && <ConfirmNotice>{notice}</ConfirmNotice>}
    </Modal>
  );
}
