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

/** Parameters for the send-test notification title. */
interface TestTitleParams {
  appName: string;
}

/** Parameters for the send-test channel/title hint line. */
interface FieldHintParams {
  title: string;
}

/** Parameters for the send-test channel-mismatch warning line. */
interface ChannelWarningParams {
  channel: string;
}

export const strings = {
  appTitle: '4IRL Notifications Admin',

  // Confirm dialogs (shared Modal shell).
  closeDialog: 'Close',
  closeAction: 'Close dialog',
  discardChangesMessage: 'Discard unsaved changes?',
  discardChanges: 'Discard changes',
  keepEditing: 'Keep editing',
  editRowGone: 'This item was removed. Close this dialog to continue.',
  // Edit dialog title; editDestination is the shorter trigger label on the row.
  editDestinationNamed: ({ name }: { name: string }): string => `Edit destination ${name}`,
  editRouteTitle: 'Edit route',
  editAppNamed: ({ appId }: { appId: string }): string => `Edit app ${appId}`,
  appSavedResult: ({ appId }: { appId: string }): string => `Saved ${appId}.`,
  dialogItemUser: 'User',
  dialogItemUserApp: 'User · app',
  dialogUserApp: ({ user, app }: { user: string; app: string }): string => `${user} · ${app}`,
  dialogItemPerson: 'Person',
  dialogItemApp: 'App',
  dialogItemDelivery: 'Delivery',
  deprovisionDialogTitle: 'Deprovision app access?',
  deprovisionNotice: "Removes this person's access to this app. Their other apps are unaffected.",
  deleteUserDialogTitle: 'Delete user?',
  deleteUserNotice: 'Removes this user from every app.',
  deletePersonDialogTitle: 'Delete person?',
  deletePersonNotice: 'Deletes the person, every app grant and token.',
  removeAppDialogTitle: 'Remove app?',
  removeAppNotice:
    'Removes the app, its publisher identity, every subscriber grant and delivery key, and the checks, routes and destinations it owns.',
  removingAction: 'Removing',
  removingActionNamed: ({ name }: { name: string }): string => `Removing ${name}`,
  removingResult: ({ appId }: { appId: string }): string => `Removing ${appId}…`,
  appRemovedResult: ({ appId }: { appId: string }): string => `Removed ${appId}.`,
  replayDeliveryDialogTitle: 'Replay delivery?',
  replayDeliveryNotice: 'Sends the same message to the destination again.',
  dialogItemDestination: 'Destination',
  dialogItemRoute: 'Route',
  disableDestinationDialogTitle: 'Disable destination?',
  deleteDestinationDialogTitle: 'Delete destination?',
  deleteRouteDialogTitle: 'Delete route?',
  dialogItemCheck: 'Check',
  dialogItemSend: 'Send key',
  dialogItemKey: 'Key',
  dialogKeyForApp: ({ hint, app }: { hint: string; app: string }): string => `${hint} for ${app}`,
  pauseHeartbeatDialogTitle: 'Pause check?',
  deleteHeartbeatDialogTitle: 'Delete check?',
  cancelSendDialogTitle: 'Cancel scheduled send?',
  rejectIdentityDialogTitle: 'Reject identity request?',
  revokeKeyDialogTitle: 'Revoke key?',
  unbindAppDialogTitle: 'Unbind app?',
  rotateDestinationDialogTitle: 'Rotate signing secret?',
  rotateTokenDialogTitle: 'Revoke and re-mint publisher token?',

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
  /** Row identifier for a deprovision dismiss name: the app alone repeats across users. */
  deprovisionAppForUser: ({ app, user }: { app: string; user: string }): string =>
    `Deprovision ${app} for ${user}`,
  deleteAction: 'Delete',
  provisioning: 'Provisioning…',

  tokenRevealLead: ({ userId, appId }: TokenRevealParams): string =>
    `Token for ${userId} @ ${appId} (copy now — shown once):`,

  passwordRevealLead:
    "This person's password is shown only once. It replaces any earlier password for this person.",
  passwordGroupHeading: 'New password',
  passwordLabel: 'Password',
  tokenGroupHeading: 'Token',
  copyPassword: 'Copy password',
  passwordCopied: 'Copied',
  passwordCopyUnavailable: 'Copy not available. Select the password and copy it manually.',
  noPasswordReturned: 'No password was returned by this request.',
  noPasswordRecovery:
    'If this person needs a new one, call POST /v1/apps/{app_id}/reset-password from the app that serves them.',

  // Operator password reset (Users list row action).
  resetPasswordAction: 'Reset password',
  confirmResetPasswordAction: 'Confirm reset',
  resettingPasswordAction: 'Resetting…',
  // Row trigger/menu-item text while a reset is in flight; sits inside resetPasswordBusyNamed.
  resettingPasswordRowAction: 'Resetting password',
  resetPasswordNotice: 'Resetting replaces the current password immediately.',
  resetPasswordUnavailable: 'Reset unavailable: email not loaded.',
  resetPasswordDone: 'Done',
  resetPasswordTriggerNamed: ({ user }: { user: string }): string => `Reset password for ${user}`,
  resetPasswordConfirmNamed: ({ user }: { user: string }): string =>
    `Confirm reset password for ${user}`,
  resetPasswordBusyNamed: ({ user }: { user: string }): string => `Resetting password for ${user}`,
  resetPasswordDoneNamed: ({ user }: { user: string }): string => `Done with password for ${user}`,
  resetPasswordRevealLead: ({ user }: { user: string }): string =>
    `The new password for ${user} is shown only once. It replaces any earlier password.`,
  resetPasswordNotFound:
    'This person does not exist, or has no access in this app. Refresh the list.',
  deprovisionNotFound:
    'This person does not exist, or has no access in this app. Refresh the list.',
  resetPasswordTooMany: ({ seconds }: { seconds: number }): string =>
    `Too many password changes. Try again in ${seconds} s.`,
  resetPasswordFailed: 'Could not reset the password. Try again.',
  resetPasswordDefaultRetrySeconds: 60,
  resetPasswordDialogTitle: 'Reset password?',
  resetPasswordTimedOut:
    'The reset took too long. The password may have changed — reset again to get a new one.',

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
  sendTestDefaultChannel: 'all',
  sendTestDefaultMessage: 'Test notification from 4IRL admin',
  sendTestAction: 'Send test notification',
  sendTestConfirmSend: 'Send anyway',
  sendTestSending: 'Sending…',
  sendTestLoading: 'Loading users…',
  sendTestSelectHint: 'Select one or more users to send a test.',
  sendTestNoUsersForApp: 'No users provisioned for this app.',
  sendTestDelivered: 'Delivered',
  sendTestFailed: 'Failed',
  sendTestColumnUser: 'User',
  sendTestColumnApp: 'App',
  sendTestColumnTopic: 'Topic',
  sendTestSelectColumnAria: 'Select',
  sendTestInvalidChannel: 'Channel must be lowercase letters, numbers, or underscores.',
  sendTestTitle: ({ appName }: TestTitleParams): string => `${appName} test`,
  sendTestFieldHint: ({ title }: FieldHintParams): string =>
    `Subscribers listen on all. Arrives titled “${title}”.`,
  sendTestChannelWarning: ({ channel }: ChannelWarningParams): string =>
    `This test will publish to “${channel}” — no subscriber listens there.`,
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

  // Two-step confirmation: every destructive action first opens a confirm
  // dialog with Confirm/Cancel before firing.
  cancelAction: 'Cancel',
  /** Accessible name of a per-row dismiss button: "Cancel" plus the row's trigger label. */
  cancelActionNamed: ({ subject }: { subject: string }): string => `Cancel ${subject}`,
  confirmDeleteAction: 'Confirm delete',
  confirmDeprovisionAction: 'Confirm deprovision',
  confirmRemoveAction: 'Confirm remove',

  // Apps section.
  appsHeading: 'Apps',
  appsLoading: 'Loading apps…',
  appsEmpty: 'No apps registered yet.',
  appsLoadError: 'Could not load apps.',
  columnApp: 'App',
  columnPerson: 'Person',
  columnAppName: 'Name',
  columnAppId: 'App ID',
  columnAppDescription: 'Description',
  columnSubscribers: 'Subscribers',

  // Held-message counts (Apps and Unprovisioned apps).
  columnHeld: 'Held',
  heldLoading: 'Checking…',
  heldUnavailableHidden: ' unavailable',
  heldPill: ({ count }: { count: number }) => `${count} held`,
  heldZeroSub: '0 held',
  heldZeroCell: '0',
  heldSubLabel: 'Held: ',

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

  publisherMintedResult: ({ appId }: { appId: string }): string =>
    `Publisher token minted for ${appId}. delivery-api stores it sealed; it is never shown.`,
  publisherRemintedResult: ({ appId }: { appId: string }): string =>
    `Publisher token re-minted for ${appId}.`,
  publisherRotatedResult: ({ appId }: { appId: string }): string =>
    `Publisher token for ${appId} revoked and re-minted. The old token no longer works.`,
  publisherMintedButRegisterFailed: 'Publisher token minted, but registering the app failed: ',

  appAlreadyRegisteredNote:
    'already registered; display name updated (description too if provided)',

  invalidDisplayName: 'Display name is required.',

  // App-ID combobox (Provision form).
  useUnprovisionedApp: ({ value }: { value: string }): string =>
    `Use unprovisioned app: “${value}”`,

  // Unprovisioned (in-use but unregistered) apps section.
  unregisteredAppsHeading: 'Unprovisioned apps',
  unregisteredAppsLoading: 'Loading…',
  unregisteredAppsHint:
    'These apps have users, topics or waiting messages but no registry entry. Have each app register itself with 4irl-notifs — its backend calls POST /v1/apps/{app_id}/provision with its own delivery key (see docs/app-integration-guide.md) — or add it above via “Add an app”. Waiting messages are delivered once the app has a matching route.',
  unregisteredAppsEmpty: 'None — every in-use app is registered.',
  unregisteredAppsEmptyHeldReady:
    'None — every in-use app is registered and no messages are waiting for an unregistered app.',

  // Page tabs.
  tabsAriaLabel: 'Admin sections',
  tabProvisioning: 'Provisioning',
  tabDelivery: 'Delivery',

  // Delivery tab: delivery-api status card.
  deliveryStatusHeading: 'delivery-api',
  deliveryStatusRefresh: 'Refresh status',
  deliveryStatusChecking: 'Checking…',
  deliveryStatusReady: 'Ready',
  deliveryStatusSchemaMismatch: 'Schema mismatch',
  deliveryStatusUnavailable: 'Unavailable',
  deliveryStatusUnreachable: 'Unreachable',
  deliveryStatusSchemaLabel: 'Schema',
  deliveryStatusMigrationLabel: 'Last migration failure',
  deliveryStatusNone: 'none',
  noValue: '—',
  heldMessagesLabel: 'Held messages',
  unassignedAlertsLabel: 'Unassigned heartbeat alerts',
  heldMessagesHint: 'Add a route or destination for the app so waiting messages are delivered.',
  unassignedAlertsHint: 'Give each check an enabled destination in Heartbeats.',
  columnActions: 'Actions',
  separator: ' · ',
  adminTestDeliveryTitle: 'Test from 4IRL admin',
  adminTestDeliveryBody: ({ destination }: { destination: string }): string =>
    `Test delivery to ${destination}`,
  deliveryStatusSchemaValue: ({
    current,
    expected,
  }: {
    current: number;
    expected: number;
  }): string => `${current} of ${expected}`,
  deliveryStatusFailureValue: ({
    version,
    failedAt,
  }: {
    version: number;
    failedAt: string;
  }): string => `v${version} · ${failedAt}`,
  deliveryStatusFailedNewerLead: ({ version }: { version: number }): string =>
    `A newer migration (v${version}) failed: `,
  deliveryStatusFailedNewerTail: ({ schemaVersion }: { schemaVersion: number }): string =>
    `. The service still runs on schema ${schemaVersion}.`,
  deliveryStatusMismatchLead: ({ version }: { version: number }): string =>
    `Migration v${version} failed: `,
  deliveryStatusMismatchTail: '. Check the delivery-migrate logs.',
  deliveryStatusMismatchNoFailure: 'delivery-api schema does not match this build.',
  deliveryStatusUnavailableDetail: 'delivery-api cannot reach its database.',
  deliveryStatusUnreachableDetail: ({ message }: { message: string }): string =>
    `Could not reach delivery-api: ${message}`,

  // Narrow-layout row menu (the three-dot button that holds a row's actions).
  rowActionsFor: ({ row }: { row: string }): string => `Actions for ${row}`,

  // Visible text of a Destinations / Routes row action. The row already shows which
  // destination or route it is, so the button reads as the bare verb; its accessible name
  // (the `…Destination` / `…Route` strings below) keeps the object and always contains this
  // text (WCAG 2.5.3 Label in Name).
  rowActionTest: 'Test',
  rowActionEdit: 'Edit',
  rowActionDisable: 'Disable',
  rowActionConfirmDisable: 'Confirm disable',
  rowActionEnable: 'Enable',
  rowActionDelete: 'Delete',
  rowActionConfirmDelete: 'Confirm delete',

  // Owner badges (Destinations, Routes and Heartbeats tables).
  columnOwner: 'Owner',
  ownerPrefix: 'Owner: ',
  ownerOperator: 'Operator',
  ownerApp: ({ appId }: { appId: string }): string => `App · ${appId}`,
  ownerUnknown: 'Unknown',
  ownerManagedNote: ({ appId }: { appId: string }): string =>
    `Managed by ${appId}'s deploy — operator edits are reverted on its next deploy.`,
  ownerUnknownNote:
    "Owner unknown — an app's deploy may manage this, and operator edits could be reverted.",
  // The short forms under a row's buttons: one line that is never cut off (the Owner badge
  // beside it names the app). The long forms above stay in the confirm notices and the row menu.
  ownerManagedInlineNote: 'Edits are overridden on next deploy',
  ownerUnknownInlineNote: 'Owner unknown — edits may be overridden',

  // Destinations section.
  destinationsHeading: 'Destinations',
  destinationsLoading: 'Loading destinations…',
  destinationsEmpty: 'No destinations yet.',
  destinationsLoadError: 'Could not load destinations.',
  columnDestName: 'Name',
  columnDestType: 'Type',
  columnDestTarget: 'Target',
  columnDestStatus: 'Status',
  columnDestActions: 'Actions',
  destinationTypeLabels: { ntfy: 'ntfy', discord: 'Discord', webhook: 'Webhook' },
  destinationTarget: ({ topic }: { topic: string }): string => `topic: ${topic}`,
  destinationEnabled: 'Enabled',
  destinationDisabled: 'Disabled',
  destinationDisabledNoReason: 'Disabled by an operator.',
  destinationSecretsUnreadable: 'secrets unreadable',
  destinationSecretsUnreadableHint: 'Delete and recreate this destination to repair it.',
  sendTestTo: ({ name }: { name: string }): string => `Send test to ${name}`,
  sendTestToSending: 'Sending…',
  testStateDelivered: 'Delivered',
  testStatePending: 'Pending',
  testStateDead: 'Dead',
  testNoDelivery: 'No delivery was created.',
  testHttpStatus: ({ code }: { code: number }): string => `HTTP ${code}`,
  editDestination: ({ name }: { name: string }): string => `Edit ${name}`,
  disableDestination: ({ name }: { name: string }): string => `Disable ${name}`,
  confirmDisableDestination: ({ name }: { name: string }): string => `Confirm disable ${name}`,
  disablingDestination: 'Disabling…',
  enableDestination: ({ name }: { name: string }): string => `Enable ${name}`,
  enablingDestination: 'Enabling…',
  deleteDestination: ({ name }: { name: string }): string => `Delete ${name}`,
  confirmDeleteDestination: ({ name }: { name: string }): string => `Confirm delete ${name}`,
  deletingDestination: 'Deleting…',

  // Add / edit destination forms.
  addDestinationAction: 'Add destination',
  addDestinationHeading: 'Add destination',
  addingDestination: 'Adding…',
  destNameLabel: 'Name',
  destTypeLabel: 'Type',
  destNamePlaceholder: 'ops_discord',
  destTopicLabel: 'ntfy topic',
  destAccessTokenLabel: 'Access token (optional)',
  destDiscordUrlLabel: 'Discord webhook URL',
  destWebhookUrlLabel: 'Webhook URL',
  destSigningSecretLabel: 'Signing secret',
  destSigningSecretPlaceholder: '32–256 printable characters',
  destGenerateAction: 'Generate',
  destFieldsHint:
    'Fields change with the type: ntfy asks for a topic and an optional access token; Discord asks for a webhook URL.',
  destEditSecretsHint:
    'Secrets are write-only: the current values are never shown. Fill a field only to replace it.',
  destLeaveBlank: '— leave blank to keep',
  invalidDestinationName:
    'Name must be lowercase letters, digits or underscores, starting with a letter or digit (max 63).',
  invalidNtfyTopic: 'Topic must be 1–64 letters, digits, underscores or hyphens.',
  invalidHttpsUrl: ({ label }: { label: string }): string => `${label} must be an https URL.`,
  invalidSigningSecret: 'Signing secret must be 32–256 printable ASCII characters.',
  secretWhitespace: ({ label }: { label: string }): string =>
    `${label} has leading or trailing whitespace. Remove it; secrets are never trimmed.`,
  fieldRequired: ({ label }: { label: string }): string => `${label} is required.`,
  saveDestination: ({ name }: { name: string }): string => `Save ${name}`,
  savingDestination: 'Saving…',
  rotateOverlapLabel: 'Overlap',
  rotateOverlapOptions: { 1: '1 hour', 24: '24 hours', 168: '7 days' },
  rotateDestination: ({ name }: { name: string }): string => `Rotate ${name}`,
  confirmRotateDestination: ({ name }: { name: string }): string => `Confirm rotate ${name}`,
  rotatingDestination: 'Rotating…',
  rotateHint:
    'Rotating generates a new signing secret. The old one keeps verifying for the overlap window, so update the receiver before it ends.',
  rotateInvalidatesPrevious:
    'The previous secret is still valid. Rotating now makes it invalid immediately.',
  revealLeadNew: ({ name }: { name: string }): string =>
    `New signing secret for ${name} (copy now — shown once):`,
  revealLeadAdded: ({ name }: { name: string }): string =>
    `Signing secret for ${name} (copy now — shown once):`,
  revealPreviousValidUntil: ({ when }: { when: string }): string =>
    `Previous secret valid until ${when}.`,
  revealDismiss: 'Done / I have saved it',

  // Routes section.
  routesHeading: 'Routes',
  routesLoading: 'Loading routes…',
  routesEmpty: 'No routes yet.',
  routesLoadError: 'Could not load routes.',
  routesHint:
    'A message goes to every enabled route whose app and channel patterns match. * matches anything; alerts* matches channels starting with “alerts”.',
  columnRouteApp: 'App',
  columnRouteChannel: 'Channel',
  columnRouteDestinations: 'Destinations',
  columnRouteStatus: 'Status',
  columnRouteActions: 'Actions',
  routeEnabled: 'Enabled',
  routeOff: 'Off',
  addRouteAction: 'Add route',
  addRouteHeading: 'Add route',
  addingRoute: 'Adding…',
  addRouteHintLoading: 'Loading destinations…',
  addRouteHintError: 'Could not load destinations',
  addRouteHintEmpty: 'Add a destination first',
  routeRowLabel: ({ id }: { id: number }): string => `route ${id}`,
  editRoute: ({ id }: { id: number }): string => `Edit route ${id}`,
  deleteRoute: ({ id }: { id: number }): string => `Delete route ${id}`,
  confirmDeleteRoute: ({ id }: { id: number }): string => `Confirm delete route ${id}`,
  deletingRoute: 'Deleting…',
  saveRoute: ({ id }: { id: number }): string => `Save route ${id}`,
  savingRoute: 'Saving…',
  routeAppLabel: 'App pattern',
  routeChannelLabel: 'Channel pattern',
  routeDestinationsLegend: 'Destinations',
  routeDestinationDisabledMark: '(disabled)',
  routeEnabledLabel: 'Enabled',
  routePatternHint: 'App: * or an app id. Channel: *, an exact channel or a prefix ending in *.',
  invalidRouteAppPattern:
    'App pattern must be * or an app id (lowercase letters, digits or underscores, not “everyone”).',
  invalidRouteChannelPattern:
    'Channel pattern must be *, a channel name, or a prefix ending in * (lowercase letters, digits or underscores).',
  routeNeedsDestination: 'Choose at least one destination (at most 20).',

  // Delivery Log section.
  deliveryLogHeading: 'Delivery log',
  deliveryLogFilterLabel: 'Filter deliveries',
  deliveryLogFilterDead: 'Dead',
  deliveryLogFilterPending: 'Pending',
  deliveryLogFilterDelivered: 'Delivered',
  deliveryLogFilterReleaseFailed: 'Release failed',
  releaseFailuresHint:
    "Held messages that could not be sent when their route appeared. Check the app's Routes and Destinations.",
  releaseFailuresLoading: 'Loading release failures…',
  releaseFailuresUnavailable: 'Release failures are unavailable on this backend.',
  releaseFailuresEmpty:
    'No release failures. Held messages wait until a matching route and destination exist; see Routes and Destinations.',
  releaseFailuresTruncated: ({ shown, total }: { shown: number; total: number }): string =>
    `Showing the newest ${shown} of ${total} release failures.`,
  releaseFailuresTableLabel: 'Release failures',
  releaseReasonInvalid: "Invalid when released — fix the app's Routes or Destinations",
  columnReleaseReason: 'Reason',
  columnReleaseFailed: 'Failed',
  deliveryLogFiltersMenu: 'Filter deliveries',
  deliveryLogRefresh: 'Refresh log',
  deliveryLogHint: ({ state }: { state: string }): string =>
    `Showing the newest 50 ${state} deliveries.`,
  deliveryLogReplayHint: 'Replay sends a dead delivery again with a fresh retry budget.',
  deliveryLogLoading: 'Loading deliveries…',
  deliveryLogLoadError: 'Could not load deliveries.',
  deliveryLogEmpty: ({ state }: { state: string }): string => `No ${state} deliveries.`,
  columnLogState: 'State',
  columnLogAppChannel: 'App · channel',
  columnLogDestination: 'Destination',
  columnLogAttempts: 'Attempts',
  columnLogLastStatus: 'Last status',
  columnLogUpdated: 'Updated',
  columnLogActions: 'Actions',
  deliveryStateDead: 'Dead',
  deliveryStatePending: 'Pending',
  deliveryStateDelivered: 'Delivered',
  deliveryMessageId: ({ id }: { id: string }): string => `msg ${id}`,
  showAttempts: ({ id }: { id: number }): string => `Show attempts for delivery ${id}`,
  hideAttempts: ({ id }: { id: number }): string => `Hide attempts for delivery ${id}`,
  attemptsTableLabel: ({ id }: { id: number }): string => `Attempts for delivery ${id}`,
  attemptsLoading: 'Loading attempts…',
  attemptsLoadError: 'Could not load attempts.',
  attemptsEmpty: 'No attempts recorded.',
  columnAttemptNumber: '#',
  columnAttemptOutcome: 'Outcome',
  columnAttemptStatus: 'Status',
  columnAttemptLatency: 'Latency',
  columnAttemptStarted: 'Started',
  columnAttemptDetail: 'Error / response',
  attemptLatency: ({ ms }: { ms: number }): string => `${ms.toLocaleString('en-US')} ms`,
  replayDelivery: ({ id }: { id: number }): string => `Replay delivery ${id}`,
  confirmReplayDelivery: ({ id }: { id: number }): string => `Confirm replay delivery ${id}`,
  replayingDelivery: 'Replaying…',
  replayQueued: ({ next }: { next: string | null }): string =>
    next === null ? 'Replay queued' : `Replay queued — next attempt ${next}`,
  destinationWasDeleted: 'destination was deleted',
  personOptedOut: 'person opted out',
  deliveryPersonalEndpoint: ({ id }: { id: string }): string => `#${id}`,
  deliveryPersonalPill: 'Personal',
  deliveryPersonalSub: ({ type, hash }: { type: string; hash: string | null }): string =>
    hash === null ? type : `${type} · ${hash}`,

  // Pending sends section.
  pendingSendsHeading: 'Pending sends',
  pendingSendsRefresh: 'Refresh pending sends',
  timesLocalHint: ({ zone }: { zone: string }): string =>
    `Times are shown in your local time zone (${zone}); the abbreviations on each time follow daylight saving.`,
  pendingSendsHint: ({ limit, zone }: { limit: number; zone: string }): string =>
    `Messages scheduled with deliver_at, soonest first (up to ${limit} shown). ${strings.timesLocalHint({ zone })} Once released they leave this list and appear in the Delivery log.`,
  pendingSendsTruncated: ({ shown, total }: { shown: number; total: number }): string =>
    `Showing the ${shown} soonest of ${total} pending sends.`,
  pendingSendsLoading: 'Loading pending sends…',
  pendingSendsLoadError: 'Could not load pending sends.',
  pendingSendsEmpty: 'No pending sends.',
  columnPendingState: 'State',
  columnPendingDestinations: 'Destinations',
  columnPendingDeliverAt: 'Deliver at',
  columnPendingCreated: 'Created',
  columnPendingActions: 'Actions',
  pendingStateScheduled: 'Scheduled',
  pendingStateDue: 'Due',
  pendingReleasing: 'releasing',
  pendingRecipientName: 'recipient',
  pendingRecipientDetail: 'ntfy · personal topic',
  cancelSend: 'Cancel send',
  cancelSendNamed: ({ key }: { key: string }): string => `Cancel send ${key}`,
  confirmCancelSend: 'Confirm cancel',
  confirmCancelSendNamed: ({ key }: { key: string }): string => `Confirm cancel send ${key}`,
  keepSend: 'Keep',
  keepSendNamed: ({ key }: { key: string }): string => `Keep send ${key}`,
  cancellingSend: 'Cancelling…',
  cancellingSendNamed: ({ key }: { key: string }): string => `Cancelling send ${key}`,
  cancelSendNotice: ({ key }: { key: string }): string =>
    `${key}: The app is not told. Re-posting this idempotency key will not reschedule it.`,
  cancelSendFailed: ({ message }: { message: string }): string => `Could not cancel: ${message}`,
  cancelSendDone: ({ key }: { key: string }): string =>
    `Cancelled send ${key}. It will not be delivered.`,
  cancelSendGone: ({ key }: { key: string }): string =>
    `Already gone ${key}: it was released or removed.`,
  cancelSendReleased: ({ key }: { key: string }): string =>
    `Already released ${key}: this message is now in the Delivery log.`,
  pendingSendReleased: ({ key }: { key: string }): string =>
    `Released ${key}: it is no longer pending.`,
  relativeDueNow: 'due now',
  relativeUnderMinute: 'in <1 min',
  relativeMinutes: ({ minutes }: { minutes: number }): string => `in ${minutes} min`,
  relativeHoursMinutes: ({ hours, minutes }: { hours: number; minutes: number }): string =>
    minutes === 0 ? `in ${hours} h` : `in ${hours} h ${minutes} min`,
  relativeDays: ({ days }: { days: number }): string =>
    days === 1 ? 'in 1 day' : `in ${days} days`,

  relativeJustNow: 'just now',
  relativeSecondsAgo: ({ seconds }: { seconds: number }): string => `${seconds} s ago`,
  relativeMinutesAgo: ({ minutes }: { minutes: number }): string => `${minutes} min ago`,
  relativeHoursAgo: ({ hours }: { hours: number }): string => `${hours} h ago`,
  relativeDaysAgo: ({ days }: { days: number }): string =>
    days === 1 ? '1 day ago' : `${days} days ago`,
  durationSeconds: ({ value }: { value: number }): string => `${value} s`,
  durationMinutes: ({ value }: { value: number }): string => `${value} min`,
  durationHours: ({ value }: { value: number }): string => `${value} h`,
  durationDays: ({ value }: { value: number }): string => `${value} d`,

  // Delivery tab: heartbeat checker fact on the status card.
  heartbeatCheckerLabel: 'Heartbeat checker',
  heartbeatCheckerNever: 'never',
  heartbeatCheckerRunning: ({ lastRun }: { lastRun: string }): string =>
    `Running · last run ${lastRun}`,
  heartbeatCheckerStalled: ({ lastRun }: { lastRun: string }): string =>
    `Stalled · last run ${lastRun}`,
  heartbeatCheckerStalledWarning:
    'The heartbeat checker has not run recently, so missed check-ins are not being alerted. The external watchdog also reports this. Check delivery-api logs.',

  // Heartbeats section.
  heartbeatsHeading: 'Heartbeats',
  heartbeatsRefresh: 'Refresh heartbeats',
  heartbeatsHint: ({ zone }: { zone: string }): string =>
    `Push checks: apps ping POST /v1/heartbeats/{app_id}/{name}. A check is late after its period and down after period + grace, which sends one down alert, then one recovery alert on the next ping. Probe checks: delivery-api requests an HTTPS URL on an interval and alerts after repeated failures in a row. ${strings.timesLocalHint({ zone })}`,
  heartbeatsLoading: 'Loading heartbeat checks…',
  heartbeatsLoadError: 'Could not load heartbeat checks.',
  heartbeatsEmpty: 'No heartbeat checks yet.',
  heartbeatsDownSummary: ({ down, total }: { down: number; total: number }): string =>
    `${down} of ${total} ${total === 1 ? 'check' : 'checks'} down`,
  columnHeartbeatState: 'State',
  columnHeartbeatCheck: 'Check',
  columnHeartbeatLastPing: 'Last ping / probe',
  columnHeartbeatSchedule: 'Schedule',
  columnHeartbeatAlertsTo: 'Alerts to',
  columnHeartbeatActions: 'Actions',
  heartbeatCheckSeparator: ' / ',
  heartbeatStateUp: 'Up',
  heartbeatStateLate: 'Late',
  heartbeatStateDown: 'Down',
  heartbeatStatePaused: 'Paused',
  heartbeatStateWaiting: 'Waiting',
  heartbeatLateSub: ({ relative }: { relative: string }): string => `down ${relative}`,
  heartbeatDownSince: ({ time }: { time: string }): string => `since ${time}`,
  heartbeatReportedFailure: 'reported failure',
  heartbeatPausedWas: ({ was }: { was: string }): string => `was ${was}`,
  heartbeatWasUp: 'up',
  heartbeatWasLate: 'late',
  heartbeatWasDown: 'down',
  heartbeatWasWaiting: 'waiting',
  heartbeatStateNew: 'New',
  heartbeatFirstProbe: 'first probe',
  heartbeatAwaitingProbe: 'Awaiting next probe',
  heartbeatProbeProgress: ({ count, threshold }: { count: number; threshold: number }): string =>
    `${count} of ${threshold} failures`,
  heartbeatProbedAgo: ({ relative }: { relative: string }): string => `probed ${relative}`,
  heartbeatProbeResultLine: ({ result, probed }: { result: string; probed: string }): string =>
    `${result} · ${probed}`,
  heartbeatProbeTimeout: ({ timeout }: { timeout: number }): string => `timeout ${timeout} s`,
  heartbeatDownReasonSince: ({ reason, time }: { reason: string; time: string }): string =>
    `${reason} · since ${time}`,
  probeReasonHttpStatus: ({ status }: { status: number }): string => `HTTP ${status}`,
  probeReasonHttpStatusUnknown: 'Unexpected HTTP status',
  probeReasonTimeout: 'Timed out',
  probeReasonBodyMismatch: 'Body did not match',
  probeReasonBlocked: 'Address blocked',
  probeReasonTls: 'TLS error',
  probeReasonScheme: 'Not HTTPS',
  probeReasonNetwork: 'Network error',
  probeReasonConfig: 'Credentials could not be used',
  probeReasonMissed: 'Prober stalled',
  probeReasonFail: 'Reported failure',
  probeReasonUnknown: 'Failed (reason not recognised)',
  probeResultOk: ({ status }: { status: number }): string => `OK · HTTP ${status}`,
  probeResultOkNoStatus: 'OK',
  heartbeatWaitingSub: 'for first ping',
  heartbeatWaitingDeadline: ({ deadline }: { deadline: string }): string =>
    `down if none by ${deadline}`,
  heartbeatFailPing: ({ relative }: { relative: string }): string => `fail ping · ${relative}`,
  heartbeatEvery: ({ period }: { period: string }): string => `every ${period}`,
  heartbeatGrace: ({ grace }: { grace: string }): string => `grace ${grace}`,
  pauseHeartbeat: 'Pause',
  pauseHeartbeatNamed: ({ check }: { check: string }): string => `Pause ${check}`,
  confirmPauseHeartbeat: 'Confirm pause',
  confirmPauseHeartbeatNamed: ({ check }: { check: string }): string => `Confirm pause ${check}`,
  keepHeartbeatRunning: 'Keep running',
  keepHeartbeatRunningNamed: ({ check }: { check: string }): string => `Keep running ${check}`,
  pausingHeartbeat: 'Pausing…',
  pausingHeartbeatNamed: ({ check }: { check: string }): string => `Pausing ${check}`,
  pauseHeartbeatNotice: 'No alerts are sent while paused. Pings are still recorded.',
  pauseProbeNotice: 'No probes run and no alerts are sent while paused.',
  probeAccessHintTokenRejected:
    '403 with Access credentials set: the Cloudflare token may have expired or been revoked. Edit the check and use Replace.',
  probeAccessHintTokenMissing:
    '403: the endpoint may be behind Cloudflare Access. Edit the check and add credentials.',
  resumeHeartbeat: 'Resume',
  resumeHeartbeatNamed: ({ check }: { check: string }): string => `Resume ${check}`,
  resumingHeartbeat: 'Resuming…',
  resumingHeartbeatNamed: ({ check }: { check: string }): string => `Resuming ${check}`,
  deleteHeartbeat: 'Delete',
  deleteHeartbeatNamed: ({ check }: { check: string }): string => `Delete ${check}`,
  confirmDeleteHeartbeat: 'Confirm delete',
  confirmDeleteHeartbeatNamed: ({ check }: { check: string }): string => `Confirm delete ${check}`,
  deletingHeartbeat: 'Deleting…',
  deletingHeartbeatNamed: ({ check }: { check: string }): string => `Deleting ${check}`,
  heartbeatPaused: ({ check }: { check: string }): string =>
    `Paused ${check}. Its alerts are suppressed until you resume it.`,
  heartbeatResumed: ({ check }: { check: string }): string =>
    `Resumed ${check}. Its deadline restarts now.`,
  heartbeatDeleted: ({ check }: { check: string }): string => `Deleted ${check}.`,
  heartbeatPauseFailed: ({ message }: { message: string }): string => `Could not pause: ${message}`,
  heartbeatResumeFailed: ({ message }: { message: string }): string =>
    `Could not resume: ${message}`,
  heartbeatDeleteFailed: ({ message }: { message: string }): string =>
    `Could not delete: ${message}`,
  heartbeatAddedResult: ({ check }: { check: string }): string =>
    `Added ${check}. It is waiting for its first ping.`,
  heartbeatSavedResult: ({ check }: { check: string }): string => `Saved ${check}.`,

  // Heartbeats add / edit form.
  addHeartbeatAction: 'Add check',
  addHeartbeatHeading: 'Add check',
  addingHeartbeat: 'Adding…',
  addHeartbeatHintLoading: 'Loading destinations…',
  addHeartbeatHintError: 'Could not load destinations.',
  heartbeatNoDestination: 'No destination',
  editHeartbeatNamed: ({ check }: { check: string }): string => `Edit ${check}`,
  editHeartbeatTrigger: 'Edit',
  saveHeartbeat: 'Save',
  savingHeartbeat: 'Saving…',
  heartbeatEditingNotice: ({ check }: { check: string }): string =>
    `Editing ${check}. The app ID and name cannot change.`,
  heartbeatAppIdLabel: 'App ID',
  heartbeatNameLabel: 'Check name',
  heartbeatPeriodLabel: 'Period',
  heartbeatGraceLabel: 'Grace',
  heartbeatPeriodUnitLabel: 'Period unit',
  heartbeatGraceUnitLabel: 'Grace unit',
  heartbeatUnitSeconds: 'seconds',
  heartbeatUnitMinutes: 'minutes',
  heartbeatUnitHours: 'hours',
  heartbeatUnitDays: 'days',
  heartbeatPeriodHint: 'Expected time between pings (1 min – 31 days).',
  heartbeatGraceHint:
    'Extra wait before “down” (1 min – 7 days). Use ≥ 1 h for daily jobs to absorb DST shifts.',
  heartbeatDestinationsLegend: 'Alert destinations',
  heartbeatDestinationsHint: 'Optional. Alerts wait until an enabled destination is assigned.',
  heartbeatDestinationsHintEmpty:
    'No destinations are registered yet. You can save this check now; its alerts wait until you add an enabled destination and assign it here.',
  heartbeatDestinationsNoEnabledWarning:
    "No enabled destination is selected; this check's alerts will wait.",
  heartbeatKindLegend: 'Check type',
  heartbeatKindPush: 'Push (app pings in)',
  heartbeatKindProbe: 'Website probe',
  heartbeatProbeLegend: 'Probe',
  heartbeatProbeUrlLabel: 'URL',
  heartbeatProbeUrlHint: 'A public https URL. Not localhost or an IP address.',
  heartbeatProbeStatusesLabel: 'Expected statuses',
  heartbeatProbeStatusesHint: 'Comma-separated codes, e.g. 200, 204. 3xx allowed.',
  heartbeatProbeBodyMatchLabel: 'Body must contain',
  heartbeatProbeBodyMatchHint: 'Optional. The response body must include this text.',
  heartbeatProbeIntervalLabel: 'Interval',
  heartbeatProbeIntervalUnitLabel: 'Interval unit',
  heartbeatProbeIntervalHintAdd:
    '1 minute to 24 hours. The check goes down after repeated failed probes in a row.',
  heartbeatProbeIntervalHintEdit: '1 minute to 24 hours.',
  heartbeatProbeIntervalThreshold: ({ threshold }: { threshold: number }): string =>
    `Down after ${threshold} failed probes in a row.`,
  heartbeatEditingNoticeProbe: ({ check }: { check: string }): string =>
    `Editing ${check}. The app ID and name cannot change. Type: Website probe.`,
  heartbeatProbeEditRestartNote: 'Saving probe changes restarts failure counting.',
  heartbeatProbeTimeoutLabel: 'Timeout (seconds)',
  heartbeatProbeTimeoutHint: '1 to 8 seconds, shorter than the interval.',
  heartbeatCredentialsCaption: 'Cloudflare Access credentials (optional)',
  heartbeatCredentialsClientIdLabel: 'Client ID',
  heartbeatCredentialsClientSecretLabel: 'Client secret',
  heartbeatCredentialsHintAdd:
    'Optional. Cloudflare Access service token for a protected endpoint. Cleared if you switch to Push.',
  heartbeatCredentialsHintEdit:
    'Optional. Cloudflare Access service token for a protected endpoint.',
  heartbeatCredentialsStored: 'Access credentials set',
  heartbeatCredentialsReplacing: 'New credentials will replace the stored ones on save',
  heartbeatCredentialsClearing: 'Access credentials will be removed on save',
  heartbeatCredentialsReplace: 'Replace',
  heartbeatCredentialsClear: 'Clear',
  heartbeatCredentialsCancelReplace: 'Cancel replace',
  heartbeatCredentialsUndo: 'Undo',
  heartbeatCredentialsHintTokenRejected:
    'Cloudflare rejected the last probe (HTTP 403). The Access token may have expired or been revoked. Use Replace to enter new ones.',
  heartbeatCredentialsHintTokenMissing:
    'The last probe got HTTP 403. The endpoint may be behind Cloudflare Access. Enter credentials below.',
  heartbeatCredentialsHintConfigStored:
    'The last probe could not use these credentials. Use Replace to enter new ones.',
  heartbeatCredentialsHintConfigMissing:
    'The last probe could not use the stored credentials. Enter new ones below.',
  heartbeatDestinationNotFoundMark: '(not found)',
  invalidHeartbeatAppId:
    'App ID must be lowercase letters, digits or underscores, starting with a letter or digit (up to 63 characters).',
  invalidHeartbeatName:
    'Check name must be lowercase letters, digits or underscores, starting with a letter or digit (up to 63 characters).',
  invalidHeartbeatPeriod: 'Period must be between 1 minute and 31 days.',
  invalidHeartbeatGrace: 'Grace must be between 1 minute and 7 days.',
  invalidProbeUrlEmpty: 'Enter the URL to check.',
  invalidProbeUrlScheme: 'URL must start with https://.',
  invalidProbeUrlTooLong: 'URL must be at most 2048 bytes.',
  invalidProbeUrlMalformed:
    'Enter a public https URL without a #fragment, username or password, and not localhost or an IP address.',
  invalidProbeStatuses:
    'Enter 1 to 10 distinct status codes between 100 and 599, separated by commas.',
  invalidProbeBodyMatch: 'Body text must be 1 to 256 bytes, not blank, with no NUL character.',
  invalidProbeInterval: 'Interval must be a whole number of seconds between 60 and 86400.',
  invalidProbeTimeout: 'Timeout must be a whole number of seconds between 1 and 8.',
  invalidProbeCredentialPair: 'Enter both the client ID and secret.',
  invalidProbeCredential: 'Use 1 to 256 visible ASCII characters with no spaces.',
  heartbeatTooManyDestinations: 'Select at most 20 alert destinations.',

  // Personal destinations section.
  personalHeading: 'Personal destinations',
  personalRefresh: 'Refresh personal destinations',
  personalHint:
    "Endpoints people registered through an app. Each one receives a person's messages only from the apps it is opted in for. People are shown by person hash, the same hash as the People table on the Provisioning tab. Opt-in is controlled by the person's apps; you can disable or delete an endpoint.",
  personalLoading: 'Loading personal destinations…',
  personalLoadFailed: ({ message }: { message: string }): string =>
    `Could not load personal destinations: ${message}.`,
  personalLoadFailedFallback: 'request failed',
  personalRetry: 'Retry',
  personalEmpty:
    'No personal destinations yet. People add them in the settings of an app that uses notifs.',
  personalSummaryEmpty: '0 personal destinations',
  personalSummary: ({
    total,
    disabled,
    shown,
  }: {
    total: number;
    disabled: number;
    shown: number;
  }): string => {
    const noun = total === 1 ? 'personal destination' : 'personal destinations';
    const scope = total > shown ? ` among the newest ${shown}` : '';
    return `${total} ${noun}, ${disabled} disabled${scope}`;
  },
  personalTruncatedHint: ({ shown, total }: { shown: number; total: number }): string =>
    `Showing the newest ${shown} of ${total}.`,
  columnPersonalHash: 'Person hash',
  columnPersonalLabel: 'Label',
  columnPersonalType: 'Type',
  columnPersonalTarget: 'Target',
  columnPersonalRegisteredBy: 'Registered by',
  columnPersonalOptedIn: 'Opted in for',
  columnPersonalStatus: 'Status',
  columnPersonalCreated: 'Created',
  columnPersonalActions: 'Actions',
  personalRegisteredByPrefix: 'Registered by: ',
  personalLabelSeparator: '·',
  personalAppRemoved: 'App removed',
  personalNoApps: 'No apps',
  personalDisabledBySystem: 'Disabled automatically.',
  personalDisabledByApp: 'Disabled by the app.',
  personalDisabledUnknown: 'Disabled.',
  personalRowName: ({ label, hash }: { label: string; hash: string }): string =>
    `${label} of ${hash}`,
  disablePersonal: ({ row }: { row: string }): string => `Disable personal destination ${row}`,
  confirmDisablePersonal: ({ row }: { row: string }): string =>
    `Confirm disable personal destination ${row}`,
  enablePersonal: ({ row }: { row: string }): string => `Enable personal destination ${row}`,
  deletePersonal: ({ row }: { row: string }): string => `Delete personal destination ${row}`,
  confirmDeletePersonal: ({ row }: { row: string }): string =>
    `Confirm delete personal destination ${row}`,
  dialogItemPersonalDestination: 'Personal destination',
  personalDialogItem: ({ label, id, hash }: { label: string; id: number; hash: string }): string =>
    `${label} · #${id} · ${hash}`,
  disablePersonalDialogTitle: 'Disable personal destination?',
  disablePersonalNotice:
    'Sending to this endpoint stops for every app. The app cannot re-enable an operator disable; only you can.',
  deletePersonalDialogTitle: 'Delete personal destination?',
  deletePersonalNotice: ({ apps }: { apps: string[] }): string =>
    `This person stops receiving messages here from every app it is opted in for (${apps.join(', ')}). The app can register it again; disable it instead to keep it off.`,
  deletePersonalNoAppsNotice:
    'No app is opted in, so no messages are affected. The app can register it again.',
  personalDisabledResult: ({ label, id }: { label: string; id: number }): string =>
    `Disabled personal destination ${label} (#${id}).`,
  personalEnabledResult: ({ label, id }: { label: string; id: number }): string =>
    `Enabled personal destination ${label} (#${id}).`,
  personalDeletedResult: ({ label, id }: { label: string; id: number }): string =>
    `Deleted personal destination ${label} (#${id}).`,
  personalGone: ({ label, id }: { label: string; id: number }): string =>
    `Personal destination ${label} (#${id}) no longer exists.`,
  personalDisableFailed: 'Could not disable the personal destination. Try again.',
  personalEnableFailed: 'Could not enable the personal destination. Try again.',
  personalDeleteFailed: 'Could not delete the personal destination. Try again.',

  // App identities section.
  identitiesHeading: 'App identities',
  identitiesRefresh: 'Refresh app identities',
  identitiesHint:
    "An app's GitHub deploy workflow requests an identity the first time it deploys. Approve it to let that workflow receive delivery keys for the app. Every deploy issues a new key; the previous key stops working 24 hours later.",
  identitiesLoading: 'Loading app identities…',
  identitiesLoadFailed: ({ message }: { message: string }): string =>
    `Could not load app identities (${message}).`,
  identitiesLoadFailedFallback: 'request failed',
  identitiesRetry: 'Retry',
  identitiesPendingSummary: ({ count }: { count: number }): string =>
    count === 1 ? '1 request awaiting approval' : `${count} requests awaiting approval`,
  identitiesNewAppsSummary: ({ count }: { count: number }): string =>
    count === 1 ? '1 new app' : `${count} new apps`,
  identityNewMark: 'New',
  identityStatusLabel: 'Status: ',
  identityViaRegistrar: ({ prefix }: { prefix: string }): string => `via ${prefix}`,
  identityCreatedByLabel: 'Created by registrar: ',
  identityCreatedAutomaticallySub: ({ time }: { time: string }): string =>
    `created automatically ${time}`,
  markSeen: 'Mark seen',
  markingSeen: 'Marking…',
  markSeenNamed: ({ app }: { app: string }): string => `Mark ${app} seen`,
  markingSeenNamed: ({ app }: { app: string }): string => `Marking ${app} seen`,
  markAllSeen: 'Mark all seen',
  markAllSeenAria: 'Mark all new apps seen',
  markingAllSeen: 'Marking…',
  identityMarkedSeen: ({ app }: { app: string }): string => `Marked ${app} seen`,
  identityNoLongerBound: ({ app }: { app: string }): string => `${app} is no longer bound`,
  identityMarkedAllSeen: ({ count }: { count: number }): string =>
    count === 1 ? 'Marked 1 app seen' : `Marked ${count} apps seen`,
  identityMarkSeenFailed: ({ message }: { message: string }): string =>
    `Could not mark seen: ${message}`,
  identityMarkAllSeenFailed: ({ message }: { message: string }): string =>
    `Could not mark apps seen: ${message}`,
  unbindRegistrarAppNotice: ({ prefix }: { prefix: string }): string =>
    `Revokes every key. Objects the app owns are kept but can't be managed until it is bound again. Its registrar ${prefix} creates it again on the next deploy.`,
  identitiesRequestsHeading: 'Requests',
  identitiesBindingsHeading: 'Approved apps',
  identitiesRequestsEmpty:
    "No pending requests. A request appears when an app's deploy asks for an identity.",
  identitiesBindingsEmpty: 'No approved apps yet.',
  identitiesRejectedNote:
    "A rejected request stays listed for 30 days and has no actions; the app's next deploy files a fresh request.",
  columnIdentityStatus: 'Status',
  columnIdentityApp: 'App',
  columnIdentityRequestedBy: 'Requested by (GitHub)',
  columnIdentityRequested: 'Requested',
  columnIdentityBoundTo: 'Bound to (GitHub)',
  columnIdentityKeys: 'Keys',
  columnIdentityActions: 'Actions',
  identityRequestPending: 'Pending',
  identityRequestRejected: 'Rejected',
  identityRequestUnknown: 'Unknown status',
  identityRequestUnknownSub: 'This UI does not recognise the status, so no action is offered.',
  identityRepoSub: ({
    repositoryId,
    workflow,
  }: {
    repositoryId: number;
    workflow: string;
  }): string => `repo id ${repositoryId} · ${workflow}`,
  identityRefSub: ({
    ref,
    environment,
    runId,
  }: {
    ref: string;
    environment: string | null;
    runId: string;
  }): string =>
    environment === null
      ? `${ref} · no environment · run ${runId}`
      : `${ref} · environment ${environment} · run ${runId}`,
  identityBoundSub: ({
    workflow,
    ref,
    environment,
  }: {
    workflow: string;
    ref: string;
    environment: string | null;
  }): string => [workflow, ref, environment ?? 'no environment'].join(' · '),
  identityApprovedSub: ({ time }: { time: string }): string => `approved ${time}`,
  identityNoKeys: 'No keys yet — the next deploy receives one.',
  identityKeyActive: 'Active',
  identityKeySuperseded: 'Superseded',
  identityKeyRevoked: 'Revoked',
  identityKeyUnknown: 'Unknown state',
  identityKeyIssued: ({ time, lastUsed }: { time: string; lastUsed: string }): string =>
    `issued ${time} · ${lastUsed}`,
  identityKeySupersededSub: ({ time, lastUsed }: { time: string; lastUsed: string }): string =>
    `stops working ${time} · ${lastUsed}`,
  identityKeyRevokedSub: ({ time, reason }: { time: string; reason: string | null }): string =>
    reason === null ? `revoked ${time}` : `revoked ${time} ${reason}`,
  identityKeyUnknownSub: ({ time }: { time: string }): string => `issued ${time}`,
  identityKeyLastUsed: ({ relative }: { relative: string }): string => `last used ${relative}`,
  identityKeyNeverUsed: 'never used',
  identityRevokedByOperator: 'by operator',
  identityRevokedWhenUnbound: 'when unbound',
  identityRevokedAfterGrace: 'after its 24 h grace period',
  approveIdentity: 'Approve',
  approveIdentityNamed: ({ app }: { app: string }): string => `Approve identity request for ${app}`,
  approvingIdentity: 'Approving…',
  approvingIdentityNamed: ({ app }: { app: string }): string =>
    `Approving identity request for ${app}`,
  rejectIdentity: 'Reject',
  rejectIdentityNamed: ({ app }: { app: string }): string => `Reject identity request for ${app}`,
  confirmRejectIdentity: 'Confirm reject',
  confirmRejectIdentityNamed: ({ app }: { app: string }): string =>
    `Confirm reject identity request for ${app}`,
  cancelRejectIdentityNamed: ({ app }: { app: string }): string =>
    `Cancel reject identity request for ${app}`,
  rejectIdentityNotice: 'The deploy keeps failing until a new request is approved.',
  rejectingIdentity: 'Rejecting…',
  rejectingIdentityNamed: ({ app }: { app: string }): string =>
    `Rejecting identity request for ${app}`,
  revokeKey: 'Revoke',
  revokeKeyNamed: ({ hint, app }: { hint: string; app: string }): string =>
    `Revoke key ${hint} for ${app}`,
  confirmRevokeKey: 'Confirm revoke',
  confirmRevokeKeyNamed: ({ hint, app }: { hint: string; app: string }): string =>
    `Confirm revoke key ${hint} for ${app}`,
  cancelRevokeKeyNamed: ({ hint, app }: { hint: string; app: string }): string =>
    `Cancel revoke key ${hint} for ${app}`,
  revokeKeyNotice: 'Anything still using this key is refused straight away.',
  revokingKey: 'Revoking…',
  revokingKeyNamed: ({ hint, app }: { hint: string; app: string }): string =>
    `Revoking key ${hint} for ${app}`,
  unbindApp: 'Unbind',
  unbindAppNamed: ({ app }: { app: string }): string => `Unbind ${app}`,
  confirmUnbindApp: 'Confirm unbind',
  confirmUnbindAppNamed: ({ app }: { app: string }): string => `Confirm unbind ${app}`,
  cancelUnbindAppNamed: ({ app }: { app: string }): string => `Cancel unbind ${app}`,
  unbindAppNotice:
    "Revokes every key. Objects the app owns are kept but can't be managed until it is bound again.",
  unbindingApp: 'Unbinding…',
  unbindingAppNamed: ({ app }: { app: string }): string => `Unbinding ${app}`,
  identityApproved: ({ app, repository }: { app: string; repository: string }): string =>
    `Approved ${app} for ${repository}. Its next deploy receives a key.`,
  identityRejected: ({ app }: { app: string }): string =>
    `Rejected the identity request for ${app}.`,
  identityKeyRevokedResult: ({ hint, app }: { hint: string; app: string }): string =>
    `Revoked key ${hint} for ${app}.`,
  identityUnbound: ({ app }: { app: string }): string => `Unbound ${app}. Its keys are revoked.`,
  identityAlreadyUnbound: ({ app }: { app: string }): string => `${app} was already unbound.`,
  identityApproveFailed: ({ message }: { message: string }): string =>
    `Could not approve: ${message}`,
  identityRejectFailed: ({ message }: { message: string }): string =>
    `Could not reject: ${message}`,
  identityRevokeFailed: ({ message }: { message: string }): string =>
    `Could not revoke: ${message}`,
  identityUnbindFailed: ({ message }: { message: string }): string =>
    `Could not unbind: ${message}`,

  // Registrars (a workflow approved to create apps under a reserved prefix).
  identitiesRegistrarsHeading: 'Registrars',
  identitiesRegistrarsEmpty:
    'No registrars yet. A deploy workflow that sends an app name instead of an app id asks to become one.',
  columnIdentityAsksFor: 'Asks for',
  columnRegistrarPrefix: 'Prefix',
  columnRegistrarWorkflow: 'Workflow (GitHub)',
  columnRegistrarApps: 'Apps',
  columnRegistrarApproved: 'Approved',
  identityRegistrarPill: 'Registrar',
  identityRequestKindLabel: 'Request kind: ',
  identityRegistrarRequestSub: 'a workflow that creates apps under a prefix',
  identityUnknownRequestPill: 'Unknown request',
  identityUnknownRequestSub:
    'This UI does not recognise the request kind, so no action is offered.',
  registrarAppsOf: ({ limit }: { limit: number }): string => `of ${limit}`,
  approveRegistrar: 'Approve as registrar…',
  approveRegistrarNamed: ({ repository }: { repository: string }): string =>
    `Approve registrar request from ${repository}`,
  approvingRegistrarNamed: ({ repository }: { repository: string }): string =>
    `Approving registrar request from ${repository}`,
  rejectRegistrarNamed: ({ repository }: { repository: string }): string =>
    `Reject registrar request from ${repository}`,
  confirmRejectRegistrarNamed: ({ repository }: { repository: string }): string =>
    `Confirm reject registrar request from ${repository}`,
  cancelRejectRegistrarNamed: ({ repository }: { repository: string }): string =>
    `Cancel reject registrar request from ${repository}`,
  rejectingRegistrarNamed: ({ repository }: { repository: string }): string =>
    `Rejecting registrar request from ${repository}`,
  registrarRequestRowLabel: ({ repository }: { repository: string }): string =>
    `${repository} (registrar)`,
  dialogItemWorkflow: 'Workflow',
  dialogWorkflowValue: ({
    repository,
    workflowPath,
  }: {
    repository: string;
    workflowPath: string;
  }): string => `${repository} · ${workflowPath}`,
  identityRegistrarRejected: ({ repository }: { repository: string }): string =>
    `Rejected registrar request from ${repository}`,
  approveRegistrarDialogTitle: 'Approve registrar?',
  approveRegistrarPrefixLabel: 'Reserved prefix',
  approveRegistrarPrefixHint:
    'Lowercase letters, digits and underscores, ending in "_", up to 32 characters. Every app this workflow creates is named prefix + app name.',
  approveRegistrarNotice:
    'This workflow can then create a capped number of apps under the prefix without asking you; the Registrars table shows the limit. Each app still gets its own key and data.',
  approveRegistrarConfirm: 'Approve registrar',
  approvingRegistrar: 'Approving…',
  prefixInvalidGrammar:
    'Use 2 to 32 lowercase letters, digits or underscores, starting with a letter or digit and ending in "_".',
  prefixReservedPlatform: 'Prefixes starting with "platform_" are reserved.',
  prefixReservedEveryone: 'The prefix "everyone_" is reserved.',
  registrarApproved: ({ prefix, repository }: { prefix: string; repository: string }): string =>
    `Approved registrar ${prefix} for ${repository}`,
  registrarApproveFailed: ({ message }: { message: string }): string =>
    `Could not approve registrar: ${message}`,
  deleteRegistrar: 'Delete registrar',
  deleteRegistrarNamed: ({ prefix }: { prefix: string }): string => `Delete registrar ${prefix}`,
  confirmDeleteRegistrar: 'Confirm delete',
  confirmDeleteRegistrarNamed: ({ prefix }: { prefix: string }): string =>
    `Confirm delete registrar ${prefix}`,
  cancelDeleteRegistrarNamed: ({ prefix }: { prefix: string }): string =>
    `Cancel delete registrar ${prefix}`,
  deletingRegistrar: 'Deleting…',
  deletingRegistrarNamed: ({ prefix }: { prefix: string }): string =>
    `Deleting registrar ${prefix}`,
  deleteRegistrarDialogTitle: 'Delete registrar?',
  dialogItemRegistrar: 'Registrar',
  dialogRegistrarValue: ({ prefix, repository }: { prefix: string; repository: string }): string =>
    `${prefix} · ${repository}`,
  deleteRegistrarNotice: ({ count }: { count: number }): string =>
    count === 0
      ? 'It has created no apps.'
      : `Unbinds the ${count} apps it created and revokes every key they hold. Objects those apps own are kept but can't be managed until they are bound again.`,
  registrarDeleted: ({ prefix, count }: { prefix: string; count: number }): string =>
    `Deleted registrar ${prefix}; unbound ${count} apps`,
  registrarAlreadyDeleted: ({ prefix }: { prefix: string }): string =>
    `Registrar ${prefix} was already deleted`,
  registrarDeleteFailed: ({ message }: { message: string }): string =>
    `Could not delete registrar: ${message}`,

  genericError: 'Something went wrong. Please try again.',
  appNotBoundError: ({ appId }: { appId: string }) =>
    `${appId} has no approved delivery key yet. Approve its key request on the Delivery tab, then add it again.`,
  appNotBoundRetryError: ({ appId }: { appId: string }) =>
    `${appId} has no approved delivery key yet. Approve its key request on the Delivery tab, then try again.`,
} as const;
