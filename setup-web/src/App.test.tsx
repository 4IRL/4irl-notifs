import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import App from './App';
import { strings } from './strings';

const IPHONE_UA =
  'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 Mobile/15E148';
const ANDROID_UA =
  'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Mobile Safari/537.36';
const DESKTOP_UA = 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 Chrome/130.0 Safari/537.36';
const IPADOS_UA = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15';

const VALID_SEARCH = '?app=chores4irl&user=u_abcdefghijklmnop&name=Chore%20Reaper';

function renderApp({
  search = VALID_SEARCH,
  userAgent = DESKTOP_UA,
  maxTouchPoints = 0,
}: { search?: string; userAgent?: string; maxTouchPoints?: number } = {}) {
  return render(<App search={search} userAgent={userAgent} maxTouchPoints={maxTouchPoints} />);
}

afterEach(() => {
  vi.restoreAllMocks();
  window.history.replaceState(null, '', '/');
});

describe('App with invalid params', () => {
  const invalidSearches: Record<string, string> = {
    'no params': '',
    'missing app': '?user=u_abcdefghijklmnop',
    'missing user': '?app=secretapp',
    'valid app with invalid user': '?app=secretapp&user=u_NOT-VALID',
    'invalid app with valid user': '?app=Bad%20App&user=u_abcdefghijklmnop',
    'reserved app everyone': '?app=everyone&user=u_abcdefghijklmnop',
  };

  it.each(Object.entries(invalidSearches))(
    'renders only the header and the alert for %s',
    (_label, search) => {
      // A desktop UA is the case that would render the QR on a valid link, so
      // its absence here proves the error branch never reaches it.
      renderApp({ search, userAgent: DESKTOP_UA });

      expect(screen.getByRole('heading', { level: 1, name: strings.pageHeading })).toBeVisible();
      const alert = screen.getByRole('alert');
      expect(within(alert).getByRole('heading', { name: strings.errorHeading })).toBeVisible();
      expect(alert).toHaveTextContent(strings.errorBody);
      expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
      expect(screen.queryByRole('tab')).not.toBeInTheDocument();
      expect(screen.queryAllByRole('tabpanel', { hidden: true })).toHaveLength(0);
      expect(screen.queryByRole('img')).not.toBeInTheDocument();
      expect(screen.queryByRole('button')).not.toBeInTheDocument();
      expect(screen.queryByRole('link')).not.toBeInTheDocument();
    },
  );

  it('echoes no param value into the DOM, including a valid app with an invalid user', () => {
    const { container } = renderApp({ search: '?app=secretapp&user=u_NOT-VALID&name=SneakyName' });

    expect(container.textContent).not.toContain('secretapp');
    expect(container.textContent).not.toContain('u_NOT-VALID');
    expect(container.textContent).not.toContain('SneakyName');
    expect(container.innerHTML).not.toContain('secretapp');
  });

  it('shows no badge next to the heading', () => {
    renderApp({ search: '', userAgent: IPHONE_UA });

    expect(document.querySelector('.setup-page__badge')).toBeNull();
  });
});

describe('App header', () => {
  it('shows the name in the badge and the lead text', () => {
    renderApp({ userAgent: IPHONE_UA });

    expect(document.querySelector('.setup-page__badge')).toHaveTextContent('Chore Reaper');
    expect(document.querySelector('.setup-page__badge')).not.toHaveClass('setup-page__badge--mono');
    expect(document.querySelector('.setup-page__lead')).toHaveTextContent(
      'Get Chore Reaper notifications on your phone with the free ntfy app.',
    );
  });

  it.each([
    ['missing', '?app=tasktracker&user=u_abcdefghijklmnop'],
    ['blank', '?app=tasktracker&user=u_abcdefghijklmnop&name=%20%20'],
    ['over 64 chars', `?app=tasktracker&user=u_abcdefghijklmnop&name=${'x'.repeat(65)}`],
  ])('falls back to the app id in mono when name is %s', (_label, search) => {
    renderApp({ search });

    const badge = document.querySelector('.setup-page__badge');
    expect(badge).toHaveTextContent('tasktracker');
    expect(badge).toHaveClass('setup-page__badge--mono');
    expect(document.querySelector('.setup-page__lead')).toHaveTextContent(
      'Get tasktracker notifications on your phone',
    );
  });

  it('renders the footer line', () => {
    renderApp();

    expect(screen.getByText(strings.footer)).toBeInTheDocument();
  });
});

describe('App on iPhone', () => {
  it('starts with the iPhone tab selected, the detected hint and no QR', () => {
    renderApp({ userAgent: IPHONE_UA });

    expect(screen.getByRole('tab', { name: strings.tabIphone })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(screen.getByRole('tab', { name: strings.tabAndroid })).toHaveAttribute(
      'aria-selected',
      'false',
    );
    expect(screen.getByText(strings.hintDetected('iPhone'))).toBeInTheDocument();
    expect(screen.queryByRole('img', { name: strings.qrAriaLabel })).not.toBeInTheDocument();
  });

  it('treats an iPadOS Macintosh UA with touch points as iPhone', () => {
    renderApp({ userAgent: IPADOS_UA, maxTouchPoints: 5 });

    expect(screen.getByRole('tab', { name: strings.tabIphone })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('switches to Android on click and announces the change through the status hint', async () => {
    const user = userEvent.setup();
    renderApp({ userAgent: IPHONE_UA });
    const hint = screen.getByText(strings.hintDetected('iPhone'));
    expect(hint).toHaveAttribute('role', 'status');
    expect(hint).toHaveAttribute('aria-live', 'polite');

    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));

    expect(screen.getByRole('tab', { name: strings.tabAndroid })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    // The same live-region element updates in place, which is what gets announced.
    expect(hint).toHaveTextContent(strings.hintShowing('Android'));
    expect(screen.queryByText(strings.hintDetected('iPhone'))).not.toBeInTheDocument();
  });

  it('reads the detected hint again after switching back to the detected platform', async () => {
    const user = userEvent.setup();
    renderApp({ userAgent: IPHONE_UA });

    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));
    await user.click(screen.getByRole('tab', { name: strings.tabIphone }));

    expect(screen.getByText(strings.hintDetected('iPhone'))).toBeInTheDocument();
  });

  it('switches by Arrow/Home/End keys with the status hint following', async () => {
    const user = userEvent.setup();
    renderApp({ userAgent: IPHONE_UA });
    const iphone = screen.getByRole('tab', { name: strings.tabIphone });
    const android = screen.getByRole('tab', { name: strings.tabAndroid });

    iphone.focus();
    await user.keyboard('{ArrowRight}');
    expect(android).toHaveAttribute('aria-selected', 'true');
    expect(android).toHaveFocus();
    expect(screen.getByText(strings.hintShowing('Android'))).toHaveAttribute('role', 'status');

    await user.keyboard('{Home}');
    expect(iphone).toHaveAttribute('aria-selected', 'true');
    expect(iphone).toHaveFocus();

    await user.keyboard('{End}');
    expect(android).toHaveAttribute('aria-selected', 'true');
    expect(android).toHaveFocus();

    await user.keyboard('{ArrowLeft}');
    expect(iphone).toHaveAttribute('aria-selected', 'true');
    expect(iphone).toHaveFocus();
  });
});

describe('App on Android', () => {
  it('starts with the Android tab selected and the detected hint', () => {
    renderApp({ userAgent: ANDROID_UA });

    expect(screen.getByRole('tab', { name: strings.tabAndroid })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(screen.getByText(strings.hintDetected('Android'))).toBeInTheDocument();
    expect(screen.queryByRole('img', { name: strings.qrAriaLabel })).not.toBeInTheDocument();
  });
});

describe('App on desktop', () => {
  it('renders the QR with its accessible name and the pick-a-phone prompt', () => {
    renderApp({ userAgent: DESKTOP_UA });

    const qr = screen.getByRole('img', { name: strings.qrAriaLabel });
    expect(qr.querySelector('svg')).not.toBeNull();
    expect(screen.getByText(strings.qrCaption)).toBeInTheDocument();
    expect(screen.getByText(strings.hintPickPhone)).toBeInTheDocument();
  });

  it('leaves both tabs unselected with the first tab as the only tab stop', () => {
    renderApp({ userAgent: DESKTOP_UA });

    const iphone = screen.getByRole('tab', { name: strings.tabIphone });
    const android = screen.getByRole('tab', { name: strings.tabAndroid });
    expect(iphone).toHaveAttribute('aria-selected', 'false');
    expect(android).toHaveAttribute('aria-selected', 'false');
    expect(iphone).toHaveAttribute('tabindex', '0');
    expect(android).toHaveAttribute('tabindex', '-1');
  });

  it('mounts both tabpanels hidden and empty until a tab is selected', () => {
    renderApp({ userAgent: DESKTOP_UA });

    const panels = screen.getAllByRole('tabpanel', { hidden: true });
    expect(panels).toHaveLength(2);
    for (const panel of panels) {
      expect(panel).not.toBeVisible();
      expect(panel).toBeEmptyDOMElement();
    }
  });

  it('keeps the QR on screen after a phone is picked and shows the Showing hint', async () => {
    const user = userEvent.setup();
    renderApp({ userAgent: DESKTOP_UA });

    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));

    expect(screen.getByRole('img', { name: strings.qrAriaLabel })).toBeInTheDocument();
    expect(screen.getByText(strings.hintShowing('Android'))).toBeInTheDocument();
    expect(screen.getByRole('tabpanel', { name: strings.tabAndroid })).toBeVisible();
  });

  it('activates a tab immediately on Arrow/Home/End, moving focus and unhiding its panel', async () => {
    const user = userEvent.setup();
    renderApp({ userAgent: DESKTOP_UA });
    const iphone = screen.getByRole('tab', { name: strings.tabIphone });
    const android = screen.getByRole('tab', { name: strings.tabAndroid });

    iphone.focus();
    await user.keyboard('{ArrowRight}');
    expect(android).toHaveFocus();
    expect(android).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tabpanel', { name: strings.tabAndroid })).toBeVisible();
    expect(screen.queryByRole('tabpanel', { name: strings.tabIphone })).not.toBeInTheDocument();

    await user.keyboard('{Home}');
    expect(iphone).toHaveFocus();
    expect(iphone).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tabpanel', { name: strings.tabIphone })).toBeVisible();

    await user.keyboard('{End}');
    expect(android).toHaveFocus();
    expect(android).toHaveAttribute('aria-selected', 'true');
  });

  it('wraps ArrowLeft from the first tab to the last', async () => {
    const user = userEvent.setup();
    renderApp({ userAgent: DESKTOP_UA });

    screen.getByRole('tab', { name: strings.tabIphone }).focus();
    await user.keyboard('{ArrowLeft}');

    expect(screen.getByRole('tab', { name: strings.tabAndroid })).toHaveAttribute(
      'aria-selected',
      'true',
    );
  });

  it('ignores keys that are not tab navigation keys and leaves their default alone', () => {
    renderApp({ userAgent: DESKTOP_UA });
    const iphone = screen.getByRole('tab', { name: strings.tabIphone });

    // fireEvent returns false when a handler called preventDefault.
    const notPrevented = fireEvent.keyDown(iphone, { key: 'a' });

    expect(notPrevented).toBe(true);
    expect(screen.getAllByRole('tab').every((tab) => tab.ariaSelected === 'false')).toBe(true);
  });

  it.each(['altKey', 'ctrlKey', 'metaKey', 'shiftKey'])(
    'leaves Arrow keys with %s held to the browser (no activation, no preventDefault)',
    (modifier) => {
      renderApp({ userAgent: DESKTOP_UA });
      const iphone = screen.getByRole('tab', { name: strings.tabIphone });

      const notPrevented = fireEvent.keyDown(iphone, { key: 'ArrowRight', [modifier]: true });

      expect(notPrevented).toBe(true);
      expect(screen.getAllByRole('tab').every((tab) => tab.ariaSelected === 'false')).toBe(true);
    },
  );
});

describe('per-platform steps and the one-tap rule', () => {
  const NTFY_ANCHOR = 'a[href^="ntfy://"]';
  const PERSONAL_HREF = 'ntfy://notifs.4irl.app/chores4irl-abcdefghijklmnop-all';
  const BROADCAST_HREF = 'ntfy://notifs.4irl.app/chores4irl-broadcast';

  it('renders the iPhone steps with derived values on an iPhone UA, and no ntfy:// anchors', () => {
    const { container } = renderApp({ userAgent: IPHONE_UA });

    const panel = screen.getByRole('tabpanel', { name: strings.tabIphone });
    expect(within(panel).getAllByRole('button')).toHaveLength(4);
    expect(within(panel).getByText('chores4irl-abcdefghijklmnop-all')).toBeInTheDocument();
    expect(within(panel).getByText('chores4irl-broadcast')).toBeInTheDocument();
    expect(within(panel).getByText('u_abcdefghijklmnop')).toBeInTheDocument();
    expect(container.querySelector(NTFY_ANCHOR)).toBeNull();
  });

  it('renders one-tap anchors only for an Android UA with Android selected', () => {
    const { container } = renderApp({ userAgent: ANDROID_UA });

    const panel = screen.getByRole('tabpanel', { name: strings.tabAndroid });
    expect(
      within(panel).getByRole('link', { name: strings.androidOneTapPersonal }),
    ).toHaveAttribute('href', PERSONAL_HREF);
    expect(
      within(panel).getByRole('link', { name: strings.androidOneTapBroadcast }),
    ).toHaveAttribute('href', BROADCAST_HREF);
    expect(within(panel).getAllByRole('button')).toHaveLength(4);
    expect(container.querySelectorAll(NTFY_ANCHOR)).toHaveLength(2);
  });

  it('drops the one-tap anchors but keeps four copy buttons when an iPhone UA switches to Android', async () => {
    const user = userEvent.setup();
    const { container } = renderApp({ userAgent: IPHONE_UA });

    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));

    const panel = screen.getByRole('tabpanel', { name: strings.tabAndroid });
    expect(container.querySelector(NTFY_ANCHOR)).toBeNull();
    expect(within(panel).getByText(strings.androidCopyOnPhoneNote)).toBeInTheDocument();
    expect(within(panel).getAllByRole('button')).toHaveLength(4);
  });

  it('shows copy-only Android steps when a desktop visitor picks Android', async () => {
    const user = userEvent.setup();
    const { container } = renderApp({ userAgent: DESKTOP_UA });

    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));

    const panel = screen.getByRole('tabpanel', { name: strings.tabAndroid });
    expect(container.querySelector(NTFY_ANCHOR)).toBeNull();
    expect(within(panel).getAllByRole('button')).toHaveLength(4);
    expect(within(panel).getByText(strings.androidCopyOnPhoneNote)).toBeInTheDocument();
  });

  it('removes the one-tap anchors while away from Android and restores them on return', async () => {
    const user = userEvent.setup();
    const { container } = renderApp({ userAgent: ANDROID_UA });
    expect(container.querySelectorAll(NTFY_ANCHOR)).toHaveLength(2);

    await user.click(screen.getByRole('tab', { name: strings.tabIphone }));
    // The rule needs selected === 'android', so the hidden Android panel has none.
    expect(container.querySelector(NTFY_ANCHOR)).toBeNull();
    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));

    expect(container.querySelectorAll(NTFY_ANCHOR)).toHaveLength(2);
  });

  it('uses the fallback app id in the password note and test step when name is missing', () => {
    renderApp({ search: '?app=tasktracker&user=u_abcdefghijklmnop', userAgent: IPHONE_UA });

    const panel = screen.getByRole('tabpanel', { name: strings.tabIphone });
    expect(within(panel).getByText(strings.passwordNote('tasktracker'))).toBeInTheDocument();
    expect(panel).toHaveTextContent('Back in tasktracker, tap Send test notification.');
  });

  it('mounts three screenshots across both panels, each with distinct alt text and explicit size', () => {
    const { container } = renderApp({ userAgent: IPHONE_UA });

    // The QR is a role="img" div, not an <img>, so this selects only screenshots.
    const shots = [...container.querySelectorAll('img')];
    expect(shots).toHaveLength(3);
    const alts = shots.map((image) => image.getAttribute('alt') ?? '');
    expect(alts.every((alt) => alt.trim() !== '')).toBe(true);
    expect(new Set(alts).size).toBe(3);
    for (const image of shots) {
      expect(image).toHaveAttribute('width');
      expect(image).toHaveAttribute('height');
    }
  });

  it.each([
    ['iPhone', IPHONE_UA],
    ['Android', ANDROID_UA],
    ['desktop', DESKTOP_UA],
  ])('never renders an input, textarea or form (%s)', async (_label, userAgent) => {
    const user = userEvent.setup();
    const { container } = renderApp({ userAgent });

    await user.click(screen.getByRole('tab', { name: strings.tabIphone }));
    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));

    expect(container.querySelector('input, textarea, form')).toBeNull();
  });
});

describe('platform switching and the URL', () => {
  it('never touches location.hash, replaceState or pushState when switching', async () => {
    const user = userEvent.setup();
    const replaceState = vi.spyOn(window.history, 'replaceState');
    const pushState = vi.spyOn(window.history, 'pushState');
    renderApp({ userAgent: IPHONE_UA });

    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));
    await user.click(screen.getByRole('tab', { name: strings.tabIphone }));
    screen.getByRole('tab', { name: strings.tabIphone }).focus();
    await user.keyboard('{ArrowRight}{Home}{End}');

    expect(window.location.hash).toBe('');
    expect(replaceState).not.toHaveBeenCalled();
    expect(pushState).not.toHaveBeenCalled();
  });

  it('ignores an existing hash fragment: selected is re-derived from detected', () => {
    window.history.replaceState(null, '', '/#android');
    renderApp({ userAgent: IPHONE_UA });

    expect(screen.getByRole('tab', { name: strings.tabIphone })).toHaveAttribute(
      'aria-selected',
      'true',
    );
    expect(screen.getByRole('tab', { name: strings.tabAndroid })).toHaveAttribute(
      'aria-selected',
      'false',
    );
  });

  it('reads the real window and navigator when no props are passed', () => {
    window.history.replaceState(null, '', `/${VALID_SEARCH}`);
    render(<App />);

    expect(document.querySelector('.setup-page__badge')).toHaveTextContent('Chore Reaper');
    // jsdom reports a desktop-class UA, so the QR handoff appears.
    expect(screen.getByRole('img', { name: strings.qrAriaLabel })).toBeInTheDocument();
  });
});

// True when `later` comes after `earlier` in document order.
function follows({ earlier, later }: { earlier: Node; later: Node }) {
  return Boolean(earlier.compareDocumentPosition(later) & Node.DOCUMENT_POSITION_FOLLOWING);
}

function storeLinks() {
  return screen.getAllByRole('link', { name: new RegExp(`^${strings.getNtfyOn}`) });
}

describe('App install card', () => {
  const platforms = [
    { name: 'iPhone', userAgent: IPHONE_UA },
    { name: 'Android', userAgent: ANDROID_UA },
    { name: 'desktop', userAgent: DESKTOP_UA },
  ];

  it.each(platforms)('shows all three store links on $name', ({ userAgent }) => {
    renderApp({ userAgent });

    expect(storeLinks()).toHaveLength(3);
    expect(
      screen.getByRole('heading', { level: 2, name: strings.installCardHeading }),
    ).toBeVisible();
  });

  it.each(platforms)('puts the card above the platform tabs on $name', ({ userAgent }) => {
    renderApp({ userAgent });

    const heading = screen.getByRole('heading', { name: strings.installCardHeading });
    expect(follows({ earlier: heading, later: screen.getByRole('tablist') })).toBe(true);
  });

  it('puts the card directly below the QR and above the steps prompt on desktop', () => {
    renderApp({ userAgent: DESKTOP_UA });

    const qr = screen.getByRole('img', { name: strings.qrAriaLabel });
    const heading = screen.getByRole('heading', { name: strings.installCardHeading });
    const stepsHeading = screen.getByRole('heading', { name: strings.stepsHere });
    expect(follows({ earlier: qr, later: heading })).toBe(true);
    expect(follows({ earlier: heading, later: stepsHeading })).toBe(true);
  });

  it('puts the card between the lead sentence and the tabs on a phone, with no QR', () => {
    renderApp({ userAgent: IPHONE_UA });

    const lead = screen.getByText(strings.leadNtfy, { selector: 'strong' });
    const heading = screen.getByRole('heading', { name: strings.installCardHeading });
    expect(follows({ earlier: lead, later: heading })).toBe(true);
    expect(screen.queryByRole('img', { name: strings.qrAriaLabel })).not.toBeInTheDocument();
  });

  it('keeps the same three links when the selected tab changes', async () => {
    const user = userEvent.setup();
    renderApp({ userAgent: IPHONE_UA });
    const before = storeLinks().map((link) => link.getAttribute('href'));

    await user.click(screen.getByRole('tab', { name: strings.tabAndroid }));

    expect(storeLinks().map((link) => link.getAttribute('href'))).toEqual(before);
  });
});
