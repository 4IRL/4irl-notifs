import { Fragment, useEffect, useRef } from 'react';
import type { JSX } from 'react';

import type { DestinationSummary, RotateSecretResult } from '../api/deliveryClient';
import { strings } from '../strings';
import { ConfirmButton } from './ConfirmButton';
import { hasUnreadableSecrets } from './destinationFields';
import { formatUtc } from './formatUtc';
import { EditDestinationForm } from './EditDestinationForm';
import type { EditDestinationSubmission } from './EditDestinationForm';
import './DestinationsTable.css';

/** Mutating actions that carry a per-row busy flag. */
export type DestinationAction = 'test' | 'toggle' | 'delete' | 'save' | 'rotate';

/** Outcome of a send-test, shown under the row actions. */
export type TestResult =
  | { kind: 'delivery'; state: string; statusCode: number | null; deadReason: string | null }
  | { kind: 'none' }
  | { kind: 'error'; message: string };

/** A signing secret shown exactly once, keyed by destination name in the owner. */
export interface SecretRevealState {
  secret: string;
  /** RFC3339 expiry of the previous secret after a rotation; null for a fresh add. */
  previousExpiresAt: string | null;
  source: 'add' | 'rotate';
}

interface DestinationsTableProps {
  destinations: DestinationSummary[];
  editingName: string | null;
  isBusy: (params: { action: DestinationAction; name: string }) => boolean;
  rowErrors: Record<string, string>;
  testResults: Record<string, TestResult>;
  reveals: Record<string, SecretRevealState>;
  /** Registers a focusable control under a key so the owner can move focus to it. */
  registerControl: (params: { key: string; element: HTMLElement | null }) => void;
  onToggleEditor: (name: string) => void;
  onCloseEditor: (name: string) => void;
  onSendTest: (name: string) => void;
  onSetEnabled: (params: { name: string; enabled: boolean }) => void;
  onDelete: (name: string) => void;
  onDismissReveal: (name: string) => void;
  onSave: (params: { name: string } & EditDestinationSubmission) => Promise<void>;
  onRotate: (params: { name: string; overlapHours: number }) => Promise<RotateSecretResult>;
}

const COLUMN_COUNT = 5;

function targetOf(destination: DestinationSummary): string {
  if (destination.type === 'ntfy') {
    return strings.destinationTarget({ topic: destination.config.topic ?? '' });
  }
  const key = destination.type === 'discord' ? 'webhook_url' : 'url';
  return destination.secrets[key] ?? '';
}

function stateLabel(state: string): { label: string; modifier: string } {
  switch (state) {
    case 'delivered':
      return { label: strings.testStateDelivered, modifier: 'ok' };
    case 'pending':
      return { label: strings.testStatePending, modifier: 'neutral' };
    case 'dead':
      return { label: strings.testStateDead, modifier: 'fail' };
    default:
      return { label: state, modifier: 'neutral' };
  }
}

function TestResultLine({ result }: { result: TestResult | undefined }): JSX.Element {
  let content: JSX.Element | null = null;
  if (result?.kind === 'delivery') {
    const pill = stateLabel(result.state);
    let detail: string | null = null;
    if (result.statusCode !== null) {
      detail = strings.testHttpStatus({ code: result.statusCode });
    } else if (result.state === 'dead') {
      detail = result.deadReason;
    }
    content = (
      <>
        <span className={`destinations__pill destinations__pill--${pill.modifier}`}>
          {pill.label}
        </span>
        {detail !== null && <span>{detail}</span>}
      </>
    );
  } else if (result?.kind === 'none') {
    content = <span>{strings.testNoDelivery}</span>;
  }
  return (
    <>
      {/* Always rendered, text-only changes, so assistive tech announces the result. */}
      <div role="status" aria-live="polite" className="destinations__result">
        {content}
      </div>
      {result?.kind === 'error' && (
        <p role="alert" className="destinations__row-error">
          {result.message}
        </p>
      )}
    </>
  );
}

function RevealBox({
  name,
  reveal,
  onDismiss,
}: {
  name: string;
  reveal: SecretRevealState;
  onDismiss: () => void;
}): JSX.Element {
  const containerRef = useRef<HTMLDivElement | null>(null);

  // The container takes focus whenever a (new) secret appears.
  useEffect(() => {
    containerRef.current?.focus();
  }, [reveal.secret]);

  return (
    <div ref={containerRef} tabIndex={-1} className="destinations__reveal" role="status">
      <p>
        {reveal.source === 'rotate'
          ? strings.revealLeadNew({ name })
          : strings.revealLeadAdded({ name })}
      </p>
      <p className="destinations__secret">{reveal.secret}</p>
      {reveal.previousExpiresAt !== null && (
        <p className="destinations__hint">
          {strings.revealPreviousValidUntil({ when: formatUtc(reveal.previousExpiresAt) })}
        </p>
      )}
      <button type="button" className="destinations__button" onClick={onDismiss}>
        {strings.revealDismiss}
      </button>
    </div>
  );
}

export function DestinationsTable({
  destinations,
  editingName,
  isBusy,
  rowErrors,
  testResults,
  reveals,
  registerControl,
  onToggleEditor,
  onCloseEditor,
  onSendTest,
  onSetEnabled,
  onDelete,
  onDismissReveal,
  onSave,
  onRotate,
}: DestinationsTableProps): JSX.Element {
  return (
    <div className="destinations__scroll">
      <table className="destinations__table">
        <thead>
          <tr>
            <th scope="col">{strings.columnDestName}</th>
            <th scope="col">{strings.columnDestType}</th>
            <th scope="col">{strings.columnDestTarget}</th>
            <th scope="col">{strings.columnDestStatus}</th>
            <th scope="col" aria-label={strings.columnDestActions} />
          </tr>
        </thead>
        <tbody>
          {destinations.map((destination) => {
            const { name } = destination;
            const unreadable = hasUnreadableSecrets(destination.secrets);
            const isEditing = editingName === name;
            const reveal = reveals[name];
            const reasonId = `dest-reason-${name}`;
            const testBusy = isBusy({ action: 'test', name });
            const testBlocked = !destination.enabled || testBusy;
            const toggleBusy = isBusy({ action: 'toggle', name });
            const rowError = rowErrors[name];
            return (
              <Fragment key={name}>
                <tr>
                  <td className="destinations__name">{name}</td>
                  <td>{strings.destinationTypeLabels[destination.type]}</td>
                  <td className="destinations__target">
                    {unreadable ? (
                      <>
                        <span className="destinations__pill destinations__pill--fail">
                          {strings.destinationSecretsUnreadable}
                        </span>
                        <span className="destinations__sub">
                          {strings.destinationSecretsUnreadableHint}
                        </span>
                      </>
                    ) : (
                      targetOf(destination)
                    )}
                  </td>
                  <td>
                    <span
                      className={`destinations__pill destinations__pill--${destination.enabled ? 'ok' : 'fail'}`}
                    >
                      {destination.enabled
                        ? strings.destinationEnabled
                        : strings.destinationDisabled}
                    </span>
                    {!destination.enabled && (
                      <span id={reasonId} className="destinations__sub">
                        {destination.disabledReason ?? strings.destinationDisabledNoReason}
                      </span>
                    )}
                  </td>
                  <td>
                    <div className="destinations__actions">
                      <button
                        ref={(element) => registerControl({ key: `first:${name}`, element })}
                        type="button"
                        className="destinations__button"
                        aria-disabled={testBlocked ? 'true' : undefined}
                        aria-describedby={!destination.enabled ? reasonId : undefined}
                        onClick={() => {
                          if (testBlocked) {
                            return;
                          }
                          onSendTest(name);
                        }}
                      >
                        {testBusy ? strings.sendTestToSending : strings.sendTestTo({ name })}
                      </button>
                      {!unreadable && (
                        <button
                          ref={(element) => registerControl({ key: `edit:${name}`, element })}
                          type="button"
                          className="destinations__button"
                          aria-expanded={isEditing}
                          onClick={() => onToggleEditor(name)}
                        >
                          {isEditing
                            ? strings.closeDestination({ name })
                            : strings.editDestination({ name })}
                        </button>
                      )}
                      {destination.enabled ? (
                        <ConfirmButton
                          triggerLabel={strings.disableDestination({ name })}
                          confirmLabel={strings.confirmDisableDestination({ name })}
                          className="destinations__button destinations__button--danger"
                          busy={toggleBusy}
                          busyLabel={strings.disablingDestination}
                          onConfirm={() => onSetEnabled({ name, enabled: false })}
                        />
                      ) : (
                        <button
                          ref={(element) => registerControl({ key: `enable:${name}`, element })}
                          type="button"
                          className="destinations__button"
                          aria-disabled={toggleBusy ? 'true' : undefined}
                          onClick={() => {
                            if (toggleBusy) {
                              return;
                            }
                            onSetEnabled({ name, enabled: true });
                          }}
                        >
                          {toggleBusy
                            ? strings.enablingDestination
                            : strings.enableDestination({ name })}
                        </button>
                      )}
                      <ConfirmButton
                        triggerLabel={strings.deleteDestination({ name })}
                        confirmLabel={strings.confirmDeleteDestination({ name })}
                        className="destinations__button destinations__button--danger"
                        busy={isBusy({ action: 'delete', name })}
                        busyLabel={strings.deletingDestination}
                        onConfirm={() => onDelete(name)}
                      />
                    </div>
                    <TestResultLine result={testResults[name]} />
                    {rowError !== undefined && (
                      <p role="alert" className="destinations__row-error">
                        {rowError}
                      </p>
                    )}
                  </td>
                </tr>
                {isEditing && !unreadable && (
                  <tr>
                    <td colSpan={COLUMN_COUNT}>
                      <EditDestinationForm
                        destination={destination}
                        saving={isBusy({ action: 'save', name })}
                        rotating={isBusy({ action: 'rotate', name })}
                        onSave={(submission) => onSave({ name, ...submission })}
                        onRotate={(overlapHours) => onRotate({ name, overlapHours })}
                        onClose={() => onCloseEditor(name)}
                      />
                    </td>
                  </tr>
                )}
                {reveal !== undefined && (
                  <tr>
                    <td colSpan={COLUMN_COUNT}>
                      <RevealBox
                        name={name}
                        reveal={reveal}
                        onDismiss={() => onDismissReveal(name)}
                      />
                    </td>
                  </tr>
                )}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
