import { useCallback, useEffect, useRef, useState } from 'react';

export interface FocusRegistry {
  /** Ref-callback helper: registers (or, with `null`, unregisters) a focusable control. */
  registerControl: (args: { key: string; element: HTMLElement | null }) => void;
  /** Asks for focus to move to the control registered under `key` once it is mounted. */
  requestFocus: (key: string) => void;
}

/**
 * Registry of focusable controls keyed by name. A focus request whose target is
 * not mounted yet stays pending and is retried after every render until the
 * control registers.
 */
export function useFocusRegistry(): FocusRegistry {
  const [, setFocusTick] = useState(0);
  const controls = useRef(new Map<string, HTMLElement>());
  const pendingFocus = useRef<string | null>(null);

  useEffect(() => {
    const key = pendingFocus.current;
    if (key === null) {
      return;
    }
    const element = controls.current.get(key);
    if (element !== undefined) {
      pendingFocus.current = null;
      // A control an open confirm has stepped aside (the `hidden` attribute) cannot take focus:
      // the row's visible control (the confirm button) keeps it instead of focus being lost.
      const target = element.hidden
        ? element.closest('tr')?.querySelector<HTMLElement>('button:not([hidden])')
        : element;
      target?.focus();
    }
  });

  const registerControl = useCallback(
    ({ key, element }: { key: string; element: HTMLElement | null }) => {
      if (element === null) {
        controls.current.delete(key);
      } else {
        controls.current.set(key, element);
      }
    },
    [],
  );

  const requestFocus = useCallback((key: string) => {
    pendingFocus.current = key;
    setFocusTick((tick) => tick + 1);
  }, []);

  return { registerControl, requestFocus };
}
