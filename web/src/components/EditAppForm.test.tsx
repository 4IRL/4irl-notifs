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
  token: 'tk_fresh',
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

  it('re-mint is additive: fires immediately with rotate=false and reveals the token', async () => {
    const user = userEvent.setup();
    const { onRemintToken } = renderForm();

    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));

    expect(onRemintToken).toHaveBeenCalledWith({ appId: 'urls4irl', rotate: false });
    expect(await screen.findByText('tk_fresh')).toBeInTheDocument();
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
    expect(await screen.findByText('tk_fresh')).toBeInTheDocument();
    expect(onUpdateApp).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  it('reports the token as shown, and as cleared when a new mint starts and on unmount', async () => {
    const user = userEvent.setup();
    const onTokenShownChange = vi.fn();
    renderForm({ onTokenShownChange });

    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));
    await screen.findByText('tk_fresh');
    expect(onTokenShownChange).toHaveBeenLastCalledWith(true);

    cleanup();
    expect(onTokenShownChange).toHaveBeenLastCalledWith(false);
  });

  it('closes without a request or a result when Save is pressed on an untouched form', async () => {
    const user = userEvent.setup();
    const { onUpdateApp, onClose } = renderForm();

    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onUpdateApp).not.toHaveBeenCalled();
    expect(screen.queryByRole('status')).not.toBeInTheDocument();
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

  it('announces a save inside the form only while a token is shown', async () => {
    const user = userEvent.setup();
    renderForm();

    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), '2');
    await user.click(screen.getByRole('button', { name: strings.saveAction }));
    await waitFor(() =>
      expect(screen.getByRole('button', { name: strings.saveAction })).toBeInTheDocument(),
    );
    expect(screen.queryByRole('status')).not.toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));
    await screen.findByText('tk_fresh');
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), '3');
    await user.click(screen.getByRole('button', { name: strings.saveAction }));

    expect(await screen.findByRole('status')).toHaveTextContent(
      strings.appSavedResult({ appId: 'urls4irl' }),
    );
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

  it('shows the server error message and reveals no token when a re-mint fails', async () => {
    const user = userEvent.setup();
    renderForm({
      onRemintToken: vi
        .fn()
        .mockRejectedValue(new ApiError({ status: 502, message: 'upstream unreachable' })),
    });

    await user.click(screen.getByRole('button', { name: strings.remintTokenAction }));

    expect(await screen.findByText('upstream unreachable')).toBeInTheDocument();
    expect(screen.queryByText('tk_fresh')).not.toBeInTheDocument();
  });
});
