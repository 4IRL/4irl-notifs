import { useEffect, useRef, useState } from 'react';

import { strings } from '../strings';
import './CopyField.css';

const COPIED_DURATION_MS = 2000;

interface CopyFieldProps {
  label: string;
  value: string;
}

// One labelled value with its own Copy button. Each instance owns its copied and
// error state, so copying one field never changes another.
export function CopyField({ label, value }: CopyFieldProps) {
  const [copied, setCopied] = useState(false);
  const [copyError, setCopyError] = useState(false);
  const resetTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mountedRef = useRef(false);
  const attemptRef = useRef(0);

  function clearResetTimer() {
    if (resetTimerRef.current !== null) {
      clearTimeout(resetTimerRef.current);
      resetTimerRef.current = null;
    }
  }

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      clearResetTimer();
    };
  }, []);

  async function handleCopy() {
    // A new click restarts the "Copied!" window and forgets any earlier failure.
    clearResetTimer();
    setCopyError(false);
    // Overlapping clicks: only the most recent attempt may report its outcome.
    attemptRef.current += 1;
    const attempt = attemptRef.current;

    try {
      // navigator.clipboard is undefined outside secure contexts.
      if (!navigator.clipboard) {
        throw new Error('Clipboard API unavailable');
      }
      await navigator.clipboard.writeText(value);
    } catch {
      if (!mountedRef.current || attempt !== attemptRef.current) return;
      setCopied(false);
      setCopyError(true);
      return;
    }

    if (!mountedRef.current || attempt !== attemptRef.current) return;
    setCopied(true);
    resetTimerRef.current = setTimeout(() => {
      resetTimerRef.current = null;
      setCopied(false);
    }, COPIED_DURATION_MS);
  }

  return (
    <div className="setup-page__copy-field">
      <span className="setup-page__label">{label}</span>
      <div className="setup-page__field">
        <span className="setup-page__value">{value}</span>
        <button
          type="button"
          className={copied ? 'setup-page__copy setup-page__copy--done' : 'setup-page__copy'}
          aria-label={copied ? strings.copiedButton : strings.copyButtonLabel(label)}
          onClick={handleCopy}
        >
          {copied ? strings.copiedButton : strings.copyButton}
        </button>
      </div>
      <span className="setup-page__sr-only" role="status" aria-live="polite">
        {copied ? strings.copiedAnnouncement(label) : ''}
      </span>
      {copyError && (
        <p className="setup-page__copy-error" role="alert">
          {strings.copyError}
        </p>
      )}
    </div>
  );
}
