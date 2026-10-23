import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react';
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
    expect(screen.getByLabelText(strings.emailLabel)).toBeInTheDocument();
    expect(screen.getByPlaceholderText(strings.emailPlaceholder)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: strings.provisionAction })).toBeInTheDocument();
    // The provision path is email-keyed: there is no User ID input.
    expect(screen.queryByLabelText(strings.userIdLabel)).not.toBeInTheDocument();
  });

  it('blocks submit and shows an error when the App ID is invalid', async () => {
    const user = userEvent.setup();
    const onProvision = vi.fn();

    render(<ProvisionForm onProvision={onProvision} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'Invalid App!');
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText(strings.invalidAppId)).toBeInTheDocument();
    expect(onProvision).not.toHaveBeenCalled();
  });

  it('blocks submit and shows an error when the Email is invalid, checked after App ID', async () => {
    const user = userEvent.setup();
    const onProvision = vi.fn();

    render(<ProvisionForm onProvision={onProvision} />);

    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.type(screen.getByLabelText(strings.emailLabel), 'not-an-email');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText(strings.invalidEmail)).toBeInTheDocument();
    expect(onProvision).not.toHaveBeenCalled();
  });

  it('calls onProvision with the app/email params on a valid submit', async () => {
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
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    await waitFor(() => {
      expect(onProvision).toHaveBeenCalledWith({
        appId: 'urls4irl',
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
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText('server exploded')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText('tk_abc123')).toBeInTheDocument();
    expect(screen.queryByText('server exploded')).not.toBeInTheDocument();
  });
});

describe('ProvisionForm password reveal', () => {
  const PASSWORD = 'abcd-efgh-ijkm-npqr';
  const baseResult = {
    userId: 'alice',
    appId: 'urls4irl',
    personHash: 'abcdefgh23456777',
    topicPattern: 'urls4irl_alice',
    broadcastTopic: 'urls4irl-broadcast',
    token: 'tk_abc123',
  };
  const withPassword = { ...baseResult, password: PASSWORD };
  const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');

  afterEach(() => {
    vi.restoreAllMocks();
    if (originalClipboard) {
      Object.defineProperty(navigator, 'clipboard', originalClipboard);
    } else {
      Reflect.deleteProperty(navigator, 'clipboard');
    }
  });

  function setClipboard(value: unknown) {
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value });
  }

  async function fillAndSubmit(user: ReturnType<typeof userEvent.setup>) {
    await user.clear(screen.getByLabelText(strings.appIdLabel));
    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.clear(screen.getByLabelText(strings.emailLabel));
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));
  }

  async function revealPassword() {
    const user = userEvent.setup();
    const onProvision = vi.fn().mockResolvedValue(withPassword);
    render(<ProvisionForm onProvision={onProvision} />);
    await fillAndSubmit(user);
    await screen.findByLabelText(strings.passwordLabel);
    return { user, onProvision };
  }

  it('has exactly one persistent empty status region with aria-atomic false before any submit', () => {
    const { container } = render(<ProvisionForm onProvision={vi.fn()} />);

    const statuses = container.querySelectorAll('[role="status"]');
    expect(statuses).toHaveLength(1);
    expect(statuses[0]).toBeEmptyDOMElement();
    expect(statuses[0]).toHaveAttribute('aria-atomic', 'false');
  });

  it('puts the lead sentence in the status region and the password in a labelled read-only input', async () => {
    await revealPassword();

    const statuses = document.querySelectorAll('[role="status"]');
    expect(statuses).toHaveLength(1);
    const status = statuses[0] as HTMLElement;
    expect(status).toHaveTextContent(strings.passwordRevealLead);
    expect(status.closest('.provision-form__token-reveal')).toBeNull();

    const input = screen.getByLabelText(strings.passwordLabel) as HTMLInputElement;
    expect(input).toHaveValue(PASSWORD);
    expect(input).toHaveAttribute('readonly');
    expect(input).toHaveAttribute('autocomplete', 'off');
    expect(input).toHaveAttribute('spellcheck', 'false');
    expect(input).not.toHaveAttribute('title');
    expect(input).not.toHaveAttribute('aria-label');
    expect(status.contains(input)).toBe(false);
    expect(status).not.toHaveTextContent(PASSWORD);

    const passwordGroup = screen.getByRole('group', { name: strings.passwordGroupHeading });
    expect(passwordGroup).toContainElement(input);
    const tokenGroup = screen.getByRole('group', { name: strings.tokenGroupHeading });
    expect(
      tokenGroup.textContent?.includes(
        strings.tokenRevealLead({ userId: 'alice', appId: 'urls4irl' }),
      ),
    ).toBe(true);
  });

  it('orders the DOM as status region, password group, token group', async () => {
    await revealPassword();

    const status = document.querySelector('[role="status"]') as HTMLElement;
    const passwordGroup = screen.getByRole('group', { name: strings.passwordGroupHeading });
    const tokenGroup = screen.getByRole('group', { name: strings.tokenGroupHeading });
    const following = Node.DOCUMENT_POSITION_FOLLOWING;
    expect(status.compareDocumentPosition(passwordGroup) & following).toBeTruthy();
    expect(passwordGroup.compareDocumentPosition(tokenGroup) & following).toBeTruthy();
  });

  it('shows the neutral no-password state with the recovery pointer when no password returns', async () => {
    const user = userEvent.setup();
    render(<ProvisionForm onProvision={vi.fn().mockResolvedValue(baseResult)} />);
    await fillAndSubmit(user);

    expect(await screen.findByText(strings.noPasswordReturned)).toBeInTheDocument();
    expect(screen.getByText(strings.noPasswordRecovery)).toBeInTheDocument();
    expect(document.querySelectorAll('[role="status"]')).toHaveLength(1);
    expect(screen.queryByLabelText(strings.passwordLabel)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: strings.copyPassword })).not.toBeInTheDocument();
    expect(screen.getByText('tk_abc123')).toBeInTheDocument();
    expect(
      screen.getByText(strings.tokenRevealLead({ userId: 'alice', appId: 'urls4irl' })),
    ).toBeInTheDocument();
  });

  it('selects all of the password text when the input is focused', async () => {
    await revealPassword();
    const input = screen.getByLabelText(strings.passwordLabel) as HTMLInputElement;

    act(() => {
      input.focus();
    });

    expect(input.selectionStart).toBe(0);
    expect(input.selectionEnd).toBe(PASSWORD.length);
  });

  it('never moves focus programmatically on reveal', async () => {
    const focusSpy = vi.spyOn(HTMLElement.prototype, 'focus');
    const user = userEvent.setup();
    let resolveProvision: (value: typeof withPassword) => void = () => {};
    const pending = new Promise<typeof withPassword>((resolve) => {
      resolveProvision = resolve;
    });
    render(<ProvisionForm onProvision={vi.fn().mockReturnValue(pending)} />);
    await user.type(screen.getByLabelText(strings.appIdLabel), 'urls4irl');
    await user.type(screen.getByLabelText(strings.emailLabel), 'alice@example.com');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));
    focusSpy.mockClear();

    resolveProvision(withPassword);
    await screen.findByLabelText(strings.passwordLabel);

    expect(focusSpy).not.toHaveBeenCalled();
  });

  it('copies the password, announces Copied in the same region, and renders a fresh node per click', async () => {
    const { user, onProvision } = await revealPassword();
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard({ writeText });
    const copyButton = screen.getByRole('button', { name: strings.copyPassword });
    expect(copyButton).toHaveAttribute('type', 'button');
    expect(copyButton).toHaveClass('provision-form__copy');
    expect(copyButton).not.toHaveAttribute('title');
    expect(copyButton).not.toHaveAttribute('aria-label');

    await user.click(copyButton);
    const status = document.querySelector('[role="status"]') as HTMLElement;
    const first = await within(status).findByText(strings.passwordCopied);
    expect(writeText).toHaveBeenCalledWith(PASSWORD);

    await user.click(copyButton);
    await waitFor(() => {
      expect(within(status).getByText(strings.passwordCopied)).not.toBe(first);
    });
    expect(status).toHaveTextContent(strings.passwordRevealLead);
    expect(onProvision).toHaveBeenCalledTimes(1);
    expect(screen.getByLabelText(strings.passwordLabel)).toHaveValue(PASSWORD);
  });

  it('shows the fallback message when the Clipboard API is missing', async () => {
    const { user } = await revealPassword();
    setClipboard(undefined);

    await user.click(screen.getByRole('button', { name: strings.copyPassword }));

    expect(await screen.findByText(strings.passwordCopyUnavailable)).toBeInTheDocument();
    expect(screen.queryByText(strings.passwordCopied)).not.toBeInTheDocument();
  });

  it('shows the fallback message when the clipboard write rejects', async () => {
    const { user } = await revealPassword();
    setClipboard({ writeText: vi.fn().mockRejectedValue(new Error('denied')) });

    await user.click(screen.getByRole('button', { name: strings.copyPassword }));

    expect(await screen.findByText(strings.passwordCopyUnavailable)).toBeInTheDocument();
  });

  it('keeps Copied until the next submit, then clears it', async () => {
    const { user } = await revealPassword();
    setClipboard({ writeText: vi.fn().mockResolvedValue(undefined) });
    await user.click(screen.getByRole('button', { name: strings.copyPassword }));
    expect(await screen.findByText(strings.passwordCopied)).toBeInTheDocument();

    await user.clear(screen.getByLabelText(strings.emailLabel));
    await user.type(screen.getByLabelText(strings.emailLabel), 'not-an-email');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText(strings.invalidEmail)).toBeInTheDocument();
    expect(screen.queryByText(strings.passwordCopied)).not.toBeInTheDocument();
  });

  it('ignores a clipboard promise that settles after the result was replaced', async () => {
    const user = userEvent.setup();
    const onProvision = vi.fn().mockResolvedValue(withPassword);
    render(<ProvisionForm onProvision={onProvision} />);
    await fillAndSubmit(user);
    await screen.findByLabelText(strings.passwordLabel);

    let resolveWrite: () => void = () => {};
    setClipboard({
      writeText: vi.fn().mockReturnValue(
        new Promise<void>((resolve) => {
          resolveWrite = resolve;
        }),
      ),
    });
    await user.click(screen.getByRole('button', { name: strings.copyPassword }));

    // New submit replaces the result while the write is still pending.
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));
    await screen.findByLabelText(strings.passwordLabel);
    await act(async () => {
      resolveWrite();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(screen.queryByText(strings.passwordCopied)).not.toBeInTheDocument();
  });

  it('clears the previous password while the next request is pending', async () => {
    const user = userEvent.setup();
    const onProvision = vi
      .fn()
      .mockResolvedValueOnce(withPassword)
      .mockReturnValueOnce(new Promise(() => {}));
    render(<ProvisionForm onProvision={onProvision} />);
    await fillAndSubmit(user);
    await screen.findByLabelText(strings.passwordLabel);

    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    await screen.findByRole('button', { name: strings.provisioning });
    expect(screen.queryByLabelText(strings.passwordLabel)).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue(PASSWORD)).not.toBeInTheDocument();
  });

  it('clears the password when a later submit is invalid', async () => {
    const { user } = await revealPassword();

    await user.clear(screen.getByLabelText(strings.emailLabel));
    await user.type(screen.getByLabelText(strings.emailLabel), 'not-an-email');
    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText(strings.invalidEmail)).toBeInTheDocument();
    expect(screen.queryByDisplayValue(PASSWORD)).not.toBeInTheDocument();
  });

  it('clears the password when a later submit rejects', async () => {
    const user = userEvent.setup();
    const onProvision = vi
      .fn()
      .mockResolvedValueOnce(withPassword)
      .mockRejectedValueOnce(new ApiError({ status: 500, message: 'server exploded' }));
    render(<ProvisionForm onProvision={onProvision} />);
    await fillAndSubmit(user);
    await screen.findByLabelText(strings.passwordLabel);

    await user.click(screen.getByRole('button', { name: strings.provisionAction }));

    expect(await screen.findByText('server exploded')).toBeInTheDocument();
    expect(screen.queryByDisplayValue(PASSWORD)).not.toBeInTheDocument();
  });

  it('never sends the password to console or browser storage', async () => {
    const consoleSpies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((method) =>
      vi.spyOn(console, method),
    );
    const setItemSpy = vi.spyOn(Storage.prototype, 'setItem');
    const { user } = await revealPassword();
    setClipboard({ writeText: vi.fn().mockResolvedValue(undefined) });
    await user.click(screen.getByRole('button', { name: strings.copyPassword }));
    await screen.findByText(strings.passwordCopied);

    expect(screen.getByDisplayValue(PASSWORD)).toBeInTheDocument();
    for (const spy of [...consoleSpies, setItemSpy]) {
      expect(JSON.stringify(spy.mock.calls)).not.toContain(PASSWORD);
    }
  });
});
