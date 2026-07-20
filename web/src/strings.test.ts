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
