import type { DestinationType } from '../api/deliveryClient';
import { strings } from '../strings';
import {
  hasSurroundingWhitespace,
  isHttpsUrl,
  isValidNtfyTopic,
  isValidSigningSecret,
} from '../validation';

/** Where a field's value is sent: the destination `config` or its write-only `secrets`. */
export type FieldSection = 'config' | 'secret';

/** One input of the add/edit destination forms. */
export interface DestinationField {
  key: string;
  section: FieldSection;
  label: string;
  /** Required when creating; edits only send changed or non-empty values. */
  requiredOnAdd: boolean;
}

const NTFY_FIELDS: DestinationField[] = [
  { key: 'topic', section: 'config', label: strings.destTopicLabel, requiredOnAdd: true },
  {
    key: 'access_token',
    section: 'secret',
    label: strings.destAccessTokenLabel,
    requiredOnAdd: false,
  },
];

const DISCORD_FIELDS: DestinationField[] = [
  {
    key: 'webhook_url',
    section: 'secret',
    label: strings.destDiscordUrlLabel,
    requiredOnAdd: true,
  },
];

const WEBHOOK_FIELDS: DestinationField[] = [
  { key: 'url', section: 'secret', label: strings.destWebhookUrlLabel, requiredOnAdd: true },
  {
    key: 'signing_secret',
    section: 'secret',
    label: strings.destSigningSecretLabel,
    requiredOnAdd: true,
  },
];

/** The inputs a destination of `type` takes, in display order. */
export function fieldsFor(type: DestinationType): DestinationField[] {
  switch (type) {
    case 'ntfy':
      return NTFY_FIELDS;
    case 'discord':
      return DISCORD_FIELDS;
    case 'webhook':
      return WEBHOOK_FIELDS;
  }
}

const URL_SECRET_KEYS = new Set(['webhook_url', 'url']);

/**
 * Validates one field value, returning an inline error message or null. Config
 * text is judged after trimming (callers send the trimmed value); a secret with
 * leading/trailing whitespace is rejected, never silently trimmed. An empty
 * value is an error only when `required`.
 */
export function validateField({
  field,
  value,
  required,
}: {
  field: DestinationField;
  value: string;
  required: boolean;
}): string | null {
  if (field.section === 'config') {
    const trimmed = value.trim();
    if (trimmed === '') {
      return required ? strings.fieldRequired({ label: field.label }) : null;
    }
    if (field.key === 'topic' && !isValidNtfyTopic(trimmed)) {
      return strings.invalidNtfyTopic;
    }
    return null;
  }
  if (value === '') {
    return required ? strings.fieldRequired({ label: field.label }) : null;
  }
  if (hasSurroundingWhitespace(value)) {
    return strings.secretWhitespace({ label: field.label });
  }
  if (URL_SECRET_KEYS.has(field.key) && !isHttpsUrl(value)) {
    return strings.invalidHttpsUrl({ label: field.label });
  }
  if (field.key === 'signing_secret' && !isValidSigningSecret(value)) {
    return strings.invalidSigningSecret;
  }
  return null;
}

/** True when the destination's masked secrets carry the unreadable-blob marker. */
export function hasUnreadableSecrets(secrets: Record<string, string>): boolean {
  return Object.hasOwn(secrets, 'error');
}

/** 32 random bytes as unpadded base64url (43 characters). */
export function generateSigningSecret(): string {
  const bytes = new Uint8Array(32);
  crypto.getRandomValues(bytes);
  let binary = '';
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '');
}
