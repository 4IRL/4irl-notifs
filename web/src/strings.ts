// Centralized user-facing copy for the admin UI. No component inlines a
// user-facing string; every label, action, message, and heading lives here so
// wording stays consistent and is changed in one place.

/** Parameters for the token-reveal lead line. */
interface TokenRevealParams {
  userId: string;
  appId: string;
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

  genericError: 'Something went wrong. Please try again.',
} as const;
