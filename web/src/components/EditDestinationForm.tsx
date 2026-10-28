import { useEffect, useRef, useState } from 'react';
import type { FormEvent, JSX, RefObject } from 'react';

import { ApiError } from '../api/client';
import type { DestinationSummary, RotateSecretResult } from '../api/deliveryClient';
import { strings } from '../strings';
import { fieldsFor, validateField } from './destinationFields';
import type { ConfirmStep } from './EditModal';
import './DestinationForm.css';

const OVERLAP_HOURS = [1, 24, 168] as const;
const DEFAULT_OVERLAP_HOURS = 24;
const PREVIOUS_EXPIRES_KEY = 'previous_secret_expires_at';
const SIGNING_SECRET_KEY = 'signing_secret';

/** The changed fields an edit sends; empty/omitted sections are not sent. */
export interface EditDestinationSubmission {
  config?: Record<string, string>;
  secrets?: Record<string, string>;
}

interface EditDestinationFormProps {
  destination: DestinationSummary;
  saving: boolean;
  rotating: boolean;
  onSave: (submission: EditDestinationSubmission) => Promise<void>;
  onRotate: (overlapHours: number) => Promise<RotateSecretResult>;
  /** Close the editor (Close button or an untouched Save); the dialog's guarded close. */
  onClose: () => void;
  /** Asks the dialog to show its in-dialog confirm step (Rotate). */
  onRequestConfirm: (step: ConfirmStep) => void;
  /** Reports whether the form holds unsaved edits (false again on unmount). */
  onDirtyChange?: (dirty: boolean) => void;
  /**
   * Called after a successful rotate once the form has re-synced; `dirty` is judged against the
   * re-synced baseline using the values at resolution time.
   */
  onRotated?: (outcome: { dirty: boolean }) => void;
  /** The dialog's initial-focus target: the first field. */
  firstFieldRef?: RefObject<HTMLInputElement | null>;
}

type Values = Record<string, string>;

function previousSecretStillValid(expiresAt: string | undefined): boolean {
  if (expiresAt === undefined) {
    return false;
  }
  const parsed = Date.parse(expiresAt);
  return !Number.isNaN(parsed) && parsed > Date.now();
}

/** True when a config field differs from the baseline or a typed secret (other than `ignore`) exists. */
function hasUnsavedEdits({
  fields,
  baseline,
  configValues,
  secretValues,
  ignoreSecret,
}: {
  fields: ReturnType<typeof fieldsFor>;
  baseline: Values;
  configValues: Values;
  secretValues: Values;
  ignoreSecret?: string;
}): boolean {
  return fields.some((field) =>
    field.section === 'config'
      ? (configValues[field.key] ?? '').trim() !== (baseline[field.key] ?? '')
      : field.key !== ignoreSecret && (secretValues[field.key] ?? '') !== '',
  );
}

/**
 * Edit form for one destination, rendered inside the shared edit dialog. Config fields are prefilled; secrets are
 * write-only (blank = keep, masked value only in the placeholder). The baseline
 * the changed-keys diff is computed against is snapshotted when the editor
 * opens and a list refresh never touches typed values. The single exception is
 * a successful Rotate: only untouched config fields and the baseline re-sync
 * from the returned destination, and the typed signing secret is cleared so a
 * later Save can never overwrite the freshly rotated secret.
 */
export function EditDestinationForm({
  destination,
  saving,
  rotating,
  onSave,
  onRotate,
  onClose,
  onRequestConfirm,
  onDirtyChange,
  onRotated,
  firstFieldRef: firstFieldRefProp,
}: EditDestinationFormProps): JSX.Element {
  const fields = fieldsFor(destination.type);
  const [baselineConfig, setBaselineConfig] = useState<Record<string, string>>(
    () => destination.config,
  );
  const [configValues, setConfigValues] = useState<Record<string, string>>(() => ({
    ...destination.config,
  }));
  const [secretValues, setSecretValues] = useState<Record<string, string>>({});
  const [overlapHours, setOverlapHours] = useState<number>(DEFAULT_OVERLAP_HOURS);
  const [error, setError] = useState<string | null>(null);
  const touchedConfig = useRef(new Set<string>());
  const localFirstFieldRef = useRef<HTMLInputElement | null>(null);
  const firstFieldRef = firstFieldRefProp ?? localFirstFieldRef;

  // Mirrors of the typed values for the rotate callback, which resolves after the click's render.
  const configValuesRef = useRef(configValues);
  const secretValuesRef = useRef(secretValues);
  useEffect(() => {
    configValuesRef.current = configValues;
    secretValuesRef.current = secretValues;
  });

  const dirty = hasUnsavedEdits({ fields, baseline: baselineConfig, configValues, secretValues });
  const onDirtyChangeRef = useRef(onDirtyChange);
  useEffect(() => {
    onDirtyChangeRef.current = onDirtyChange;
  });
  useEffect(() => {
    onDirtyChangeRef.current?.(dirty);
  }, [dirty]);
  useEffect(
    () => () => {
      onDirtyChangeRef.current?.(false);
    },
    [],
  );

  function handleSave(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    // A save and a rotate never overlap: the save would close the dialog under the rotate.
    if (saving || rotating) {
      return;
    }

    const changedConfig: Record<string, string> = {};
    const changedSecrets: Record<string, string> = {};
    for (const field of fields) {
      if (field.section === 'config') {
        const value = configValues[field.key] ?? '';
        if (value.trim() === (baselineConfig[field.key] ?? '')) {
          continue;
        }
        const problem = validateField({ field, value, required: true });
        if (problem !== null) {
          setError(problem);
          return;
        }
        changedConfig[field.key] = value.trim();
      } else {
        const value = secretValues[field.key] ?? '';
        if (value === '') {
          continue;
        }
        const problem = validateField({ field, value, required: false });
        if (problem !== null) {
          setError(problem);
          return;
        }
        changedSecrets[field.key] = value;
      }
    }

    const hasConfig = Object.keys(changedConfig).length > 0;
    const hasSecrets = Object.keys(changedSecrets).length > 0;
    setError(null);
    if (!hasConfig && !hasSecrets) {
      onClose();
      return;
    }
    onSave({
      config: hasConfig ? changedConfig : undefined,
      secrets: hasSecrets ? changedSecrets : undefined,
    }).catch((rejection: unknown) => {
      setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
    });
  }

  function handleRotate() {
    setError(null);
    onRotate(overlapHours)
      .then((result) => {
        const nextConfig = result.destination.config;
        setBaselineConfig(nextConfig);
        setConfigValues((current) => {
          const resynced = { ...current };
          for (const field of fields) {
            if (field.section === 'config' && !touchedConfig.current.has(field.key)) {
              resynced[field.key] = nextConfig[field.key] ?? '';
            }
          }
          return resynced;
        });
        setSecretValues((current) => ({ ...current, [SIGNING_SECRET_KEY]: '' }));
        // Judged against the new baseline with the values typed while the request was pending:
        // untouched fields now equal `nextConfig`, so only touched fields and non-signing
        // secrets can still be unsaved.
        const latestConfig = { ...configValuesRef.current };
        for (const field of fields) {
          if (field.section === 'config' && !touchedConfig.current.has(field.key)) {
            latestConfig[field.key] = nextConfig[field.key] ?? '';
          }
        }
        onRotated?.({
          dirty: hasUnsavedEdits({
            fields,
            baseline: nextConfig,
            configValues: latestConfig,
            secretValues: secretValuesRef.current,
            ignoreSecret: SIGNING_SECRET_KEY,
          }),
        });
      })
      .catch((rejection: unknown) => {
        setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
      });
  }

  const isWebhook = destination.type === 'webhook';
  const invalidatesPrevious = previousSecretStillValid(baselineConfig[PREVIOUS_EXPIRES_KEY]);

  function requestRotate() {
    if (rotating || saving) {
      return;
    }
    const confirmLabel = strings.confirmRotateDestination({ name: destination.name });
    onRequestConfirm({
      message: invalidatesPrevious
        ? `${confirmLabel}. ${strings.rotateInvalidatesPrevious}`
        : confirmLabel,
      confirmLabel,
      cancelLabel: strings.cancelAction,
      onConfirm: handleRotate,
      onCancel: () => undefined,
    });
  }

  return (
    <div className="destination-form">
      <form
        aria-label={strings.editDestination({ name: destination.name })}
        onSubmit={handleSave}
        noValidate
      >
        <div className="destination-form__grid">
          {fields.map((field, index) => {
            const inputId = `edit-dest-${field.key}-${destination.name}`;
            const isSecret = field.section === 'secret';
            const masked = destination.secrets[field.key];
            return (
              <div key={field.key} className="destination-form__field">
                <label htmlFor={inputId}>{field.label}</label>
                <input
                  ref={index === 0 ? firstFieldRef : undefined}
                  id={inputId}
                  type={isSecret ? 'password' : 'text'}
                  autoComplete={isSecret ? 'new-password' : 'off'}
                  placeholder={
                    isSecret && masked !== undefined
                      ? `${masked} ${strings.destLeaveBlank}`
                      : undefined
                  }
                  value={
                    isSecret ? (secretValues[field.key] ?? '') : (configValues[field.key] ?? '')
                  }
                  onChange={(event) => {
                    const { value } = event.target;
                    if (isSecret) {
                      setSecretValues((current) => ({ ...current, [field.key]: value }));
                    } else {
                      touchedConfig.current.add(field.key);
                      setConfigValues((current) => ({ ...current, [field.key]: value }));
                    }
                  }}
                />
              </div>
            );
          })}
        </div>
        <p className="destination-form__hint">{strings.destEditSecretsHint}</p>

        <div className="destination-form__actions">
          <button
            type="submit"
            className="destination-form__button destination-form__button--primary"
            aria-disabled={saving || rotating ? 'true' : undefined}
          >
            {saving
              ? strings.savingDestination
              : strings.saveDestination({ name: destination.name })}
          </button>
          {isWebhook && (
            <>
              <label
                htmlFor={`rotate-overlap-${destination.name}`}
                className="destination-form__inline-label"
              >
                {strings.rotateOverlapLabel}
              </label>
              <select
                id={`rotate-overlap-${destination.name}`}
                className="destination-form__select"
                value={overlapHours}
                onChange={(event) => setOverlapHours(Number(event.target.value))}
              >
                {OVERLAP_HOURS.map((hours) => (
                  <option key={hours} value={hours}>
                    {strings.rotateOverlapOptions[hours]}
                  </option>
                ))}
              </select>
              <button
                type="button"
                className="destination-form__button destination-form__button--danger"
                aria-disabled={rotating || saving ? 'true' : undefined}
                onClick={requestRotate}
              >
                {rotating
                  ? strings.rotatingDestination
                  : strings.rotateDestination({ name: destination.name })}
              </button>
            </>
          )}
          <button
            type="button"
            className="destination-form__button destination-form__button--ghost"
            aria-disabled={rotating || saving ? 'true' : undefined}
            onClick={onClose}
          >
            {strings.closeEditAction}
          </button>
        </div>
        {isWebhook && <p className="destination-form__hint">{strings.rotateHint}</p>}

        {error !== null && (
          <p role="alert" className="destination-form__error">
            {error}
          </p>
        )}
      </form>
    </div>
  );
}
