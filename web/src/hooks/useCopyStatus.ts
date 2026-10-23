import { useCallback, useRef, useState } from 'react';

/** Outcome of the latest Copy click; `count` keys a fresh node per click so a repeat is announced. */
export interface CopyStatus {
  kind: 'copied' | 'fallback';
  count: number;
}

interface UseCopyStatusResult {
  status: CopyStatus | null;
  /** Writes `text` to the clipboard and records Copied, or the fallback if that is unavailable. */
  copy: (text: string) => void;
  /** Clears the status and invalidates any clipboard promise still in flight. */
  clear: () => void;
}

/**
 * Clipboard copy with a status that belongs to the current reveal. A clipboard
 * promise that settles after `clear()` is ignored, so a late "Copied" never
 * attaches to a later reveal.
 */
export function useCopyStatus(): UseCopyStatusResult {
  const [status, setStatus] = useState<CopyStatus | null>(null);
  const tokenRef = useRef(0);

  const show = useCallback(({ kind, token }: { kind: CopyStatus['kind']; token: number }) => {
    if (token !== tokenRef.current) {
      return;
    }
    setStatus((previous) => ({ kind, count: (previous?.count ?? 0) + 1 }));
  }, []);

  const copy = useCallback(
    (text: string) => {
      const token = tokenRef.current;
      const clipboard = navigator.clipboard as Clipboard | undefined;
      if (clipboard === undefined) {
        show({ kind: 'fallback', token });
        return;
      }
      try {
        clipboard.writeText(text).then(
          () => show({ kind: 'copied', token }),
          () => show({ kind: 'fallback', token }),
        );
      } catch {
        show({ kind: 'fallback', token });
      }
    },
    [show],
  );

  const clear = useCallback(() => {
    tokenRef.current += 1;
    setStatus(null);
  }, []);

  return { status, copy, clear };
}
