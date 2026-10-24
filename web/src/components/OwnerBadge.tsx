import type { JSX } from 'react';

import type { OwnerFields } from '../api/deliveryClient';
import { strings } from '../strings';
import { classifyOwner } from './ownership';
import './OwnerBadge.css';

/**
 * Who owns a destination, route or check. Always text (never colour alone), with a
 * visually hidden "Owner: " prefix so a screen reader can tell it from the row's
 * own app id. A missing, unrecognised or inconsistent owner renders as an explicit
 * unknown state, never as the operator.
 */
export function OwnerBadge(owner: OwnerFields): JSX.Element {
  const classified = classifyOwner(owner);
  let label: string;
  switch (classified.kind) {
    case 'operator':
      label = strings.ownerOperator;
      break;
    case 'app':
      label = strings.ownerApp({ appId: classified.appId });
      break;
    default:
      label = strings.ownerUnknown;
  }
  return (
    <span className={`owner-badge owner-badge--${classified.kind}`}>
      <span className="visually-hidden">{strings.ownerPrefix}</span>
      {label}
    </span>
  );
}
