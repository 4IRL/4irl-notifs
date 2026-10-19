import { QRCodeSVG } from 'qrcode.react';

import { strings } from '../strings';
import './QrHandoff.css';

interface QrHandoffProps {
  app: string;
  user: string;
  name: string | null;
}

/**
 * Desktop-only card that hands this exact page over to a phone. QRCodeSVG renders
 * a bare <svg> with no accessible name, so the wrapper carries role="img" and the
 * label. Dark modules on a white padded wrapper keep the code scannable in the
 * dark theme. The QR is generated in the browser; nothing is sent anywhere.
 *
 * The code carries a canonical link rebuilt from the already-validated params, never
 * `window.location.href`: unknown params and the hash are dropped, which also bounds
 * the length so QRCodeSVG cannot throw on an oversized URL. `name` is null when the
 * link had no valid name (the phone then falls back to `app` the same way).
 */
export function QrHandoff({ app, user, name }: QrHandoffProps) {
  const link = new URL(window.location.pathname, window.location.origin);
  link.searchParams.set('app', app);
  link.searchParams.set('user', user);
  if (name !== null) {
    link.searchParams.set('name', name);
  }

  return (
    <section className="setup-page__qr-card">
      <h2 className="setup-page__qr-heading">{strings.qrHeading}</h2>
      <div className="setup-page__qr" role="img" aria-label={strings.qrAriaLabel}>
        <QRCodeSVG value={link.href} level="M" fgColor="#000000" bgColor="#ffffff" />
      </div>
      <p>{strings.qrCaption}</p>
    </section>
  );
}
