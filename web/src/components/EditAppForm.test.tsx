import type { ComponentProps } from 'react';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import type { AppSummary } from '../api/personClient';
import { strings } from '../strings';
import { EditAppForm } from './EditAppForm';

afterEach(() => {
  cleanup();
});

const app: AppSummary = {
  appId: 'urls4irl',
  displayName: 'URLs4IRL',
  description: 'Shared URL app',
  createdAt: '2026-07-25T10:00:00Z',
};

const publisherResult = {
  appId: 'urls4irl',
  publisherUserId: 'urls4irl-publisher',
  topicPattern: 'urls4irl-*',
  createdAt: '2026-11-03T10:00:00Z',
  updatedAt: '2026-11-03T11:00:00Z',
};

function renderForm(overrides: Partial<ComponentProps<typeof EditAppForm>> = {}) {
  const props: ComponentProps<typeof EditAppForm> = {
    app,
    onUpdateApp: vi.fn().mockResolvedValue(undefined),
    onRemintToken: vi.fn().mockResolvedValue(publisherResult),
    onClose: vi.fn(),
    onRequestConfirm: vi.fn(),
    ...overrides,
  };
  render(<EditAppForm {...props} />);
  return props;
}

describe('EditAppForm', () => {
  it('saves edited metadata, clearing an emptied description to null', async () => {
    const user = userEvent.setup();
    const { onUpdateApp } = renderForm();

    const description = screen.getByLabelText(strings.appDescriptionLabel);
    await user.clear(description);
    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    expect(onUpdateApp).toHaveBeenCalledWith({
      appId: 'urls4irl',
      displayName: 'URLs4IRL',
      description: null,
    });
  });

  it('rejects an empty display name without calling onUpdateApp', async () => {
    const user = userEvent.setup();
    const { onUpdateApp } = renderForm();

    await user.clear(screen.getByLabelText(strings.appDisplayNameLabel));
    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    expect(screen.getByRole('alert')).toHaveTextContent(strings.invalidDisplayName);
    expect(onUpdateApp).not.toHaveBeenCalled();
  });

  it('re-mint is additive: fires immediately with rotate=false and announces it', async () => {
    const user = userEvent.setup();
    const { onRemintToken } = renderForm();

    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));

    expect(onRemintToken).toHaveBeenCalledWith({ appId: 'urls4irl', rotate: false });
    expect(await screen.findByRole('status')).toHaveTextContent(
      strings.publisherRemintedResult({ appId: 'urls4irl' }),
    );
  });

  it('revoke & re-mint asks the dialog for a confirm step, then fires with rotate=true', async () => {
    const user = userEvent.setup();
    const { onRemintToken, onRequestConfirm, onUpdateApp, onClose } = renderForm();

    await user.click(screen.getByRole('button', { name: strings.rotateTokenAction }));
    expect(onRemintToken).not.toHaveBeenCalled();
    expect(onRequestConfirm).toHaveBeenCalledTimes(1);
    const step = vi.mocked(onRequestConfirm).mock.calls[0][0];
    expect(step.message).toBe(strings.rotateTokenWarning);
    expect(step.confirmLabel).toBe(strings.confirmRotateTokenAction);
    expect(step.cancelLabel).toBe(strings.cancelAction);
    // No second Modal is opened by the form itself.
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

    step.onConfirm();
    expect(onRemintToken).toHaveBeenCalledTimes(1);
    expect(onRemintToken).toHaveBeenCalledWith({ appId: 'urls4irl', rotate: true });
    await waitFor(() =>
      expect(screen.getByRole('status')).toHaveTextContent(
        strings.publisherRotatedResult({ appId: 'urls4irl' }),
      ),
    );
    expect(onUpdateApp).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  it('clears the status when the next mint starts', async () => {
    const user = userEvent.setup();
    const onRemintToken = vi
      .fn()
      .mockResolvedValueOnce(publisherResult)
      .mockReturnValueOnce(new Promise<never>(() => undefined));
    renderForm({ onRemintToken });

    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));
    await waitFor(() => expect(screen.getByRole('status')).not.toBeEmptyDOMElement());
    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });

  it('closes without a request or a result when Save is pressed on an untouched form', async () => {
    const user = userEvent.setup();
    const { onUpdateApp, onClose } = renderForm();

    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onUpdateApp).not.toHaveBeenCalled();
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });

  it('treats whitespace-only and empty-description differences as untouched', async () => {
    const user = userEvent.setup();
    const onDirtyChange = vi.fn();
    const { onUpdateApp, onClose } = renderForm({
      app: { ...app, description: null },
      onDirtyChange,
    });

    await user.type(screen.getByLabelText(strings.appDescriptionLabel), '   ');
    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    expect(onDirtyChange).not.toHaveBeenCalledWith(true);
    expect(onUpdateApp).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('reports dirty, back to clean on revert, and busy while saving', async () => {
    const user = userEvent.setup();
    const save = (() => {
      let resolve!: () => void;
      const promise = new Promise<void>((res) => {
        resolve = res;
      });
      return { promise, resolve };
    })();
    const onDirtyChange = vi.fn();
    const onBusyChange = vi.fn();
    renderForm({ onDirtyChange, onBusyChange, onUpdateApp: vi.fn().mockReturnValue(save.promise) });

    const name = screen.getByLabelText(strings.appDisplayNameLabel);
    await user.type(name, 'x');
    expect(onDirtyChange).toHaveBeenLastCalledWith(true);
    await user.type(name, '{Backspace}');
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);

    await user.type(name, 'y');
    await user.click(screen.getByRole('button', { name: strings.saveAction }));
    expect(onBusyChange).toHaveBeenLastCalledWith(true);
    expect(screen.getByRole('button', { name: strings.savingApp })).toHaveAttribute(
      'aria-disabled',
      'true',
    );

    save.resolve();
    await waitFor(() => expect(onBusyChange).toHaveBeenLastCalledWith(false));
    // The saved values are the new baseline: the form is clean again.
    expect(onDirtyChange).toHaveBeenLastCalledWith(false);
  });

  it('ignores Save while a re-mint is in flight, so the minted token cannot be lost', async () => {
    const user = userEvent.setup();
    const pending = new Promise<never>(() => undefined);
    const { onUpdateApp } = renderForm({ onRemintToken: vi.fn().mockReturnValue(pending) });

    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), '2');
    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));
    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    expect(onUpdateApp).not.toHaveBeenCalled();
  });

  it('clears a mint status when a save starts', async () => {
    const user = userEvent.setup();
    renderForm();

    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));
    await waitFor(() => expect(screen.getByRole('status')).not.toBeEmptyDOMElement());
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), '3');
    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    await waitFor(() => expect(screen.getByRole('status')).toBeEmptyDOMElement());
  });

  it('closes when the close button is clicked', async () => {
    const user = userEvent.setup();
    const { onClose } = renderForm();

    await user.click(screen.getByRole('button', { name: strings.closeEditAction }));

    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('shows the server error message when saving metadata fails', async () => {
    const user = userEvent.setup();
    renderForm({
      onUpdateApp: vi
        .fn()
        .mockRejectedValue(new ApiError({ status: 404, message: 'app not found' })),
    });

    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), '2');
    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    expect(await screen.findByText('app not found')).toBeInTheDocument();
  });

  it('shows the server error message and shows no status when a re-mint fails', async () => {
    const user = userEvent.setup();
    renderForm({
      onRemintToken: vi
        .fn()
        .mockRejectedValue(new ApiError({ status: 502, message: 'upstream unreachable' })),
    });

    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));

    expect(await screen.findByText('upstream unreachable')).toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('upstream unreachable');
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });
});
