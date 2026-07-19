import { cleanup, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import { strings } from '../strings';
import { ProvisionForm } from './ProvisionForm';

afterEach(() => {
  cleanup();
});

describe('ProvisionForm', () => {
  it('renders the heading, labeled inputs, and provision button', () => {
    const onProvision = vi.fn();

    render(<ProvisionForm onProvision={onProvision} />);

    expect(screen.getByRole('heading', { name: strings.provisionHeading })).toBeInTheDocument();
    expect(screen.getByLabelText(strings.appIdLabel)).toBeInTheDocument();
    expect(screen.getByPlaceholderText(strings.appIdPlaceholder)).toBeInTheDocument();
    expect(screen.getByLabelText(strings.userIdLabel)).toBeInTheDocument();
    expect(screen.getByPlaceholderText(strings.userIdPlaceholder)).toBeInTheDocument();
    expect(screen.getByLabelText(strings.emailLabel)).toBeInTheDocument();
    expect(screen.getByPlaceholderText(strings.emailPlaceholder)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: strings.provisionAction })).toBeInTheDocument();
  });

  it('blocks submit and shows an error when the App ID is invalid', async () => {
    const user = userEvent.setup();
    const onProvision = vi.fn();

    render(<ProvisionForm onProvision={onProvision} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'Invalid App!');
    await user.type(screen.getByLabelText(strings.userIdLabel), 'alice');
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText(strings.invalidAppId)).toBeInTheDocument();
    expect(onProvision).not.toHaveBeenCalled();
  });

  it('blocks submit and shows an error when the User ID is invalid', async () => {
    const user = userEvent.setup();
    const onProvision = vi.fn();

    render(<ProvisionForm onProvision={onProvision} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.type(screen.getByLabelText(strings.userIdLabel), 'Invalid User!');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText(strings.invalidUserId)).toBeInTheDocument();
    expect(onProvision).not.toHaveBeenCalled();
  });

  it('blocks submit and shows an error when the Email is invalid, checked after App ID and User ID', async () => {
    const user = userEvent.setup();
    const onProvision = vi.fn();

    render(<ProvisionForm onProvision={onProvision} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.type(screen.getByLabelText(strings.userIdLabel), 'alice');
    await user.type(screen.getByLabelText(strings.emailLabel), 'not-an-email');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText(strings.invalidEmail)).toBeInTheDocument();
    expect(onProvision).not.toHaveBeenCalled();
  });

  it('calls onProvision with the app/user/email params on a valid submit', async () => {
    const user = userEvent.setup();
    const onProvision = vi.fn().mockResolvedValue({
      userId: 'u_76gzqgp4byjl6dje',
      appId: 'urls4irl',
      personHash: '76gzqgp4byjl6dje',
      topicPattern: 'urls4irl-76gzqgp4byjl6dje-*',
      token: 'tk_abc123',
    });

    render(<ProvisionForm onProvision={onProvision} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.type(screen.getByLabelText(strings.userIdLabel), 'alice');
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    await waitFor(() => {
      expect(onProvision).toHaveBeenCalledWith({
        appId: 'urls4irl',
        userId: 'alice',
        email: 'alice@example.com',
      });
    });
  });

  it('shows the pending state and disables the button while onProvision is in flight', async () => {
    const user = userEvent.setup();
    let resolveProvision: (value: {
      userId: string;
      appId: string;
      topicPattern: string;
      token: string;
    }) => void = () => {};
    const pending = new Promise<{
      userId: string;
      appId: string;
      topicPattern: string;
      token: string;
    }>((resolve) => {
      resolveProvision = resolve;
    });
    const onProvision = vi.fn().mockReturnValue(pending);

    render(<ProvisionForm onProvision={onProvision} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.type(screen.getByLabelText(strings.userIdLabel), 'alice');
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    const pendingButton = await screen.findByRole('button', { name: strings.provisioning });
    expect(pendingButton).toBeDisabled();

    resolveProvision({
      userId: 'alice',
      appId: 'urls4irl',
      topicPattern: 'urls4irl_alice',
      token: 'tk_abc123',
    });

    await waitFor(() => {
      expect(screen.getByRole('button', { name: strings.provisionAction })).not.toBeDisabled();
    });
  });

  it('shows the token-reveal lead and token on a successful provision', async () => {
    const user = userEvent.setup();
    const onProvision = vi.fn().mockResolvedValue({
      userId: 'alice',
      appId: 'urls4irl',
      topicPattern: 'urls4irl_alice',
      token: 'tk_abc123',
    });

    render(<ProvisionForm onProvision={onProvision} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.type(screen.getByLabelText(strings.userIdLabel), 'alice');
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(
      await screen.findByText(strings.tokenRevealLead({ userId: 'alice', appId: 'urls4irl' })),
    ).toBeInTheDocument();
    expect(screen.getByText('tk_abc123')).toBeInTheDocument();
  });

  it('shows the ApiError message when onProvision rejects with an ApiError', async () => {
    const user = userEvent.setup();
    const onProvision = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 409, message: 'user already provisioned' }));

    render(<ProvisionForm onProvision={onProvision} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.type(screen.getByLabelText(strings.userIdLabel), 'alice');
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText('user already provisioned')).toBeInTheDocument();
    expect(screen.queryByText('tk_abc123')).not.toBeInTheDocument();
  });

  it('shows the generic error message when onProvision rejects with a non-ApiError', async () => {
    const user = userEvent.setup();
    const onProvision = vi.fn().mockRejectedValue(new Error('network down'));

    render(<ProvisionForm onProvision={onProvision} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.type(screen.getByLabelText(strings.userIdLabel), 'alice');
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText(strings.genericError)).toBeInTheDocument();
    expect(screen.queryByText('network down')).not.toBeInTheDocument();
  });

  it('clears a prior token reveal when a later submit fails', async () => {
    const user = userEvent.setup();
    const onProvision = vi
      .fn()
      .mockResolvedValueOnce({
        userId: 'alice',
        appId: 'urls4irl',
        topicPattern: 'urls4irl_alice',
        token: 'tk_abc123',
      })
      .mockRejectedValueOnce(new ApiError({ status: 500, message: 'server exploded' }));

    render(<ProvisionForm onProvision={onProvision} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.type(screen.getByLabelText(strings.userIdLabel), 'alice');
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText('tk_abc123')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText('server exploded')).toBeInTheDocument();
    expect(screen.queryByText('tk_abc123')).not.toBeInTheDocument();
  });

  it('clears a prior error when a later submit succeeds', async () => {
    const user = userEvent.setup();
    const onProvision = vi
      .fn()
      .mockRejectedValueOnce(new ApiError({ status: 500, message: 'server exploded' }))
      .mockResolvedValueOnce({
        userId: 'alice',
        appId: 'urls4irl',
        topicPattern: 'urls4irl_alice',
        token: 'tk_abc123',
      });

    render(<ProvisionForm onProvision={onProvision} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.type(screen.getByLabelText(strings.userIdLabel), 'alice');
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText('server exploded')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText('tk_abc123')).toBeInTheDocument();
    expect(screen.queryByText('server exploded')).not.toBeInTheDocument();
  });
});
