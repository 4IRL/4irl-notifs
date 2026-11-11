import type { JSX } from 'react';

import { heldCountFor } from '../heldCounts';
import type { HeldCounts } from '../heldCounts';
import { strings } from '../strings';
import './HeldCount.css';
import './visuallyHidden.css';

interface HeldCountProps {
  held: HeldCounts;
  appId: string;
  /** `cell` is the table cell text; `sub` is the compact lead-cell sub-line. */
  variant: 'cell' | 'sub';
}

/** One app's held-message count; loading, unavailable, zero and "N held" look the same everywhere. */
export function HeldCount({ held, appId, variant }: HeldCountProps): JSX.Element {
  const sub = variant === 'sub';
  const prefix = sub ? strings.heldSubLabel : '';
  if (held.status === 'loading') {
    return (
      <span className="held-count">
        {prefix}
        {strings.heldLoading}
      </span>
    );
  }
  if (held.status === 'unavailable') {
    return (
      <span className="held-count">
        {prefix}
        {strings.noValue}
        <span className="visually-hidden">{strings.heldUnavailableHidden}</span>
      </span>
    );
  }
  const count = heldCountFor({ held, appId });
  if (count === 0) {
    return <span className="held-count">{sub ? strings.heldZeroSub : strings.heldZeroCell}</span>;
  }
  return (
    <span className="held-count">
      <span className="held-count__pill">{strings.heldPill({ count })}</span>
    </span>
  );
}
