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
    expect(strings.sendTestDefaultChannel).toBe('alerts');
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
    expect(strings.sendTestSelectedCount({ count: 2 })).toBe('2 selected');
    expect(strings.sendTestSelectAria({ who: 'alice@example.com' })).toBe(
      'Select alice@example.com',
    );
    expect(strings.sendTestResultsSummary({ sent: 2, total: 3, failed: 1 })).toBe(
      'Sent to 2 of 3 users · 1 failed',
    );
    expect(
      strings.sendTestResultDetail({ messageId: 'VkT2p9wQ', topic: 'urls4irl-hash-alerts' }),
    ).toBe('id VkT2p9wQ · urls4irl-hash-alerts');
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
