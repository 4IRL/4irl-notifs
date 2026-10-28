import { useLayoutEffect, useRef } from 'react';
import type { ChangeEvent, FocusEvent, CompositionEvent } from 'react';

interface UseLowercaseInputParams {
  setValue: (value: string) => void;
}

interface LowercaseInputHandlers {
  onChange: (event: ChangeEvent<HTMLInputElement>) => void;
  onCompositionStart: () => void;
  onCompositionEnd: (event: CompositionEvent<HTMLInputElement>) => void;
  onBlur: (event: FocusEvent<HTMLInputElement>) => void;
}

interface PendingCaret {
  element: HTMLInputElement;
  caret: number;
}

/**
 * Lowercase-as-you-type for a controlled text input, keeping the caret where the
 * user expects it (React moves it to the end when the value it renders differs
 * from what was typed). While an IME composition is active the value is passed
 * through untouched; it is lowercased on `compositionend` and on blur (Android
 * predictive keyboards can hold an id in composition until blur).
 */
export function useLowercaseInput({ setValue }: UseLowercaseInputParams): LowercaseInputHandlers {
  const composing = useRef(false);
  const pending = useRef<PendingCaret | null>(null);

  function restoreCaret() {
    const target = pending.current;
    pending.current = null;
    if (target !== null && target.element.isConnected) {
      target.element.setSelectionRange(target.caret, target.caret);
    }
  }

  // Runs after every render of the host: applies a caret saved by a handler.
  useLayoutEffect(() => {
    restoreCaret();
  });

  function lowercaseElement({ element }: { element: HTMLInputElement }) {
    const raw = element.value;
    const lower = raw.toLowerCase();
    if (lower === raw) {
      return;
    }
    const selectionStart = element.selectionStart ?? raw.length;
    pending.current = {
      element,
      caret: raw.slice(0, selectionStart).toLowerCase().length,
    };
    setValue(lower);
    // If state already held the lowercase value no render follows; restore anyway.
    queueMicrotask(restoreCaret);
  }

  return {
    onChange(event) {
      const element = event.currentTarget;
      if (composing.current) {
        setValue(element.value);
        return;
      }
      if (element.value === element.value.toLowerCase()) {
        setValue(element.value);
        return;
      }
      lowercaseElement({ element });
    },
    onCompositionStart() {
      composing.current = true;
    },
    onCompositionEnd(event) {
      composing.current = false;
      lowercaseElement({ element: event.currentTarget });
    },
    onBlur(event) {
      composing.current = false;
      lowercaseElement({ element: event.currentTarget });
    },
  };
}
