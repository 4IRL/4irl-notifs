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
  createdAt: '2026-11-03T10:00:00Z',
  updatedAt: '2026-11-03T10:00:00Z',
};

const mintedCopy = strings.publisherMintedResult({ appId: 'tasktracker' });

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

  it('submits trimmed metadata (omitting an empty description) and shows the minted status', async () => {
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
    expect(await screen.findByText(mintedCopy)).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent(mintedCopy);
  });

  it('shows the already-registered note in the status region when flagged', async () => {
    const user = userEvent.setup();
    const onAddApp = vi.fn().mockResolvedValue({ ...publisherResult, alreadyRegistered: true });
    render(<AddAppForm onAddApp={onAddApp} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'tasktracker');
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), 'Task Tracker');
    await user.click(screen.getByRole('button', { name: strings.addAppAction }));

    await screen.findByText(mintedCopy);
    expect(screen.getByRole('status')).toHaveTextContent(strings.appAlreadyRegisteredNote);
  });

  it('keeps the status region empty before submit and omits the note on a normal create', async () => {
    const user = userEvent.setup();
    const onAddApp = vi.fn().mockResolvedValue(publisherResult);
    render(<AddAppForm onAddApp={onAddApp} />);
    expect(screen.getByRole('status')).toBeEmptyDOMElement();

    await user.type(screen.getByLabelText(strings.appIdLabel), 'tasktracker');
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), 'Task Tracker');
    await user.click(screen.getByRole('button', { name: strings.addAppAction }));

    await screen.findByText(mintedCopy);
    expect(screen.queryByText(strings.appAlreadyRegisteredNote)).not.toBeInTheDocument();
  });

  it('clears a previous success when the next submit fails validation', async () => {
    const user = userEvent.setup();
    const onAddApp = vi.fn().mockResolvedValue(publisherResult);
    render(<AddAppForm onAddApp={onAddApp} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'tasktracker');
    await user.type(screen.getByLabelText(strings.appDisplayNameLabel), 'Task Tracker');
    await user.click(screen.getByRole('button', { name: strings.addAppAction }));
    await screen.findByText(mintedCopy);

    await user.clear(screen.getByLabelText(strings.appIdLabel));
    await user.type(screen.getByLabelText(strings.appIdLabel), 'Bad-Id');
    await user.click(screen.getByRole('button', { name: strings.addAppAction }));
    expect(screen.getByRole('alert')).toHaveTextContent(strings.invalidAppId);
    expect(screen.getByRole('status')).toBeEmptyDOMElement();

    await user.clear(screen.getByLabelText(strings.appIdLabel));
    await user.type(screen.getByLabelText(strings.appIdLabel), 'tasktracker');
    await user.clear(screen.getByLabelText(strings.appDisplayNameLabel));
    await user.click(screen.getByRole('button', { name: strings.addAppAction }));
    expect(screen.getByRole('alert')).toHaveTextContent(strings.invalidDisplayName);
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
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
