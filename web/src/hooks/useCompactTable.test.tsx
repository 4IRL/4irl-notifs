import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { setCompactViewport } from '../test-viewport';
import { COMPACT_TABLE_QUERY, useCompactTable } from './useCompactTable';

function Probe() {
  const compact = useCompactTable();
  return <p>{compact ? 'compact' : 'roomy'}</p>;
}

describe('useCompactTable', () => {
  // (adminTable.css names the very same query; the e2e suite checks the switch exactly at
  // 480px / 481px, since Vitest does not process stylesheets.)
  it('uses the one narrow breakpoint the admin table CSS uses', () => {
    expect(COMPACT_TABLE_QUERY).toBe('(max-width: 30rem)');
  });

  it('is false in the roomy layout (the jsdom default)', () => {
    render(<Probe />);

    expect(screen.getByText('roomy')).toBeInTheDocument();
  });

  it('is true in the narrow layout from the first render', () => {
    setCompactViewport(true);
    render(<Probe />);

    expect(screen.getByText('compact')).toBeInTheDocument();
  });

  it('follows the viewport while mounted', () => {
    render(<Probe />);
    expect(screen.getByText('roomy')).toBeInTheDocument();

    setCompactViewport(true);
    expect(screen.getByText('compact')).toBeInTheDocument();

    setCompactViewport(false);
    expect(screen.getByText('roomy')).toBeInTheDocument();
  });

  it('fails closed to the roomy layout when matchMedia is missing', () => {
    const original = window.matchMedia;
    // @ts-expect-error simulating an environment with no matchMedia
    window.matchMedia = undefined;
    try {
      render(<Probe />);
      expect(screen.getByText('roomy')).toBeInTheDocument();
    } finally {
      window.matchMedia = original;
    }
  });
});
