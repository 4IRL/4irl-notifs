import { useCallback, useState } from 'react';

/** Which control of a row has its confirm pair open, by row key. Absent: nothing is confirming. */
type ConfirmingByRow = Readonly<Record<string, string>>;

export interface ConfirmingRows {
  /**
   * True for the row's buttons that must step aside: the row has a confirm pair open and
   * `control` is not the control that opened it. Omit `control` for a plain button (it is never
   * the one confirming).
   */
  isSteppedAside: (params: { rowKey: string; control?: string }) => boolean;
  /** Wire to a ConfirmButton's `onConfirmingChange` for the control `control` of row `rowKey`. */
  reportConfirming: (params: { rowKey: string; control: string; confirming: boolean }) => void;
}

/**
 * Tracks which admin-table rows have a destructive confirm pair open, so a row can hide its
 * other inline buttons while the pair shows: the pair then takes their place and the Actions
 * cell never gets wider than when the row is idle. Rows are independent (several can confirm at
 * once); within a row only the control that opened the pair is recorded.
 */
export function useConfirmingRows(): ConfirmingRows {
  const [confirmingByRow, setConfirmingByRow] = useState<ConfirmingByRow>({});

  const reportConfirming = useCallback(
    ({ rowKey, control, confirming }: { rowKey: string; control: string; confirming: boolean }) => {
      setConfirmingByRow((current) => {
        if (confirming) {
          return current[rowKey] === control ? current : { ...current, [rowKey]: control };
        }
        // Only the control that is recorded may clear the row.
        if (current[rowKey] !== control) {
          return current;
        }
        const next = { ...current };
        delete next[rowKey];
        return next;
      });
    },
    [],
  );

  const isSteppedAside = useCallback(
    ({ rowKey, control }: { rowKey: string; control?: string }) => {
      const open = confirmingByRow[rowKey];
      return open !== undefined && open !== control;
    },
    [confirmingByRow],
  );

  return { isSteppedAside, reportConfirming };
}
