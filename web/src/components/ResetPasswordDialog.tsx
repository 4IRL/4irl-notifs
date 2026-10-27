import { useEffect, useRef } from 'react';
import type { JSX, RefObject } from 'react';

import type { CopyStatus } from '../hooks/useCopyStatus';
import type { ResetDialog } from '../hooks/useResetPassword';
import { strings } from '../strings';
import { ConfirmActions, ConfirmItem, ConfirmNotice } from './ConfirmDialog';
import { Modal } from './Modal';

interface ResetPasswordDialogProps {
  dialog: ResetDialog | null;
  copyStatus: CopyStatus | null;
  copy: (text: string) => void;
  returnFocusRef: RefObject<HTMLElement | null>;
  fallbackFocusRef: RefObject<HTMLElement | null>;
  onClose: () => void;
  onConfirm: () => void;
}

/** The reset-password modal: one shell whose actions and body follow the flow's phase. */
export function ResetPasswordDialog({
  dialog,
  copyStatus,
  copy,
  returnFocusRef,
  fallbackFocusRef,
  onClose,
  onConfirm,
}: ResetPasswordDialogProps): JSX.Element {
  const cancelRef = useRef<HTMLButtonElement | null>(null);
  const copyButtonRef = useRef<HTMLButtonElement | null>(null);
  const closeButtonRef = useRef<HTMLButtonElement | null>(null);

  const phase = dialog?.phase;
  useEffect(() => {
    if (phase === 'revealed') {
      copyButtonRef.current?.focus();
    } else if (phase === 'failed') {
      closeButtonRef.current?.focus();
    }
  }, [phase]);

  function renderActions(current: ResetDialog): JSX.Element {
    switch (current.phase) {
      case 'revealed':
        return (
          <button
            type="button"
            className="modal__button modal__button--cancel"
            aria-label={strings.resetPasswordDoneNamed({ user: current.user })}
            onClick={onClose}
          >
            {strings.resetPasswordDone}
          </button>
        );
      case 'failed':
        return (
          <button
            ref={closeButtonRef}
            type="button"
            className="modal__button modal__button--cancel"
            onClick={onClose}
          >
            {strings.closeDialog}
          </button>
        );
      case 'confirm':
      case 'busy': {
        const busy = current.phase === 'busy';
        return (
          <ConfirmActions
            cancelRef={cancelRef}
            cancel={{
              label: strings.cancelAction,
              ariaLabel: strings.cancelActionNamed({
                subject: strings.resetPasswordTriggerNamed({ user: current.user }),
              }),
              disabled: busy,
              onClick: () => {
                if (!busy) {
                  onClose();
                }
              },
            }}
            confirm={{
              label: busy ? strings.resettingPasswordAction : strings.confirmResetPasswordAction,
              ariaLabel: busy
                ? strings.resetPasswordBusyNamed({ user: current.user })
                : strings.resetPasswordConfirmNamed({ user: current.user }),
              disabled: busy,
              onClick: onConfirm,
            }}
          />
        );
      }
    }
  }

  function renderRevealed(current: Extract<ResetDialog, { phase: 'revealed' }>): JSX.Element {
    return (
      <>
        <label htmlFor="users-reset-password" className="reset-dialog__label">
          {strings.passwordLabel}
        </label>
        <div className="reset-dialog__row">
          <input
            id="users-reset-password"
            type="text"
            readOnly
            autoComplete="off"
            spellCheck={false}
            className="reset-dialog__password"
            value={current.password}
            onFocus={(event) => event.currentTarget.select()}
          />
          <button
            ref={copyButtonRef}
            type="button"
            className="modal__button modal__button--secondary"
            onClick={() => copy(current.password)}
          >
            {strings.copyPassword}
          </button>
        </div>
        {copyStatus !== null && (
          <p
            role="status"
            key={copyStatus.count}
            className={
              copyStatus.kind === 'copied' ? 'reset-dialog__copy-ok' : 'reset-dialog__copy-fallback'
            }
          >
            {copyStatus.kind === 'copied'
              ? strings.passwordCopied
              : strings.passwordCopyUnavailable}
          </p>
        )}
      </>
    );
  }

  return (
    <Modal
      open={dialog !== null}
      title={
        dialog?.phase === 'revealed'
          ? strings.passwordGroupHeading
          : strings.resetPasswordDialogTitle
      }
      onDismiss={onClose}
      dismissible={dialog?.phase !== 'busy'}
      backdropDismiss={dialog?.phase !== 'revealed'}
      closeOnHashChange={false}
      busy={dialog?.phase === 'busy'}
      initialFocusRef={cancelRef}
      returnFocusRef={returnFocusRef}
      fallbackFocusRef={fallbackFocusRef}
      actions={dialog === null ? null : renderActions(dialog)}
    >
      {/* One persistent live region: mounted empty so the busy to revealed change is announced. */}
      <div role="status" className="reset-dialog__status">
        {dialog?.phase === 'revealed' && (
          <p>{strings.resetPasswordRevealLead({ user: dialog.user })}</p>
        )}
      </div>
      {dialog !== null && <ConfirmItem label={strings.dialogItemUser} value={dialog.user} />}
      {(dialog?.phase === 'confirm' || dialog?.phase === 'busy') && (
        <ConfirmNotice>{strings.resetPasswordNotice}</ConfirmNotice>
      )}
      {dialog?.phase === 'failed' && (
        <p role="alert" className="modal__alert">
          {dialog.message}
        </p>
      )}
      {dialog?.phase === 'revealed' && renderRevealed(dialog)}
    </Modal>
  );
}
