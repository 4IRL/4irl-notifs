import { strings } from '../strings';
import './Steps.css';

interface StoreLinkProps {
  href: string;
  label: string;
}

// An external app-store link. Opens in a new tab, so it carries noopener noreferrer.
export function StoreLink({ href, label }: StoreLinkProps) {
  return (
    <a className="setup-page__link" href={href} target="_blank" rel="noopener noreferrer">
      {label}
      <span aria-hidden="true">{strings.externalLinkMark}</span>
      <span className="setup-page__sr-only">{strings.opensInNewTab}</span>
    </a>
  );
}
