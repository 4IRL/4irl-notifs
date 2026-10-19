import type { ReactNode } from 'react';

import './Steps.css';

interface StepProps {
  number: number;
  title: string;
  children: ReactNode;
}

// One numbered step card. Steps live in an <ol>, so the number is announced by list
// semantics and the visual badge is hidden from assistive tech.
export function Step({ number, title, children }: StepProps) {
  return (
    <li className="setup-page__step">
      <h2 className="setup-page__step-h">
        <span className="setup-page__num" aria-hidden="true">
          {number}
        </span>
        {title}
      </h2>
      {children}
    </li>
  );
}
