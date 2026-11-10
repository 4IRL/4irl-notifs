import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import type { HeldCounts } from '../heldCounts';
import { strings } from '../strings';
import { HeldCount } from './HeldCount';

afterEach(() => {
  cleanup();
});

const loading: HeldCounts = { status: 'loading' };
const unavailable: HeldCounts = { status: 'unavailable' };

function ready(byApp: Record<string, number>): HeldCounts {
  return { status: 'ready', byApp: new Map(Object.entries(byApp)), unassignedTotal: 0 };
}

function renderCell({ held, appId, variant }: Parameters<typeof HeldCount>[0]) {
  return render(<HeldCount held={held} appId={appId} variant={variant} />);
}

describe('HeldCount cell variant', () => {
  it('shows the loading placeholder and never the unavailable text', () => {
    const { container } = renderCell({ held: loading, appId: 'a', variant: 'cell' });
    expect(screen.getByText(strings.heldLoading)).toBeInTheDocument();
    expect(container).not.toHaveTextContent('unavailable');
    expect(container.querySelector('.visually-hidden')).toBeNull();
  });

  it('shows the no-value glyph with hidden unavailable text, never 0', () => {
    const { container } = renderCell({ held: unavailable, appId: 'a', variant: 'cell' });
    expect(container).toHaveTextContent(`${strings.noValue}${strings.heldUnavailableHidden}`);
    expect(container.querySelector('.visually-hidden')).toHaveTextContent('unavailable');
    expect(container).not.toHaveTextContent('0');
  });

  it('shows plain 0 without a pill when ready with no entry for the app', () => {
    const { container } = renderCell({ held: ready({}), appId: 'a', variant: 'cell' });
    expect(container).toHaveTextContent('0');
    expect(container.querySelector('.held-count__pill')).toBeNull();
  });

  it('shows an "N held" pill when ready with a positive count', () => {
    const { container } = renderCell({ held: ready({ a: 3 }), appId: 'a', variant: 'cell' });
    expect(screen.getByText('3 held')).toHaveClass('held-count__pill');
    expect(container.querySelector('.held-count')).not.toBeNull();
  });
});

describe('HeldCount sub variant', () => {
  it('shows Held: Checking… while loading', () => {
    const { container } = renderCell({ held: loading, appId: 'a', variant: 'sub' });
    expect(container).toHaveTextContent(`${strings.heldSubLabel}${strings.heldLoading}`);
    expect(container).not.toHaveTextContent('unavailable');
  });

  it('shows Held: with the glyph and hidden unavailable text when unavailable', () => {
    const { container } = renderCell({ held: unavailable, appId: 'a', variant: 'sub' });
    expect(container).toHaveTextContent(`${strings.heldSubLabel}${strings.noValue}`);
    expect(container.querySelector('.visually-hidden')).toHaveTextContent('unavailable');
    expect(container).not.toHaveTextContent('0 held');
  });

  it('shows plain "0 held" when ready and zero', () => {
    const { container } = renderCell({ held: ready({}), appId: 'a', variant: 'sub' });
    expect(container).toHaveTextContent(strings.heldZeroSub);
    expect(container.querySelector('.held-count__pill')).toBeNull();
  });

  it('shows the pill when ready and positive', () => {
    renderCell({ held: ready({ a: 3 }), appId: 'a', variant: 'sub' });
    expect(screen.getByText('3 held')).toHaveClass('held-count__pill');
  });
});
