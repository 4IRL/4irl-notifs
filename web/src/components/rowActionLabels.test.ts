import { describe, expect, it } from 'vitest';

import { visibleTextInName } from '../test-labels';
import { destinationActionLabels, routeActionLabels } from './rowActionLabels';
import type { ActionLabel } from './rowActionLabels';

function violations(labels: object): string[] {
  return (Object.entries(labels) as Array<[string, ActionLabel]>)
    .filter(([, label]) => !visibleTextInName(label))
    .map(([key, label]) => `${key}: "${label.visible}" is not in "${label.accessible}"`);
}

describe('destinationActionLabels', () => {
  it('shows the bare verb and keeps the destination in the accessible name', () => {
    const labels = destinationActionLabels({ name: 'hook1' });

    expect(Object.fromEntries(Object.entries(labels).map(([key, l]) => [key, l.visible]))).toEqual({
      test: 'Test',
      edit: 'Edit',
      disable: 'Disable',
      confirmDisable: 'Confirm disable',
      cancelDisable: 'Cancel',
      enable: 'Enable',
      delete: 'Delete',
      confirmDelete: 'Confirm delete',
      cancelDelete: 'Cancel',
    });
    expect(labels.test.accessible).toBe('Send test to hook1');
    expect(labels.edit.accessible).toBe('Edit hook1');
    expect(labels.confirmDelete.accessible).toBe('Confirm delete hook1');
    expect(labels.cancelDisable.accessible).toBe('Cancel Disable hook1');
  });

  it('keeps every visible text inside its accessible name', () => {
    expect(violations(destinationActionLabels({ name: 'hook1' }))).toEqual([]);
  });
});

describe('routeActionLabels', () => {
  it('shows the bare verb and keeps the route in the accessible name', () => {
    const labels = routeActionLabels({ id: 7 });

    expect(labels.edit).toEqual({ visible: 'Edit', accessible: 'Edit route 7' });
    expect(labels.delete).toEqual({ visible: 'Delete', accessible: 'Delete route 7' });
    expect(labels.confirmDelete).toEqual({
      visible: 'Confirm delete',
      accessible: 'Confirm delete route 7',
    });
    expect(labels.cancelDelete).toEqual({ visible: 'Cancel', accessible: 'Cancel Delete route 7' });
  });

  it('keeps every visible text inside its accessible name', () => {
    expect(violations(routeActionLabels({ id: 7 }))).toEqual([]);
  });
});
