// Centralized user-facing copy for the ntfy setup page. No component inlines a
// user-facing string; every heading, label, hint and message lives here so wording
// stays consistent and is changed in one place.

export const strings = {
  pageHeading: 'Set up notifications',
  // Lead paragraph parts: "Get <displayName> notifications on your phone with the free <ntfy> app. ..."
  leadBefore: 'Get ',
  leadAfterName: ' notifications on your phone with the free ',
  leadNtfy: 'ntfy',
  leadAfterNtfy: ' app. Takes about 2 minutes.',
  errorHeading: "This setup link isn't complete",
  errorBody:
    'Open it again from the notification settings in the app that sent you here. Those settings build the link with your details filled in.',
  tabsAriaLabel: 'Your phone',
  tabIphone: 'iPhone',
  tabAndroid: 'Android',
  hintDetected: (platformLabel: string) => `Detected ${platformLabel} — not right? Switch above.`,
  hintShowing: (platformLabel: string) => `Showing ${platformLabel} steps.`,
  hintPickPhone: 'Which phone do you have? Pick one to see its steps. Copy buttons work here too.',
  qrHeading: 'Continue on your phone',
  qrAriaLabel: 'QR code that opens this setup page on your phone',
  qrCaption: "Scan with your phone's camera. Copy buttons and one-tap links work best there.",
  stepsHere: 'Or follow the steps here',
  footer: 'This page never sees your password and sends nothing anywhere.',
  copyButton: 'Copy',
  // Accessible name of the idle button; the visible text is just "Copy".
  copyButtonLabel: (label: string) => `Copy ${label}`,
  copiedButton: 'Copied!',
  // Screen-reader announcement for the polite live region next to the button.
  copiedAnnouncement: (label: string) => `${label} copied to clipboard`,
  copyError: "Couldn't copy. Press and hold the value above to select it.",

  // Shared step copy. Bold segments are split out so components render <b> without HTML in strings.
  // Cue appended to the accessible name of links that open a new tab.
  opensInNewTab: ' (opens in a new tab)',
  // Install card. The store names are plain text: the page ships no brand artwork or logos.
  installCardHeading: 'Get the ntfy app',
  installCardNote: "It's free. Skip this if you already have it.",
  // Prefix of each store link's accessible name: "Get ntfy on Google Play (opens in a new tab)".
  getNtfyOn: 'Get ntfy on ',
  googlePlayName: 'Google Play',
  fDroidName: 'F-Droid',
  appStoreName: 'App Store',
  logInTitle: 'Log in',
  labelServiceUrl: 'Service URL',
  labelUsername: 'Username',
  labelPersonalTopic: 'Your notifications topic',
  labelBroadcastTopic: 'Announcements topic',
  passwordNote: (displayName: string) =>
    `Password: the notification password you set in ${displayName}. It's never shown on this page.`,
  testItTitle: 'Test it',
  testItBefore: (displayName: string) => `Back in ${displayName}, tap `,
  testItBold: 'Send test notification',
  testItAfter: '.',

  // iPhone steps
  iosServerTitle: 'Set the default server',
  iosServerBefore: 'ntfy → Settings → ',
  iosServerBold: 'Default server',
  iosServerAfter: ' → paste this address, then tap Save:',
  labelDefaultServer: 'Default server',
  iosServerWarning:
    "Paste it exactly, with no trailing slash. If it doesn't match, notifications never arrive and nothing tells you why.",
  iosLogInBefore: 'Settings → ',
  iosLogInBold: 'Users',
  iosLogInAfter: ' → Add user.',
  iosPersonalTitle: 'Subscribe to your notifications',
  iosPersonalBefore: 'Tap ',
  iosPersonalBold: '+',
  iosPersonalAfter: ' and paste:',
  iosBroadcastTitle: 'Subscribe to announcements',
  iosBroadcastBefore: 'Tap ',
  iosBroadcastBold: '+',
  iosBroadcastAfter: ' again and paste:',

  // Android steps
  androidLogInBefore: 'ntfy → Settings → ',
  androidLogInBold: 'Manage users',
  androidLogInAfter: ' → Add new user, then fill in:',
  androidOneTapTitle: 'Subscribe in one tap',
  androidOneTapPersonal: 'Subscribe to my notifications',
  androidOneTapBroadcast: 'Subscribe to announcements',
  androidFallbackPersonalBefore: "Button didn't open ntfy? Tap ",
  androidFallbackBold: '+',
  androidFallbackPersonalAfter: ' in ntfy and paste:',
  androidFallbackBroadcast: 'Or paste:',
  androidCopyTitle: 'Subscribe to your topics',
  androidCopyBefore: 'In ntfy, tap ',
  androidCopyBold: '+',
  androidCopyAfter: ' and paste each topic:',
  androidCopyOnPhoneNote: 'On your phone, this step is a one-tap button instead.',
} as const;
