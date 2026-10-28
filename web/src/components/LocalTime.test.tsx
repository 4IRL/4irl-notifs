import { cleanup, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';

import { useTimeZone } from '../test-timezone';
import { LocalTime } from './LocalTime';

afterEach(() => {
  cleanup();
});

describe('LocalTime', () => {
  it('renders a time element whose dateTime and title are the UTC ISO instant', () => {
    render(<LocalTime timestamp="2026-10-17T08:12:44Z" />);

    const time = screen.getByText('2026-10-17 08:12 UTC');
    expect(time.tagName).toBe('TIME');
    expect(time).toHaveAttribute('datetime', '2026-10-17T08:12:44.000Z');
    expect(time).toHaveAttribute('title', '2026-10-17T08:12:44.000Z');
  });

  it('reads an offset-less timestamp as UTC for the attributes too', () => {
    render(<LocalTime timestamp="2026-10-17 08:12:44" />);

    expect(screen.getByText('2026-10-17 08:12 UTC')).toHaveAttribute(
      'datetime',
      '2026-10-17T08:12:44.000Z',
    );
  });

  it('shows seconds and no zone for the time-of-day format', () => {
    render(<LocalTime timestamp="2026-10-17T08:12:44Z" format="timeOfDaySeconds" />);

    expect(screen.getByText('08:12:44')).toHaveAttribute('title', '2026-10-17T08:12:44.000Z');
  });

  it('renders an unparseable value as raw text without datetime or title', () => {
    render(<LocalTime timestamp="soon" />);

    const time = screen.getByText('soon');
    expect(time).not.toHaveAttribute('datetime');
    expect(time).not.toHaveAttribute('title');
  });

  it('applies the className', () => {
    render(<LocalTime timestamp="2026-10-17T08:12:44Z" className="mono" />);

    expect(screen.getByText('2026-10-17 08:12 UTC')).toHaveClass('mono');
  });
});

describe('LocalTime in a non-UTC zone', () => {
  useTimeZone('America/Los_Angeles');

  it('renders the local value while keeping the UTC instant', () => {
    render(<LocalTime timestamp="2026-10-17T08:12:44Z" />);

    const time = screen.getByText('2026-10-17 01:12 PDT');
    expect(time).toHaveAttribute('datetime', '2026-10-17T08:12:44.000Z');
  });
});
