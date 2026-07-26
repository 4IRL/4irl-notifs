import { cleanup, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { ApiError } from '../api/client';
import { strings } from '../strings';
import { AddAppForm } from './AddAppForm';

afterEach(() => {
  cleanup();
});

const publisherResult = {
  appId: 'tasktracker',
  publisherUserId: 'tasktracker-publisher',
  topicPattern: 'tasktracker-*',
  token: 'tk_publisher',
};

describe('AddAppForm', () => {
  it('rejects an invalid app_id without calling onAddApp', async () => {
    const user = userEvent.setup();
    const onAddApp = vi.fn();
    render(<AddAppForm onAddApp={onAddApp} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'Bad-Id');
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), 'Task Tracker');
    await user.click(screen.getByRole('button', { name: strings.addAppAction }));

    expect(screen.getByRole('alert')).toHaveTextContent(strings.invalidAppId);
    expect(onAddApp).not.toHaveBeenCalled();
  });

  it('rejects an empty display name without calling onAddApp', async () => {
    const user = userEvent.setup();
    const onAddApp = vi.fn();
    render(<AddAppForm onAddApp={onAddApp} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'tasktracker');
    await user.click(screen.getByRole('button', { name: strings.addAppAction }));

    expect(screen.getByRole('alert')).toHaveTextContent(strings.invalidDisplayName);
    expect(onAddApp).not.toHaveBeenCalled();
  });

  it('submits trimmed metadata (omitting an empty description) and reveals the token', async () => {
    const user = userEvent.setup();
    const onAddApp = vi.fn().mockResolvedValue(publisherResult);
    render(<AddAppForm onAddApp={onAddApp} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'tasktracker');
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), '  Task Tracker  ');
    await user.click(screen.getByRole('button', { name: strings.addAppAction }));

    expect(onAddApp).toHaveBeenCalledWith({
      appId: 'tasktracker',
      displayName: 'Task Tracker',
      description: undefined,
    });
    expect(await screen.findByText('tk_publisher')).toBeInTheDocument();
  });

  it('shows the server error message when registration fails', async () => {
    const user = userEvent.setup();
    const onAddApp = vi
      .fn()
      .mockRejectedValue(new ApiError({ status: 409, message: 'app already exists' }));
    render(<AddAppForm onAddApp={onAddApp} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'tasktracker');
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), 'Task Tracker');
    await user.click(screen.getByRole('button', { name: strings.addAppAction }));

    expect(await screen.findByText('app already exists')).toBeInTheDocument();
  });
});
