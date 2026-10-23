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
    'If this person needs a new one, call POST /v1/reset-password from the app that serves them.',

  // Operator password reset (Users list row action).
  resetPasswordAction: 'Reset password',
  confirmResetPasswordAction: 'Confirm reset',
  resettingPasswordAction: 'Resetting…',
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
  resetPasswordNotFound: 'This person does not exist. Refresh the list.',
  resetPasswordTooMany: ({ seconds }: { seconds: number }): string =>
    `Too many password changes. Try again in ${seconds} s.`,
  resetPasswordFailed: 'Could not reset the password. Try again.',
  resetPasswordDefaultRetrySeconds: 60,

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

  // Inline two-step confirmation (no modal): every destructive action first
  // shows a Confirm/Cancel pair before firing.
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
  rowActionClose: 'Close',
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
  closeDestination: ({ name }: { name: string }): string => `Close ${name}`,
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
  closeRoute: ({ id }: { id: number }): string => `Close route ${id}`,
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
  deliveryLogFilterLabel: 'Filter by state',
  deliveryLogFilterDead: 'Dead',
  deliveryLogFilterPending: 'Pending',
  deliveryLogFilterDelivered: 'Delivered',
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

  // Pending sends section.
  pendingSendsHeading: 'Pending sends',
  pendingSendsRefresh: 'Refresh pending sends',
  pendingSendsHint: ({ limit }: { limit: number }): string =>
    `Messages scheduled with deliver_at, soonest first (up to ${limit} shown). Times are UTC. Once released they leave this list and appear in the Delivery log.`,
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
  heartbeatsHint:
    'Apps ping POST /v1/heartbeats/{app_id}/{name}. A check is late after its period and down after period + grace, which sends one down alert, then one recovery alert on the next ping. Times are UTC.',
  heartbeatsLoading: 'Loading heartbeat checks…',
  heartbeatsLoadError: 'Could not load heartbeat checks.',
  heartbeatsEmpty: 'No heartbeat checks yet.',
  heartbeatsDownSummary: ({ down, total }: { down: number; total: number }): string =>
    `${down} of ${total} ${total === 1 ? 'check' : 'checks'} down`,
  columnHeartbeatState: 'State',
  columnHeartbeatCheck: 'Check',
  columnHeartbeatLastPing: 'Last ping',
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
  addHeartbeatHintEmpty: 'Add a destination first. Checks alert through registered destinations.',
  editHeartbeatNamed: ({ check }: { check: string }): string => `Edit ${check}`,
  closeHeartbeatNamed: ({ check }: { check: string }): string => `Close ${check}`,
  editHeartbeatTrigger: 'Edit',
  closeHeartbeatTrigger: 'Close',
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
  heartbeatDestinationNotFoundMark: '(not found)',
  invalidHeartbeatAppId:
    'App ID must be lowercase letters, digits or underscores, starting with a letter or digit (up to 63 characters).',
  invalidHeartbeatName:
    'Check name must be lowercase letters, digits or underscores, starting with a letter or digit (up to 63 characters).',
  invalidHeartbeatPeriod: 'Period must be between 1 minute and 31 days.',
  invalidHeartbeatGrace: 'Grace must be between 1 minute and 7 days.',
  heartbeatNeedsDestination: 'Select at least one alert destination.',
  heartbeatTooManyDestinations: 'Select at most 20 alert destinations.',

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

  genericError: 'Something went wrong. Please try again.',
} as const;
