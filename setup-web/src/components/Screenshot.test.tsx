import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { screenshots } from '../screenshots';
import type { ScreenshotKey } from '../screenshots';
import { Screenshot } from './Screenshot';

const KEYS = Object.keys(screenshots) as ScreenshotKey[];

describe('Screenshot', () => {
  it.each(KEYS)('renders %s from the manifest with explicit size and lazy loading', (key) => {
    render(<Screenshot name={key} />);

    const { src, alt, width, height } = screenshots[key];
    const image = screen.getByRole('img', { name: alt });
    expect(image).toHaveAttribute('src', src);
    expect(image).toHaveAttribute('width', String(width));
    expect(image).toHaveAttribute('height', String(height));
    expect(image).toHaveAttribute('loading', 'lazy');
  });
});
