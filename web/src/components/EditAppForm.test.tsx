import type { ComponentProps } from 'react';
import { cleanup, render, screen } from '@testing-library/react';
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

  it('revoke & re-mint requires confirmation, then fires with rotate=true', async () => {
    const user = userEvent.setup();
    const { onRemintToken } = renderForm();

    await user.click(screen.getByRole('button', { name: strings.rotateTokenAction }));
    expect(onRemintToken).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: strings.confirmRotateTokenAction }));
    expect(onRemintToken).toHaveBeenCalledWith({ appId: 'urls4irl', rotate: true });
    expect(await screen.findByText('tk_fresh')).toBeInTheDocument();
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
