import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { strings } from '../strings';
import { CopyField } from './CopyField';

function stubClipboard(clipboard: { writeText: (text: string) => Promise<void> } | undefined) {
  Object.defineProperty(navigator, 'clipboard', {
    value: clipboard,
    configurable: true,
    writable: true,
  });
}

function setupUser() {
  // user-event installs its own clipboard stub on setup, so stub ours afterwards.
  return userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
}

const originalClipboard = Object.getOwnPropertyDescriptor(navigator, 'clipboard');

beforeEach(() => {
  vi.useFakeTimers({ shouldAdvanceTime: true });
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  // vi.restoreAllMocks does not undo defineProperty, so put navigator.clipboard back.
  if (originalClipboard) {
    Object.defineProperty(navigator, 'clipboard', originalClipboard);
  } else {
    Reflect.deleteProperty(navigator, 'clipboard');
  }
});

describe('CopyField', () => {
  it('renders the label, the value, and a Copy button named after the label', () => {
    render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    expect(screen.getByText('Username')).toBeInTheDocument();
    expect(screen.getByText('u_abcdefghijklmnop')).toBeInTheDocument();
    const button = screen.getByRole('button', { name: 'Copy Username' });
    expect(button).toHaveTextContent(strings.copyButton);
  });

  it('writes the exact value, shows "Copied!", and reverts to "Copy" after 2000 ms', async () => {
    const user = setupUser();
    const writeText = vi.fn().mockResolvedValue(undefined);
    stubClipboard({ writeText });
    render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    await user.click(screen.getByRole('button', { name: 'Copy Username' }));

    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writeText).toHaveBeenCalledWith('u_abcdefghijklmnop');
    expect(await screen.findByRole('button', { name: strings.copiedButton })).toBeInTheDocument();
    expect(screen.getByRole('status')).toHaveTextContent(strings.copiedAnnouncement('Username'));

    act(() => {
      vi.advanceTimersByTime(1500);
    });
    expect(screen.getByRole('button', { name: strings.copiedButton })).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(600);
    });
    expect(screen.getByRole('button', { name: 'Copy Username' })).toHaveTextContent(
      strings.copyButton,
    );
    expect(screen.queryByRole('button', { name: strings.copiedButton })).not.toBeInTheDocument();
    expect(screen.getByRole('status')).toBeEmptyDOMElement();
  });

  it('shows the alert and never "Copied!" when the clipboard write rejects', async () => {
    const user = setupUser();
    stubClipboard({ writeText: vi.fn().mockRejectedValue(new Error('denied')) });
    render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    await user.click(screen.getByRole('button', { name: 'Copy Username' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(strings.copyError);
    expect(screen.queryByRole('button', { name: strings.copiedButton })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy Username' })).toBeInTheDocument();
  });

  it('shows the same alert, without throwing, when navigator.clipboard is undefined', async () => {
    const user = setupUser();
    stubClipboard(undefined);
    render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    await user.click(screen.getByRole('button', { name: 'Copy Username' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(strings.copyError);
    expect(screen.queryByRole('button', { name: strings.copiedButton })).not.toBeInTheDocument();
  });

  it('keeps each instance independent: copying one leaves the other unchanged', async () => {
    const user = setupUser();
    stubClipboard({ writeText: vi.fn().mockResolvedValue(undefined) });
    render(
      <>
        <CopyField label="Service URL" value="https://notifs.4irl.app" />
        <CopyField label="Username" value="u_abcdefghijklmnop" />
      </>,
    );

    await user.click(screen.getByRole('button', { name: 'Copy Service URL' }));

    expect(await screen.findByRole('button', { name: strings.copiedButton })).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: strings.copiedButton })).toHaveLength(1);
    expect(screen.getByRole('button', { name: 'Copy Username' })).toHaveTextContent(
      strings.copyButton,
    );
  });

  it('keeps each instance error state independent', async () => {
    const user = setupUser();
    const writeText = vi
      .fn()
      .mockRejectedValueOnce(new Error('denied'))
      .mockResolvedValue(undefined);
    stubClipboard({ writeText });
    render(
      <>
        <CopyField label="Service URL" value="https://notifs.4irl.app" />
        <CopyField label="Username" value="u_abcdefghijklmnop" />
      </>,
    );

    await user.click(screen.getByRole('button', { name: 'Copy Service URL' }));
    expect(await screen.findByRole('alert')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Copy Username' }));
    expect(await screen.findByRole('button', { name: strings.copiedButton })).toBeInTheDocument();
    // The first field's alert is untouched by the second field's success.
    expect(screen.getAllByRole('alert')).toHaveLength(1);
  });

  it('restarts the 2000 ms window on a second click within the window', async () => {
    const user = setupUser();
    stubClipboard({ writeText: vi.fn().mockResolvedValue(undefined) });
    render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    await user.click(screen.getByRole('button', { name: 'Copy Username' }));
    expect(await screen.findByRole('button', { name: strings.copiedButton })).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(1500);
    });
    await user.click(screen.getByRole('button', { name: strings.copiedButton }));

    // 1500 ms past the second click (3000 ms past the first): still copied.
    act(() => {
      vi.advanceTimersByTime(1500);
    });
    expect(screen.getByRole('button', { name: strings.copiedButton })).toBeInTheDocument();

    act(() => {
      vi.advanceTimersByTime(600);
    });
    expect(screen.getByRole('button', { name: 'Copy Username' })).toBeInTheDocument();
  });

  it('removes the alert after a later successful copy', async () => {
    const user = setupUser();
    const writeText = vi
      .fn()
      .mockRejectedValueOnce(new Error('denied'))
      .mockResolvedValue(undefined);
    stubClipboard({ writeText });
    render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    await user.click(screen.getByRole('button', { name: 'Copy Username' }));
    expect(await screen.findByRole('alert')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Copy Username' }));

    expect(await screen.findByRole('button', { name: strings.copiedButton })).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('clears the pending timer on unmount so nothing fires afterwards', async () => {
    const user = setupUser();
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    stubClipboard({ writeText: vi.fn().mockResolvedValue(undefined) });
    const { unmount } = render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    await user.click(screen.getByRole('button', { name: 'Copy Username' }));
    expect(await screen.findByRole('button', { name: strings.copiedButton })).toBeInTheDocument();
    expect(vi.getTimerCount()).toBeGreaterThan(0);

    unmount();
    expect(vi.getTimerCount()).toBe(0);

    act(() => {
      vi.advanceTimersByTime(3000);
    });
    expect(consoleError).not.toHaveBeenCalled();
  });

  it('does not start a timer when unmounted while the clipboard write is pending', async () => {
    const user = setupUser();
    let resolveWrite: () => void = () => {};
    const pendingWrite = new Promise<void>((resolve) => {
      resolveWrite = resolve;
    });
    stubClipboard({ writeText: vi.fn().mockReturnValue(pendingWrite) });
    const { unmount } = render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    await user.click(screen.getByRole('button', { name: 'Copy Username' }));
    unmount();
    await act(async () => {
      resolveWrite();
      await pendingWrite;
    });

    expect(vi.getTimerCount()).toBe(0);
  });

  it('does not report an error when the clipboard write rejects after unmount', async () => {
    const user = setupUser();
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    let rejectWrite: (reason: Error) => void = () => {};
    const pendingWrite = new Promise<void>((_resolve, reject) => {
      rejectWrite = reject;
    });
    stubClipboard({ writeText: vi.fn().mockReturnValue(pendingWrite) });
    const { unmount } = render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    await user.click(screen.getByRole('button', { name: 'Copy Username' }));
    unmount();
    await act(async () => {
      rejectWrite(new Error('denied'));
      await pendingWrite.catch(() => {});
    });

    expect(consoleError).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('lets the latest of two overlapping clicks own the full 2000 ms window', async () => {
    const user = setupUser();
    const resolvers: Array<() => void> = [];
    const writeText = vi.fn().mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolvers.push(resolve);
        }),
    );
    stubClipboard({ writeText });
    render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    const button = screen.getByRole('button', { name: 'Copy Username' });
    await user.click(button);
    await user.click(button);
    expect(resolvers).toHaveLength(2);

    // The first write settles late; the second (latest) one settles after 1000 ms.
    await act(async () => {
      resolvers[1]();
      await Promise.resolve();
    });
    expect(screen.getByRole('button', { name: strings.copiedButton })).toBeInTheDocument();
    act(() => {
      vi.advanceTimersByTime(1000);
    });
    await act(async () => {
      resolvers[0]();
      await Promise.resolve();
    });

    // 1000 ms into the latest window: the stale first resolve must not restart or shorten it.
    expect(screen.getByRole('button', { name: strings.copiedButton })).toBeInTheDocument();
    expect(vi.getTimerCount()).toBe(1);
    act(() => {
      vi.advanceTimersByTime(1500);
    });
    expect(screen.getByRole('button', { name: 'Copy Username' })).toBeInTheDocument();
  });

  it('ignores a stale rejection from an earlier click once a later click succeeded', async () => {
    const user = setupUser();
    const rejecters: Array<(reason: Error) => void> = [];
    const resolvers: Array<() => void> = [];
    const writeText = vi.fn().mockImplementation(
      () =>
        new Promise<void>((resolve, reject) => {
          resolvers.push(resolve);
          rejecters.push(reject);
        }),
    );
    stubClipboard({ writeText });
    render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    const button = screen.getByRole('button', { name: 'Copy Username' });
    await user.click(button);
    await user.click(button);

    await act(async () => {
      resolvers[1]();
      await Promise.resolve();
    });
    await act(async () => {
      rejecters[0](new Error('denied'));
      await Promise.resolve();
    });

    expect(screen.getByRole('button', { name: strings.copiedButton })).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('shows the alert when writeText throws synchronously', async () => {
    const user = setupUser();
    stubClipboard({
      writeText: vi.fn().mockImplementation(() => {
        throw new Error('sync failure');
      }),
    });
    render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    await user.click(screen.getByRole('button', { name: 'Copy Username' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(strings.copyError);
    expect(screen.queryByRole('button', { name: strings.copiedButton })).not.toBeInTheDocument();
  });

  it('renders markup in a value as literal text', () => {
    render(<CopyField label="Topic" value="<img src=x onerror=alert(1)>" />);

    expect(screen.getByText('<img src=x onerror=alert(1)>')).toBeInTheDocument();
    expect(document.querySelector('img')).toBeNull();
  });

  it('keeps the value and its Copy button in the same field group', () => {
    render(<CopyField label="Username" value="u_abcdefghijklmnop" />);

    const field = screen.getByText('u_abcdefghijklmnop').closest('div');
    expect(field).not.toBeNull();
    expect(
      within(field as HTMLElement).getByRole('button', { name: 'Copy Username' }),
    ).toBeInTheDocument();
  });
});
