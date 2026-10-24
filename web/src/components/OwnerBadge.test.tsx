import { cleanup, render } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import type { OwnerKind } from '../api/deliveryClient';
import { strings } from '../strings';
import { OwnerBadge } from './OwnerBadge';
import { ownerInlineNoteOf, ownerNoteOf } from './ownership';

afterEach(() => {
  cleanup();
});

describe('OwnerBadge', () => {
  it('renders Operator with a hidden Owner prefix', () => {
    const { container } = render(<OwnerBadge ownerKind="operator" ownerAppId={null} />);

    expect(container).toHaveTextContent(`${strings.ownerPrefix}${strings.ownerOperator}`);
    expect(container.querySelector('.visually-hidden')?.textContent).toBe(strings.ownerPrefix);
    expect(container.querySelector('.owner-badge--operator')).not.toBeNull();
  });

  it('renders App · <id> for an app owner', () => {
    const { container } = render(<OwnerBadge ownerKind="app" ownerAppId="chores4irl" />);

    expect(container).toHaveTextContent(
      `${strings.ownerPrefix}${strings.ownerApp({ appId: 'chores4irl' })}`,
    );
    expect(container.querySelector('.owner-badge--app')).not.toBeNull();
  });

  it.each<[string, OwnerKind, string | null]>([
    ['unknown kind', 'unknown', null],
    ['app owner without an id', 'app', null],
    ['app owner with an empty id', 'app', ''],
    ['an unrecognised kind', 'robot' as OwnerKind, null],
    ['an operator owner carrying an app id', 'operator', 'chores4irl'],
  ])('renders %s as an explicit unknown state, never Operator', (_label, ownerKind, ownerAppId) => {
    const { container } = render(<OwnerBadge ownerKind={ownerKind} ownerAppId={ownerAppId} />);

    expect(container).toHaveTextContent(`${strings.ownerPrefix}${strings.ownerUnknown}`);
    expect(container).not.toHaveTextContent(strings.ownerOperator);
    expect(container.querySelector('.owner-badge--unknown')).not.toBeNull();
  });
});

describe('ownerNoteOf', () => {
  it('is null for the operator only', () => {
    expect(ownerNoteOf({ ownerKind: 'operator', ownerAppId: null })).toBeNull();
  });

  it('names the app for an app owner', () => {
    expect(ownerNoteOf({ ownerKind: 'app', ownerAppId: 'chores4irl' })).toBe(
      "Managed by chores4irl's deploy — operator edits are reverted on its next deploy.",
    );
  });

  it.each<[string, OwnerKind, string | null]>([
    ['unknown kind', 'unknown', null],
    ['an unrecognised kind', 'robot' as OwnerKind, null],
    ['an app owner without an id', 'app', null],
    ['an app owner with an empty id', 'app', ''],
    ['an operator owner carrying an app id', 'operator', 'chores4irl'],
  ])('warns for %s, agreeing with the badge', (_label, ownerKind, ownerAppId) => {
    expect(ownerNoteOf({ ownerKind, ownerAppId })).toBe(strings.ownerUnknownNote);
  });
});

describe('ownerInlineNoteOf', () => {
  it('is null for the operator only', () => {
    expect(ownerInlineNoteOf({ ownerKind: 'operator', ownerAppId: null })).toBeNull();
  });

  it('is the short, app-free line for an app owner', () => {
    const note = ownerInlineNoteOf({ ownerKind: 'app', ownerAppId: 'chores4irl' });
    expect(note).toBe('Edits are overridden on next deploy');
    expect(note).not.toContain('chores4irl');
  });

  it.each<[string, OwnerKind, string | null]>([
    ['unknown kind', 'unknown', null],
    ['an unrecognised kind', 'robot' as OwnerKind, null],
    ['an app owner without an id', 'app', null],
    ['an app owner with an empty id', 'app', ''],
    ['an operator owner carrying an app id', 'operator', 'chores4irl'],
  ])(
    'is the short unknown line for %s, agreeing with the long note',
    (_label, ownerKind, ownerAppId) => {
      const note = ownerInlineNoteOf({ ownerKind, ownerAppId });
      expect(note).toBe('Owner unknown — edits may be overridden');
      expect(note).not.toBeNull();
      expect(ownerNoteOf({ ownerKind, ownerAppId })).toBe(strings.ownerUnknownNote);
    },
  );

  it('is short enough to stay on one line under the buttons', () => {
    for (const owner of [
      { ownerKind: 'app' as OwnerKind, ownerAppId: 'a_very_long_app_identifier' },
      { ownerKind: 'unknown' as OwnerKind, ownerAppId: null },
    ]) {
      expect(ownerInlineNoteOf(owner)!.length).toBeLessThanOrEqual(40);
    }
  });
});
