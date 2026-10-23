import { act, renderHook } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { useConfirmingRows } from './useConfirmingRows';

describe('useConfirmingRows', () => {
  it('steps a row aside only while another of its controls is confirming', () => {
    const { result } = renderHook(() => useConfirmingRows());
    expect(result.current.isSteppedAside({ rowKey: 'a' })).toBe(false);

    act(() =>
      result.current.reportConfirming({ rowKey: 'a', control: 'delete', confirming: true }),
    );

    // A plain button and a different control of the row step aside; the confirming one does not.
    expect(result.current.isSteppedAside({ rowKey: 'a' })).toBe(true);
    expect(result.current.isSteppedAside({ rowKey: 'a', control: 'disable' })).toBe(true);
    expect(result.current.isSteppedAside({ rowKey: 'a', control: 'delete' })).toBe(false);
    // Other rows are untouched.
    expect(result.current.isSteppedAside({ rowKey: 'b' })).toBe(false);

    act(() =>
      result.current.reportConfirming({ rowKey: 'a', control: 'delete', confirming: false }),
    );
    expect(result.current.isSteppedAside({ rowKey: 'a' })).toBe(false);
  });

  it('keeps rows independent and lets only the recorded control close a row', () => {
    const { result } = renderHook(() => useConfirmingRows());

    act(() => {
      result.current.reportConfirming({ rowKey: 'a', control: 'delete', confirming: true });
      result.current.reportConfirming({ rowKey: 'b', control: 'pause', confirming: true });
    });
    expect(result.current.isSteppedAside({ rowKey: 'a' })).toBe(true);
    expect(result.current.isSteppedAside({ rowKey: 'b' })).toBe(true);

    // A stale close from a control that is not the recorded one changes nothing.
    act(() =>
      result.current.reportConfirming({ rowKey: 'a', control: 'disable', confirming: false }),
    );
    expect(result.current.isSteppedAside({ rowKey: 'a' })).toBe(true);

    act(() =>
      result.current.reportConfirming({ rowKey: 'a', control: 'delete', confirming: false }),
    );
    expect(result.current.isSteppedAside({ rowKey: 'a' })).toBe(false);
    expect(result.current.isSteppedAside({ rowKey: 'b' })).toBe(true);
  });

  it('keeps a stable reporter and ignores a repeated report', () => {
    const { result } = renderHook(() => useConfirmingRows());
    const { reportConfirming } = result.current;

    act(() => reportConfirming({ rowKey: 'a', control: 'delete', confirming: true }));
    const afterFirst = result.current.isSteppedAside;
    act(() => reportConfirming({ rowKey: 'a', control: 'delete', confirming: true }));

    expect(result.current.reportConfirming).toBe(reportConfirming);
    expect(result.current.isSteppedAside).toBe(afterFirst);
  });
});
