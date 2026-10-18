import { useEffect, useRef, useState } from 'react';
import type { FormEvent, JSX } from 'react';

import { ApiError } from '../api/client';
import type { CreateDestinationParams, DestinationType } from '../api/deliveryClient';
import { strings } from '../strings';
import { isValidDestinationName } from '../validation';
import { fieldsFor, generateSigningSecret, validateField } from './destinationFields';
import './DestinationForm.css';

const DESTINATION_TYPES: DestinationType[] = ['ntfy', 'discord', 'webhook'];
const SIGNING_SECRET_KEY = 'signing_secret';

/** What the form hands to its owner: the create call and any generated secret to reveal. */
export interface AddDestinationSubmission {
  params: CreateDestinationParams;
  /** The signing secret iff it is exactly the value the Generate button produced. */
  generatedSecret: string | null;
}

interface AddDestinationFormProps {
  onAdd: (submission: AddDestinationSubmission) => Promise<void>;
  onCancel: () => void;
}

/**
 * Add-destination form. Fields depend on the type. Secret inputs are masked
 * (`autocomplete="new-password"`) except that Generate fills the signing secret
 * and shows it as plain text until the form is submitted or cancelled. The
 * parent collapses the form on success and renders any reveal outside it.
 */
export function AddDestinationForm({ onAdd, onCancel }: AddDestinationFormProps): JSX.Element {
  const [name, setName] = useState('');
  const [type, setType] = useState<DestinationType>('ntfy');
  const [values, setValues] = useState<Record<string, string>>({});
  const [generatedSecret, setGeneratedSecret] = useState<string | null>(null);
  const [signingVisible, setSigningVisible] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const nameRef = useRef<HTMLInputElement | null>(null);

  useEffect(() => {
    nameRef.current?.focus();
  }, []);

  const fields = fieldsFor(type);

  function setValue({ key, value }: { key: string; value: string }) {
    setValues((current) => ({ ...current, [key]: value }));
  }

  function handleGenerate() {
    const secret = generateSigningSecret();
    setGeneratedSecret(secret);
    setSigningVisible(true);
    setValue({ key: SIGNING_SECRET_KEY, value: secret });
  }

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submitting) {
      return;
    }
    const trimmedName = name.trim();
    if (!isValidDestinationName(trimmedName)) {
      setError(strings.invalidDestinationName);
      return;
    }

    const config: Record<string, string> = {};
    const secrets: Record<string, string> = {};
    for (const field of fields) {
      const value = values[field.key] ?? '';
      const problem = validateField({ field, value, required: field.requiredOnAdd });
      if (problem !== null) {
        setError(problem);
        return;
      }
      if (field.section === 'config') {
        config[field.key] = value.trim();
      } else if (value !== '') {
        secrets[field.key] = value;
      }
    }

    const signingSecret = secrets[SIGNING_SECRET_KEY];
    const revealed =
      type === 'webhook' && signingSecret !== undefined && signingSecret === generatedSecret
        ? signingSecret
        : null;

    setError(null);
    setSubmitting(true);
    setSigningVisible(false);
    onAdd({ params: { name: trimmedName, type, config, secrets }, generatedSecret: revealed })
      .catch((rejection: unknown) => {
        setError(rejection instanceof ApiError ? rejection.message : strings.genericError);
      })
      .finally(() => {
        setSubmitting(false);
      });
  }

  return (
    <section className="destination-form" aria-labelledby="add-destination-heading">
      <h3 id="add-destination-heading">{strings.addDestinationHeading}</h3>
      <form onSubmit={handleSubmit} noValidate>
        <div className="destination-form__grid">
          <div className="destination-form__field">
            <label htmlFor="add-dest-name">{strings.destNameLabel}</label>
            <input
              ref={nameRef}
              id="add-dest-name"
              type="text"
              placeholder={strings.destNamePlaceholder}
              value={name}
              onChange={(event) => setName(event.target.value)}
            />
          </div>
          <div className="destination-form__field">
            <label htmlFor="add-dest-type">{strings.destTypeLabel}</label>
            <select
              id="add-dest-type"
              value={type}
              onChange={(event) => {
                setType(event.target.value as DestinationType);
                setError(null);
              }}
            >
              {DESTINATION_TYPES.map((option) => (
                <option key={option} value={option}>
                  {strings.destinationTypeLabels[option]}
                </option>
              ))}
            </select>
          </div>
          {fields.map((field) => {
            const inputId = `add-dest-${field.key}`;
            const isSigning = field.key === SIGNING_SECRET_KEY;
            const isSecret = field.section === 'secret';
            const masked = isSecret && !(isSigning && signingVisible);
            return (
              <div key={field.key} className="destination-form__field">
                <label htmlFor={inputId}>{field.label}</label>
                <div className="destination-form__row">
                  <input
                    id={inputId}
                    type={masked ? 'password' : 'text'}
                    autoComplete={isSecret ? 'new-password' : 'off'}
                    placeholder={isSigning ? strings.destSigningSecretPlaceholder : undefined}
                    value={values[field.key] ?? ''}
                    onChange={(event) => setValue({ key: field.key, value: event.target.value })}
                  />
                  {isSigning && (
                    <button
                      type="button"
                      className="destination-form__button"
                      onClick={handleGenerate}
                    >
                      {strings.destGenerateAction}
                    </button>
                  )}
                </div>
              </div>
            );
          })}
        </div>
        <p className="destination-form__hint">{strings.destFieldsHint}</p>
        {error !== null && (
          <p role="alert" className="destination-form__error">
            {error}
          </p>
        )}
        <div className="destination-form__actions">
          <button
            type="submit"
            className="destination-form__button destination-form__button--primary"
            aria-disabled={submitting ? 'true' : undefined}
          >
            {submitting ? strings.addingDestination : strings.addDestinationAction}
          </button>
          <button
            type="button"
            className="destination-form__button destination-form__button--ghost"
            onClick={() => {
              setSigningVisible(false);
              onCancel();
            }}
          >
            {strings.cancelAction}
          </button>
        </div>
      </form>
    </section>
  );
}
