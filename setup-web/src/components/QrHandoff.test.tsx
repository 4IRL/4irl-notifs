import { render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { strings } from '../strings';
import { QrHandoff } from './QrHandoff';

const qrProps = vi.hoisted(() => ({ calls: [] as Array<{ value: string }> }));

vi.mock('qrcode.react', () => ({
  QRCodeSVG: (props: { value: string }) => {
    qrProps.calls.push(props);
    return <svg data-testid="qr-svg" />;
  },
}));

const USER = 'u_abcdefghijklmnop';

afterEach(() => {
  qrProps.calls.length = 0;
  window.history.replaceState(null, '', '/');
});

describe('QrHandoff', () => {
  it('encodes a canonical link from the validated app, user and name', () => {
    window.history.replaceState(null, '', '/');
    render(<QrHandoff app="chores4irl" user={USER} name="Chore Tracker" />);

    expect(qrProps.calls.at(-1)?.value).toBe(
      `${window.location.origin}/?app=chores4irl&user=${USER}&name=Chore+Tracker`,
    );
  });

  it('omits name when the link had no valid name', () => {
    render(<QrHandoff app="chores4irl" user={USER} name={null} />);

    expect(qrProps.calls.at(-1)?.value).toBe(
      `${window.location.origin}/?app=chores4irl&user=${USER}`,
    );
  });

  it('drops unknown params and the hash, so an oversized link still renders', () => {
    const oversized = 'x'.repeat(5000);
    window.history.replaceState(
      null,
      '',
      `/?app=chores4irl&user=${USER}&junk=${oversized}#${oversized}`,
    );
    render(<QrHandoff app="chores4irl" user={USER} name={null} />);

    expect(screen.getByRole('img', { name: strings.qrAriaLabel })).toBeInTheDocument();
    const value = qrProps.calls.at(-1)?.value;
    expect(value).toBe(`${window.location.origin}/?app=chores4irl&user=${USER}`);
    expect(value).not.toContain('junk');
    expect(value).not.toContain('#');
  });

  it('gives the QR an accessible name and shows the caption', () => {
    render(<QrHandoff app="chores4irl" user={USER} name={null} />);

    const qr = screen.getByRole('img', { name: strings.qrAriaLabel });
    expect(qr).toContainElement(screen.getByTestId('qr-svg'));
    expect(screen.getByRole('heading', { name: strings.qrHeading })).toBeInTheDocument();
    expect(screen.getByText(strings.qrCaption)).toBeInTheDocument();
  });
});
