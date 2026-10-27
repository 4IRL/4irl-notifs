import { describe, expect, it } from 'vitest';

import { strings } from './strings';

describe('strings module', () => {
  it('exposes the app heading and section titles', () => {
    expect(strings.appTitle).toBe('4IRL Notifications Admin');
    expect(strings.provisionHeading).toBe('Provision a user into an app');
    expect(strings.usersHeading).toBe('Users');
  });

  it('exposes form labels and actions', () => {
    expect(strings.appIdLabel).toBe('App ID');
    expect(strings.userIdLabel).toBe('User ID');
    expect(strings.emailLabel).toBe('Email');
    expect(strings.provisionAction).toBe('Provision');
    expect(strings.deprovisionAction).toBe('Deprovision');
    expect(strings.deleteAction).toBe('Delete');
  });

  it('builds a token-reveal message for a provisioned pair', () => {
    expect(strings.tokenRevealLead({ userId: 'alice', appId: 'urls4irl' })).toBe(
      'Token for alice @ urls4irl (copy now — shown once):',
    );
  });

  it('exposes the provision password reveal copy', () => {
    expect(strings.passwordRevealLead).toBe(
      "This person's password is shown only once. It replaces any earlier password for this person.",
    );
    expect(strings.noPasswordReturned).toBe('No password was returned by this request.');
    expect(strings.noPasswordRecovery).toBe(
      'If this person needs a new one, call POST /v1/reset-password from the app that serves them.',
    );
    expect(strings.passwordCopied).toBe('Copied');
    expect(strings.passwordCopyUnavailable).toBe(
      'Copy not available. Select the password and copy it manually.',
    );
    expect(strings.copyPassword).toBe('Copy password');
    expect(strings.passwordGroupHeading).toBe('New password');
    expect(strings.passwordLabel).toBe('Password');
  });

  it('exposes the operator password reset copy', () => {
    expect(strings.resetPasswordAction).toBe('Reset password');
    expect(strings.closeDialog).toBe('Close');
    expect(strings.dialogItemUser).toBe('User');
    expect(strings.dialogItemUserApp).toBe('User · app');
    expect(strings.dialogUserApp({ user: 'alice', app: 'urls4irl' })).toBe('alice · urls4irl');
    expect(strings.dialogItemPerson).toBe('Person');
    expect(strings.dialogItemApp).toBe('App');
    expect(strings.dialogItemDelivery).toBe('Delivery');
    expect(strings.deprovisionDialogTitle).toBe('Deprovision app access?');
    expect(strings.deprovisionNotice).toBe(
      "Removes this person's access to this app. Their other apps are unaffected.",
    );
    expect(strings.deleteUserDialogTitle).toBe('Delete user?');
    expect(strings.deleteUserNotice).toBe('Removes this user from every app.');
    expect(strings.deletePersonDialogTitle).toBe('Delete person?');
    expect(strings.deletePersonNotice).toBe('Deletes the person, every app grant and token.');
    expect(strings.removeAppDialogTitle).toBe('Remove app?');
    expect(strings.removeAppNotice).toBe(
      'Removes the app, its publisher identity and every subscriber grant.',
    );
    expect(strings.replayDeliveryDialogTitle).toBe('Replay delivery?');
    expect(strings.replayDeliveryNotice).toBe('Sends the same message to the destination again.');
    expect(strings.dialogItemDestination).toBe('Destination');
    expect(strings.dialogItemRoute).toBe('Route');
    expect(strings.disableDestinationDialogTitle).toBe('Disable destination?');
    expect(strings.deleteDestinationDialogTitle).toBe('Delete destination?');
    expect(strings.deleteRouteDialogTitle).toBe('Delete route?');
    expect(strings.dialogItemCheck).toBe('Check');
    expect(strings.dialogItemSend).toBe('Send key');
    expect(strings.dialogItemKey).toBe('Key');
    expect(strings.dialogKeyForApp({ hint: 'dak_7Q2m', app: 'tasktracker' })).toBe(
      'dak_7Q2m for tasktracker',
    );
    expect(strings.pauseHeartbeatDialogTitle).toBe('Pause check?');
    expect(strings.deleteHeartbeatDialogTitle).toBe('Delete check?');
    expect(strings.cancelSendDialogTitle).toBe('Cancel scheduled send?');
    expect(strings.rejectIdentityDialogTitle).toBe('Reject identity request?');
    expect(strings.revokeKeyDialogTitle).toBe('Revoke key?');
    expect(strings.unbindAppDialogTitle).toBe('Unbind app?');
    expect(strings.rotateDestinationDialogTitle).toBe('Rotate signing secret?');
    expect(strings.rotateTokenDialogTitle).toBe('Revoke and re-mint publisher token?');
    expect(strings.confirmResetPasswordAction).toBe('Confirm reset');
    expect(strings.resettingPasswordAction).toBe('Resetting…');
    expect(strings.resetPasswordNotice).toBe(
      'Resetting replaces the current password immediately.',
    );
    expect(strings.resetPasswordUnavailable).toBe('Reset unavailable: email not loaded.');
    expect(strings.resetPasswordDone).toBe('Done');
    expect(strings.resetPasswordTriggerNamed({ user: 'alice@example.com' })).toBe(
      'Reset password for alice@example.com',
    );
    expect(strings.resetPasswordConfirmNamed({ user: 'alice@example.com' })).toBe(
      'Confirm reset password for alice@example.com',
    );
    expect(strings.resetPasswordBusyNamed({ user: 'alice@example.com' })).toBe(
      'Resetting password for alice@example.com',
    );
    expect(strings.resetPasswordDoneNamed({ user: 'alice@example.com' })).toBe(
      'Done with password for alice@example.com',
    );
    expect(strings.resetPasswordRevealLead({ user: 'alice@example.com' })).toBe(
      'The new password for alice@example.com is shown only once. It replaces any earlier password.',
    );
    expect(strings.resetPasswordNotFound).toBe(
      'This person does not exist, or has no access in this app. Refresh the list.',
    );
    expect(strings.deprovisionNotFound).toBe(
      'This person does not exist, or has no access in this app. Refresh the list.',
    );
    expect(strings.resetPasswordFailed).toBe('Could not reset the password. Try again.');
    expect(strings.resetPasswordDefaultRetrySeconds).toBe(60);
    expect(strings.resetPasswordDialogTitle).toBe('Reset password?');
    expect(strings.resetPasswordTimedOut).toBe(
      'The reset took too long. The password may have changed — reset again to get a new one.',
    );
  });

  it('formats the reset 429 wait so it reads correctly for any count', () => {
    expect(strings.resetPasswordTooMany({ seconds: 42 })).toBe(
      'Too many password changes. Try again in 42 s.',
    );
    expect(strings.resetPasswordTooMany({ seconds: 1 })).toBe(
      'Too many password changes. Try again in 1 s.',
    );
    expect(strings.resetPasswordTooMany({ seconds: 1 })).not.toContain('seconds');
  });

  it('exposes validation and empty-state copy', () => {
    expect(strings.invalidAppId).toBe('App ID must be lowercase letters, digits, or underscores.');
    expect(strings.invalidUserId).toBe(
      'User ID must be lowercase letters, digits, underscores, or hyphens.',
    );
    expect(strings.invalidEmail).toBe('Enter a valid email address.');
    expect(strings.usersEmpty).toBe('No users provisioned yet.');
  });

  it('exposes the send-test-notification copy, including param builders', () => {
    expect(strings.sendTestHeading).toBe('Send test notification');
    expect(strings.sendTestTargetAppLabel).toBe('Target app');
    expect(strings.sendTestChannelLabel).toBe('Channel');
    expect(strings.sendTestMessageLabel).toBe('Message');
    expect(strings.sendTestDefaultChannel).toBe('all');
    expect(strings.sendTestDefaultMessage).toBe('Test notification from 4IRL admin');
    expect(strings.sendTestAction).toBe('Send test notification');
    expect(strings.sendTestSending).toBe('Sending…');
    expect(strings.sendTestLoading).toBe('Loading users…');
    expect(strings.sendTestSelectHint).toBe('Select one or more users to send a test.');
    expect(strings.sendTestNoUsersForApp).toBe('No users provisioned for this app.');
    expect(strings.sendTestDelivered).toBe('Delivered');
    expect(strings.sendTestFailed).toBe('Failed');
    expect(strings.sendTestColumnUser).toBe('User');
    expect(strings.sendTestColumnApp).toBe('App');
    expect(strings.sendTestColumnTopic).toBe('Topic');
    expect(strings.sendTestSelectColumnAria).toBe('Select');
    expect(strings.sendTestInvalidChannel).toBe(
      'Channel must be lowercase letters, numbers, or underscores.',
    );
    expect(strings.sendTestConfirmSend).toBe('Send anyway');
    expect(strings.sendTestTitle({ appName: 'Chore Reaper' })).toBe('Chore Reaper test');
    expect(strings.sendTestFieldHint({ title: 'Chore Reaper test' })).toBe(
      'Subscribers listen on all. Arrives titled “Chore Reaper test”.',
    );
    expect(strings.sendTestChannelWarning({ channel: 'alerts' })).toBe(
      'This test will publish to “alerts” — no subscriber listens there.',
    );
    expect(strings.sendTestSelectedCount({ count: 2 })).toBe('2 selected');
    expect(strings.sendTestSelectAria({ who: 'alice@example.com' })).toBe(
      'Select alice@example.com',
    );
    expect(strings.sendTestResultsSummary({ sent: 2, total: 3, failed: 1 })).toBe(
      'Sent to 2 of 3 users · 1 failed',
    );
    expect(
      strings.sendTestResultDetail({ messageId: 'VkT2p9wQ', topic: 'urls4irl-hash-all' }),
    ).toBe('id VkT2p9wQ · urls4irl-hash-all');
  });

  it('exposes the tab and delivery status card copy', () => {
    expect(strings.tabProvisioning).toBe('Provisioning');
    expect(strings.tabDelivery).toBe('Delivery');
    expect(strings.deliveryStatusRefresh).toBe('Refresh status');
    expect(strings.deliveryStatusChecking).toBe('Checking…');
    expect(strings.deliveryStatusSchemaValue({ current: 3, expected: 4 })).toBe('3 of 4');
    expect(
      strings.deliveryStatusFailureValue({ version: 5, failedAt: '2026-10-17 06:00 UTC' }),
    ).toBe('v5 · 2026-10-17 06:00 UTC');
    expect(strings.deliveryStatusUnreachableDetail({ message: 'boom' })).toBe(
      'Could not reach delivery-api: boom',
    );
  });

  it('names a row in the narrow-layout kebab menu', () => {
    expect(strings.rowActionsFor({ row: 'urls4irl/nightly' })).toBe('Actions for urls4irl/nightly');
    expect(strings.routeRowLabel({ id: 7 })).toBe('route 7');
  });

  it('exposes the people section heading, status copy, and column headers', () => {
    expect(strings.peopleHeading).toBe('People');
    expect(strings.peopleLoading).toBe('Loading people…');
    expect(strings.peopleEmpty).toBe('No people recorded yet.');
    expect(strings.peopleLoadError).toBe('Could not load people.');
    expect(strings.columnPersonHash).toBe('Person hash');
    expect(strings.columnEmail).toBe('Email');
    expect(strings.columnCreated).toBe('Created');
  });
});
