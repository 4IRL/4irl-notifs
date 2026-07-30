// Centralized user-facing copy for the admin UI. No component inlines a
// user-facing string; every label, action, message, and heading lives here so
// wording stays consistent and is changed in one place.

/** Parameters for the token-reveal lead line. */
interface TokenRevealParams {
  userId: string;
  appId: string;
}

/** Parameters for the send-test selected-count line. */
interface SelectedCountParams {
  count: number;
}

/** Parameters for a send-test row checkbox accessible name. */
interface SelectAriaParams {
  who: string;
}

/** Parameters for the send-test results summary line. */
interface ResultsSummaryParams {
  sent: number;
  total: number;
  failed: number;
}

/** Parameters for a delivered send-test recipient's mono detail line. */
interface ResultDetailParams {
  messageId: string;
  topic: string;
}

export const strings = {
  appTitle: '4IRL Notifications Admin',
  headerBadge: 'ntfy hub',

  provisionHeading: 'Provision a user into an app',
  usersHeading: 'Users',

  appIdLabel: 'App ID',
  userIdLabel: 'User ID',
  emailLabel: 'Email',
  appIdPlaceholder: 'urls4irl',
  userIdPlaceholder: 'alice',
  emailPlaceholder: 'alice@example.com',

  provisionAction: 'Provision',
  deprovisionAction: 'Deprovision',
  deleteAction: 'Delete',
  provisioning: 'Provisioning…',

  tokenRevealLead: ({ userId, appId }: TokenRevealParams): string =>
    `Token for ${userId} @ ${appId} (copy now — shown once):`,

  invalidAppId: 'App ID must be lowercase letters, digits, or underscores.',
  invalidUserId: 'User ID must be lowercase letters, digits, underscores, or hyphens.',
  invalidEmail: 'Enter a valid email address.',

  usersEmpty: 'No users provisioned yet.',
  usersLoading: 'Loading users…',
  columnUser: 'User',
  columnApps: 'Apps',
  columnTopicPatterns: 'Topic patterns',

  // Send test notification section.
  sendTestHeading: 'Send test notification',
  sendTestTargetAppLabel: 'Target app',
  sendTestChannelLabel: 'Channel',
  sendTestMessageLabel: 'Message',
  sendTestDefaultChannel: 'alerts',
  sendTestDefaultMessage: 'Test notification from 4IRL admin',
  sendTestAction: 'Send test notification',
  sendTestSending: 'Sending…',
  sendTestLoading: 'Loading users…',
  sendTestSelectHint: 'Select one or more users to send a test.',
  sendTestNoUsersForApp: 'No users provisioned for this app.',
  sendTestDelivered: 'Delivered',
  sendTestFailed: 'Failed',
  sendTestColumnUser: 'User',
  sendTestColumnApp: 'App',
  sendTestColumnTopic: 'Topic',
  sendTestInvalidChannel: 'Channel must be lowercase letters, numbers, or underscores.',
  sendTestSelectedCount: ({ count }: SelectedCountParams): string => `${count} selected`,
  sendTestSelectAria: ({ who }: SelectAriaParams): string => `Select ${who}`,
  sendTestResultsSummary: ({ sent, total, failed }: ResultsSummaryParams): string =>
    `Sent to ${sent} of ${total} users · ${failed} failed`,
  sendTestResultDetail: ({ messageId, topic }: ResultDetailParams): string =>
    `id ${messageId} · ${topic}`,

  peopleHeading: 'People',
  peopleLoading: 'Loading people…',
  peopleEmpty: 'No people recorded yet.',
  peopleLoadError: 'Could not load people.',
  columnPersonHash: 'Person hash',
  columnEmail: 'Email',
  columnCreated: 'Created',

  // Inline two-step confirmation (no modal): every destructive action first
  // shows a Confirm/Cancel pair before firing.
  cancelAction: 'Cancel',
  confirmDeleteAction: 'Confirm delete',
  confirmDeprovisionAction: 'Confirm deprovision',
  confirmRemoveAction: 'Confirm remove',

  // Apps section.
  appsHeading: 'Apps',
  appsLoading: 'Loading apps…',
  appsEmpty: 'No apps registered yet.',
  appsLoadError: 'Could not load apps.',
  columnAppName: 'Name',
  columnAppId: 'App ID',
  columnAppDescription: 'Description',
  columnSubscribers: 'Subscribers',

  addAppHeading: 'Add an app',
  appDisplayNameLabel: 'Display name',
  appDescriptionLabel: 'Description',
  appDisplayNamePlaceholder: 'URLs4IRL',
  appDescriptionPlaceholder: 'Short description (optional)',
  addAppAction: 'Add app',
  addingApp: 'Adding…',

  editAction: 'Edit',
  removeAction: 'Remove',
  saveAction: 'Save',
  savingApp: 'Saving…',
  closeEditAction: 'Close',

  remintTokenAction: 'Re-mint token',
  rotateTokenAction: 'Revoke & re-mint',
  confirmRotateTokenAction: 'Confirm revoke & re-mint',
  minting: 'Minting…',
  rotateTokenWarning:
    'Revoking replaces the publisher token immediately — the app stops publishing until it is redeployed with the new token.',

  publisherTokenRevealLead: ({ appId }: { appId: string }): string =>
    `Publisher token for ${appId} (copy now — shown once):`,

  invalidDisplayName: 'Display name is required.',

  // App-ID combobox (Provision form).
  useUnprovisionedApp: ({ value }: { value: string }): string =>
    `Use unprovisioned app: “${value}”`,

  // Unprovisioned (in-use but unregistered) apps section.
  unregisteredAppsHeading: 'Unprovisioned apps',
  unregisteredAppsLoading: 'Loading…',
  unregisteredAppsHint:
    'These apps have users or topics but no registry entry. Have each app register itself with 4irl-notifs — its backend calls POST /v1/provision-app with its own service token (see docs/app-integration-guide.md) — or add it above via “Add an app”.',
  unregisteredAppsEmpty: 'None — every in-use app is registered.',

  genericError: 'Something went wrong. Please try again.',
} as const;
