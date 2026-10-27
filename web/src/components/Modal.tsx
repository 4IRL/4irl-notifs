import { useEffect, useId, useRef } from 'react';
import type { JSX, KeyboardEvent, ReactNode, RefObject } from 'react';
import { createPortal } from 'react-dom';

import { strings } from '../strings';
import './Modal.css';

/** Data attributes stamped on .modal that name the row owning a portaled dialog. */
export const OWNER_ROW_ATTRIBUTE = 'data-owner-row-attribute';
export const OWNER_ROW_ID = 'data-owner-row-id';

export interface ModalProps {
  open: boolean;
  /** Rendered as the dialog's h2 and used as its accessible name. */
  title: string;
  /** Called by the X, Escape, an allowed backdrop click and (optionally) hashchange. */
  onDismiss: () => void;
  /** Default true. False ignores X, Escape and backdrop clicks and hides the X. */
  dismissible?: boolean;
  /** Default true. False ignores backdrop clicks only. */
  backdropDismiss?: boolean;
  /** Default true. A window `hashchange` calls onDismiss (even if not dismissible). */
  closeOnHashChange?: boolean;
  /** Stamped on .modal so a focused element inside the portal can resolve to its row. */
  ownerRow?: { attribute: string; id: string };
  /** Default: first focusable in the body or actions. */
  initialFocusRef?: RefObject<HTMLElement | null>;
  /** Default: the element focused when the dialog opened. */
  returnFocusRef?: RefObject<HTMLElement | null>;
  /** Focused after close when the return target is missing, disconnected or hidden. */
  fallbackFocusRef?: RefObject<HTMLElement | null>;
  busy?: boolean;
  children: ReactNode;
  actions: ReactNode;
}

const FOCUSABLE_PARTS = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
];
const FOCUSABLE_SELECTOR = FOCUSABLE_PARTS.join(',');
const INITIAL_FOCUS_SELECTOR = ['.modal__body', '.modal__actions']
  .flatMap((region) => FOCUSABLE_PARTS.map((part) => `${region} ${part}`))
  .join(',');

function usable(element: HTMLElement | null | undefined): element is HTMLElement {
  return Boolean(element && element.isConnected && element.closest('[hidden]') === null);
}

function tabbables(dialog: HTMLElement): HTMLElement[] {
  return Array.from(dialog.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
    (element) => element.closest('[hidden]') === null,
  );
}

export function Modal({
  open,
  title,
  onDismiss,
  dismissible = true,
  backdropDismiss = true,
  closeOnHashChange = true,
  ownerRow,
  initialFocusRef,
  returnFocusRef,
  fallbackFocusRef,
  busy = false,
  children,
  actions,
}: ModalProps): JSX.Element | null {
  const titleId = useId();
  const bodyId = useId();
  const dialogRef = useRef<HTMLDivElement | null>(null);
  const pointerDownOnBackdrop = useRef(false);
  const pointerUpOnBackdrop = useRef(false);

  // Latest props for the open/close effect, which must not re-run when they change.
  const latest = useRef({
    onDismiss,
    dismissible,
    closeOnHashChange,
    initialFocusRef,
    returnFocusRef,
  });
  const fallbackRef = useRef(fallbackFocusRef);
  // Declared before the open effect so it has run by the time that effect reads the refs.
  // Invariant: consumers keep returnFocusRef/fallbackFocusRef object identities stable.
  useEffect(() => {
    latest.current = { onDismiss, dismissible, closeOnHashChange, initialFocusRef, returnFocusRef };
    fallbackRef.current = fallbackFocusRef;
  });

  useEffect(() => {
    if (!open) return undefined;

    const active = document.activeElement;
    // body/documentElement mean "nothing focused": focusing them is a no-op, so use the fallback.
    const opener =
      active instanceof HTMLElement &&
      active !== document.body &&
      active !== document.documentElement
        ? active
        : null;
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    const appRoot = document.getElementById('root');
    const rootWasInert = appRoot?.hasAttribute('inert') ?? false;
    appRoot?.setAttribute('inert', '');

    const dialog = dialogRef.current;
    const initial =
      latest.current.initialFocusRef?.current ??
      dialog?.querySelector<HTMLElement>(INITIAL_FOCUS_SELECTOR) ??
      dialog;
    initial?.focus();

    const onHashChange = () => {
      if (latest.current.closeOnHashChange) latest.current.onDismiss();
    };
    window.addEventListener('hashchange', onHashChange);

    // #root is inert, so if focus is lost to <body> the dialog's own handler never sees keys.
    const onDocumentKeyDown = (event: globalThis.KeyboardEvent) => {
      const current = dialogRef.current;
      if (!current || (event.target instanceof Node && current.contains(event.target))) return;
      if (event.key === 'Escape') {
        event.stopPropagation();
        event.preventDefault();
        if (latest.current.dismissible) latest.current.onDismiss();
      } else if (event.key === 'Tab') {
        event.preventDefault();
        (tabbables(current)[0] ?? current).focus();
      }
    };
    document.addEventListener('keydown', onDocumentKeyDown);

    return () => {
      window.removeEventListener('hashchange', onHashChange);
      document.removeEventListener('keydown', onDocumentKeyDown);
      document.body.style.overflow = previousOverflow;
      if (!rootWasInert) appRoot?.removeAttribute('inert');

      const target = latest.current.returnFocusRef?.current ?? opener;
      if (usable(target)) {
        target.focus();
      } else {
        const fallback = fallbackRef.current?.current;
        if (fallback && fallback.isConnected) fallback.focus();
      }
    };
  }, [open]);

  if (!open) return null;

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      event.stopPropagation();
      event.preventDefault();
      if (dismissible) onDismiss();
      return;
    }
    if (event.key !== 'Tab') return;
    const dialog = dialogRef.current;
    if (!dialog) return;
    const focusables = tabbables(dialog);
    if (focusables.length === 0) {
      event.preventDefault();
      dialog.focus();
      return;
    }
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    const active = document.activeElement;
    if (event.shiftKey && (active === first || active === dialog)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && (active === last || active === dialog)) {
      event.preventDefault();
      first.focus();
    }
  };

  return createPortal(
    <div
      className="modal__backdrop"
      onPointerDown={(event) => {
        pointerDownOnBackdrop.current = event.target === event.currentTarget;
        pointerUpOnBackdrop.current = false;
      }}
      onPointerUp={(event) => {
        pointerUpOnBackdrop.current = event.target === event.currentTarget;
      }}
      onMouseDown={(event) => {
        // Keep focus inside the dialog so Escape and Tab still reach it.
        if (event.target === event.currentTarget) event.preventDefault();
      }}
      onClick={(event) => {
        const startedOnBackdrop = pointerDownOnBackdrop.current;
        const endedOnBackdrop = pointerUpOnBackdrop.current;
        pointerDownOnBackdrop.current = false;
        pointerUpOnBackdrop.current = false;
        if (
          event.target === event.currentTarget &&
          startedOnBackdrop &&
          endedOnBackdrop &&
          dismissible &&
          backdropDismiss
        ) {
          onDismiss();
        }
      }}
    >
      <div
        ref={dialogRef}
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={bodyId}
        aria-busy={busy ? true : undefined}
        tabIndex={-1}
        {...{
          [OWNER_ROW_ATTRIBUTE]: ownerRow?.attribute,
          [OWNER_ROW_ID]: ownerRow?.id,
        }}
        onKeyDown={handleKeyDown}
      >
        <div className="modal__header">
          <h2 id={titleId} className="modal__title">
            {title}
          </h2>
          {dismissible ? (
            <button
              type="button"
              className="modal__close"
              aria-label={strings.closeDialog}
              onClick={onDismiss}
            >
              ×
            </button>
          ) : null}
        </div>
        <div id={bodyId} className="modal__body">
          {children}
        </div>
        <div className="modal__actions">{actions}</div>
      </div>
    </div>,
    document.body,
  );
}
