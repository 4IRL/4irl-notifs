import { fireEvent, render, screen } from '@testing-library/react';
import { createPortal } from 'react-dom';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { revealFocusedControl } from './adminTableFocus';

afterEach(() => {
  vi.restoreAllMocks();
});

function renderWrapper(): HTMLElement {
  const { container } = render(
    <div className="admin-table__scroll" tabIndex={0} onFocus={revealFocusedControl}>
      <table>
        <tbody>
          <tr>
            <td>
              <button type="button">Edit</button>
            </td>
          </tr>
          <tr>
            <td className="destinations__reveal-cell">
              <input aria-label="Name" />
            </td>
          </tr>
        </tbody>
      </table>
    </div>,
  );
  const wrapper = container.querySelector<HTMLElement>('.admin-table__scroll');
  if (wrapper === null) {
    throw new Error('wrapper did not render');
  }
  return wrapper;
}

/** jsdom has no focus-visible heuristics: say whether the next focus counts as one. */
function setFocusVisible(focusVisible: boolean): void {
  const matches = Element.prototype.matches;
  vi.spyOn(Element.prototype, 'matches').mockImplementation(function (
    this: Element,
    selector: string,
  ) {
    return selector === ':focus-visible' ? focusVisible : matches.call(this, selector);
  });
}

describe('revealFocusedControl', () => {
  it('scrolls a keyboard-focused control the least distance into view', () => {
    const scrollIntoView = vi.spyOn(window.HTMLElement.prototype, 'scrollIntoView');
    setFocusVisible(true);
    renderWrapper();

    fireEvent.focus(screen.getByRole('button', { name: 'Edit' }));

    expect(scrollIntoView).toHaveBeenCalledOnce();
    expect(scrollIntoView).toHaveBeenCalledWith({ block: 'nearest', inline: 'nearest' });
  });

  it('does not move a control that a mouse press focused out from under the pointer', () => {
    const scrollIntoView = vi.spyOn(window.HTMLElement.prototype, 'scrollIntoView');
    setFocusVisible(false);
    renderWrapper();

    fireEvent.focus(screen.getByRole('button', { name: 'Edit' }));

    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it('ignores focus React bubbles up from a portal (the row menu lives outside the wrapper)', () => {
    const scrollIntoView = vi.spyOn(window.HTMLElement.prototype, 'scrollIntoView');
    setFocusVisible(true);
    render(
      <div className="admin-table__scroll" tabIndex={0} onFocus={revealFocusedControl}>
        {createPortal(<button type="button">Portalled item</button>, document.body)}
      </div>,
    );

    fireEvent.focus(screen.getByRole('button', { name: 'Portalled item' }));

    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it('leaves the wrapper itself and the inline editor rows to the browser', () => {
    const scrollIntoView = vi.spyOn(window.HTMLElement.prototype, 'scrollIntoView');
    setFocusVisible(true);
    const wrapper = renderWrapper();

    fireEvent.focus(wrapper);
    fireEvent.focus(screen.getByRole('textbox', { name: 'Name' }));

    expect(scrollIntoView).not.toHaveBeenCalled();
  });
});
