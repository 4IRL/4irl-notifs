import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { strings } from '../strings';
import { AppTabs } from './AppTabs';

function renderTabs(deliveryContent = <p>delivery body</p>) {
  return render(<AppTabs provisioning={<input aria-label="draft" />} delivery={deliveryContent} />);
}

function provisioningTab() {
  return screen.getByRole('tab', { name: strings.tabProvisioning });
}

function deliveryTab() {
  return screen.getByRole('tab', { name: strings.tabDelivery });
}

beforeEach(() => {
  window.history.replaceState(null, '', '/');
});

afterEach(() => {
  window.history.replaceState(null, '', '/');
  vi.restoreAllMocks();
});

describe('AppTabs', () => {
  it('selects Provisioning by default with roving tabindex', () => {
    renderTabs();

    expect(provisioningTab()).toHaveAttribute('aria-selected', 'true');
    expect(provisioningTab()).toHaveAttribute('tabindex', '0');
    expect(deliveryTab()).toHaveAttribute('aria-selected', 'false');
    expect(deliveryTab()).toHaveAttribute('tabindex', '-1');
  });

  it('opens Delivery when the page loads on #delivery', () => {
    window.history.replaceState(null, '', '/#delivery');
    renderTabs();

    expect(deliveryTab()).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('delivery body')).toBeInTheDocument();
  });

  it('treats any other hash as Provisioning', () => {
    window.history.replaceState(null, '', '/#something-else');
    renderTabs();

    expect(provisioningTab()).toHaveAttribute('aria-selected', 'true');
  });

  it('places both tabpanels inside main, linked to their tabs', () => {
    renderTabs();

    const main = screen.getByRole('main');
    const provisioningPanel = document.getElementById('app-panel-provisioning');
    const deliveryPanel = document.getElementById('app-panel-delivery');
    expect(provisioningPanel).not.toBeNull();
    expect(deliveryPanel).not.toBeNull();
    expect(provisioningPanel?.parentElement).toBe(main);
    expect(deliveryPanel?.parentElement).toBe(main);
    expect(provisioningTab()).toHaveAttribute('aria-controls', 'app-panel-provisioning');
    expect(deliveryTab()).toHaveAttribute('aria-controls', 'app-panel-delivery');
    expect(provisioningPanel).toHaveAttribute('aria-labelledby', provisioningTab().id);
    expect(deliveryPanel).toHaveAttribute('aria-labelledby', deliveryTab().id);
  });

  it('hides the inactive panel without unmounting it', async () => {
    const user = userEvent.setup();
    renderTabs();

    expect(document.getElementById('app-panel-delivery')).toHaveAttribute('hidden');
    expect(document.getElementById('app-panel-provisioning')).not.toHaveAttribute('hidden');

    await user.click(deliveryTab());

    expect(document.getElementById('app-panel-provisioning')).toHaveAttribute('hidden');
    expect(document.getElementById('app-panel-provisioning')).toContainElement(
      screen.getByLabelText('draft', { selector: 'input' }),
    );
    expect(document.getElementById('app-panel-delivery')).not.toHaveAttribute('hidden');
  });

  it('mounts the Delivery content only on its first activation, then keeps it', async () => {
    const user = userEvent.setup();
    renderTabs();

    expect(screen.queryByText('delivery body')).not.toBeInTheDocument();
    await user.click(deliveryTab());
    expect(screen.getByText('delivery body')).toBeInTheDocument();
    await user.click(provisioningTab());
    expect(screen.getByText('delivery body')).toBeInTheDocument();
  });

  it('keeps typed Provisioning input across a round trip', async () => {
    const user = userEvent.setup();
    renderTabs();

    await user.type(screen.getByLabelText('draft'), 'half typed');
    await user.click(deliveryTab());
    await user.click(provisioningTab());

    expect(screen.getByLabelText('draft')).toHaveValue('half typed');
  });

  it('replaces the history entry on a tab click instead of pushing one', async () => {
    const user = userEvent.setup();
    const replaceState = vi.spyOn(window.history, 'replaceState');
    const pushState = vi.spyOn(window.history, 'pushState');
    const lengthBefore = window.history.length;
    renderTabs();

    await user.click(deliveryTab());

    expect(replaceState).toHaveBeenCalled();
    expect(pushState).not.toHaveBeenCalled();
    expect(window.history.length).toBe(lengthBefore);
    expect(window.location.hash).toBe('#delivery');
  });

  it('follows an external hashchange', () => {
    renderTabs();

    act(() => {
      window.history.replaceState(null, '', '/#delivery');
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });

    expect(deliveryTab()).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('delivery body')).toBeInTheDocument();

    act(() => {
      window.history.replaceState(null, '', '/');
      window.dispatchEvent(new HashChangeEvent('hashchange'));
    });

    expect(provisioningTab()).toHaveAttribute('aria-selected', 'true');
  });

  it('moves and activates with ArrowRight / ArrowLeft, wrapping around', async () => {
    const user = userEvent.setup();
    renderTabs();

    provisioningTab().focus();
    await user.keyboard('{ArrowRight}');
    expect(deliveryTab()).toHaveAttribute('aria-selected', 'true');
    expect(deliveryTab()).toHaveFocus();

    await user.keyboard('{ArrowRight}');
    expect(provisioningTab()).toHaveAttribute('aria-selected', 'true');
    expect(provisioningTab()).toHaveFocus();

    await user.keyboard('{ArrowLeft}');
    expect(deliveryTab()).toHaveAttribute('aria-selected', 'true');
    expect(deliveryTab()).toHaveFocus();
  });

  it('jumps with End and Home', async () => {
    const user = userEvent.setup();
    renderTabs();

    provisioningTab().focus();
    await user.keyboard('{End}');
    expect(deliveryTab()).toHaveAttribute('aria-selected', 'true');
    expect(deliveryTab()).toHaveFocus();

    await user.keyboard('{Home}');
    expect(provisioningTab()).toHaveAttribute('aria-selected', 'true');
    expect(provisioningTab()).toHaveFocus();
  });
});
