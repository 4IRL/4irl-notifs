import { useCompactTable } from '../hooks/useCompactTable';

import './RefreshButton.css';

interface RefreshButtonProps {
  /** The accessible name; also the visible text at roomy width and the tooltip at compact. */
  label: string;
  onClick: () => void;
  /** A real (native) disabled state. */
  disabled?: boolean;
  /**
   * A refresh is in flight. The button stays focusable (`aria-disabled`, not native `disabled`)
   * so a focused button keeps focus, and clicks are ignored.
   */
  busy?: boolean;
  /** The site's own ghost-button class, used at roomy width. */
  className?: string;
}

/**
 * The refresh control of the five Delivery panels: the site's ghost text button at roomy width,
 * a 44px icon-only button (double arrow) at the compact breakpoint. One element is rendered, never
 * both. The accessible name is the label at both widths.
 */
export function RefreshButton({
  label,
  onClick,
  disabled = false,
  busy = false,
  className,
}: RefreshButtonProps) {
  const compact = useCompactTable();
  const inert = busy || disabled;

  function handleClick() {
    if (busy) {
      return;
    }
    onClick();
  }

  const shared = {
    type: 'button' as const,
    disabled,
    'aria-disabled': busy && !disabled ? ('true' as const) : undefined,
    'aria-busy': busy ? ('true' as const) : undefined,
    onClick: handleClick,
  };

  if (compact) {
    return (
      <button
        {...shared}
        className="refresh-button__icon"
        aria-label={label}
        title={label}
        data-dimmed={inert ? 'true' : undefined}
      >
        <svg
          className="refresh-button__glyph"
          viewBox="0 0 24 24"
          width="20"
          height="20"
          aria-hidden="true"
          focusable="false"
        >
          <path
            d="M20 12a8 8 0 0 1-14 5.3M4 12a8 8 0 0 1 14-5.3M18 3v4h-4M6 21v-4h4"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          />
        </svg>
      </button>
    );
  }

  return (
    <button {...shared} className={className}>
      {label}
    </button>
  );
}
