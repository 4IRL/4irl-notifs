import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { strings } from '../strings';
import { QrHandoff } from './QrHandoff';

// Deliberately NOT mocking qrcode.react: the real encoder throws when the link
// exceeds the QR capacity, which is what this guards against.
describe('QrHandoff capacity', () => {
  it('renders the real QR svg for the worst-case combination of params', () => {
    const prefix = 'https://a.4irl.app/?q=';
    const returnUrl = `${prefix}${'['.repeat(400 - prefix.length)}`;
    const name = '\u{1F600}'.repeat(64);

    render(
      <QrHandoff
        app={'a'.repeat(63)}
        user="u_abcdefghijklmnop"
        name={name}
        returnUrl={returnUrl}
      />,
    );

    const qr = screen.getByRole('img', { name: strings.qrAriaLabel });
    expect(qr.querySelector('svg')).not.toBeNull();
  });
});
