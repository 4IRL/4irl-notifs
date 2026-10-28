import { afterEach, describe, expect, it } from 'vitest';

import { rowIdOf } from './heartbeatRowId';

afterEach(() => {
  document.body.innerHTML = '';
});

function mount(html: string): HTMLElement {
  document.body.innerHTML = html;
  return document.body;
}

describe('rowIdOf', () => {
  it('returns the id of the row containing the element', () => {
    const body = mount(
      '<table><tr data-check-id="4"><td><button id="b"></button></td></tr></table>',
    );
    expect(rowIdOf(body.querySelector('#b'))).toBe('4');
  });

  it('prefers the plain row over an owner stamp when both are ancestors', () => {
    const body = mount(
      '<div data-owner-row-attribute="data-check-id" data-owner-row-id="9">' +
        '<div data-check-id="4"><button id="b"></button></div></div>',
    );
    expect(rowIdOf(body.querySelector('#b'))).toBe('4');
  });

  it('resolves an element inside an owner-stamped dialog to the owned row', () => {
    const body = mount(
      '<div role="dialog" data-owner-row-attribute="data-check-id" data-owner-row-id="9">' +
        '<button id="b"></button></div>',
    );
    expect(rowIdOf(body.querySelector('#b'))).toBe('9');
  });

  it('ignores an owner stamp for a different row attribute', () => {
    const body = mount(
      '<div data-owner-row-attribute="data-route-id" data-owner-row-id="9">' +
        '<button id="b"></button></div>',
    );
    expect(rowIdOf(body.querySelector('#b'))).toBeNull();
  });

  it('returns null for no element or an element outside any row', () => {
    const body = mount('<button id="b"></button>');
    expect(rowIdOf(null)).toBeNull();
    expect(rowIdOf(body.querySelector('#b'))).toBeNull();
  });
});
