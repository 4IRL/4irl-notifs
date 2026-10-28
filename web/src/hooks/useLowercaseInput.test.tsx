import { useState } from 'react';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { useLowercaseInput } from './useLowercaseInput';

afterEach(() => {
  cleanup();
});

function Harness({
  setValueSpy,
  initial = '',
}: {
  setValueSpy: (v: string) => void;
  initial?: string;
}) {
  const [value, setValue] = useState(initial);
  const handlers = useLowercaseInput({
    setValue: (next) => {
      setValueSpy(next);
      setValue(next);
    },
  });
  return <input aria-label="id" value={value} {...handlers} />;
}

function setup({ initial }: { initial?: string } = {}) {
  const setValue = vi.fn();
  render(<Harness setValueSpy={setValue} initial={initial} />);
  const input = screen.getByLabelText<HTMLInputElement>('id');
  return { setValue, input };
}

/** jsdom resets the selection when a value is assigned, so fake the caret the
 *  browser would report for the edited value and spy on the restore. */
function changeWithCaret({
  input,
  value,
  caret,
}: {
  input: HTMLInputElement;
  value: string;
  caret: number;
}) {
  Object.defineProperty(input, 'selectionStart', { configurable: true, get: () => caret });
  const restore = vi.spyOn(input, 'setSelectionRange');
  fireEvent.change(input, { target: { value } });
  return restore;
}

describe('useLowercaseInput', () => {
  it('lowercases typed characters', async () => {
    const { input } = setup();
    await userEvent.type(input, 'MyApp');
    expect(input.value).toBe('myapp');
  });

  it('keeps the caret after a character typed mid-value', () => {
    const { input } = setup({ initial: 'abcd' });
    const restore = changeWithCaret({ input, value: 'abXcd', caret: 3 });
    expect(input.value).toBe('abxcd');
    expect(restore).toHaveBeenLastCalledWith(3, 3);
  });

  it('keeps the caret after text pasted mid-value', () => {
    const { input } = setup({ initial: 'abcd' });
    const restore = changeWithCaret({ input, value: 'abABCcd', caret: 5 });
    expect(input.value).toBe('ababccd');
    expect(restore).toHaveBeenLastCalledWith(5, 5);
  });

  it('calls setValue exactly once per input event', () => {
    const { input, setValue } = setup();
    fireEvent.change(input, { target: { value: 'A' } });
    expect(setValue).toHaveBeenCalledTimes(1);
    expect(setValue).toHaveBeenLastCalledWith('a');
    fireEvent.change(input, { target: { value: 'ab' } });
    expect(setValue).toHaveBeenCalledTimes(2);
    expect(setValue).toHaveBeenLastCalledWith('ab');
  });

  it('does not call setValue for an event whose value is unchanged', () => {
    const { input, setValue } = setup({ initial: 'abc' });
    fireEvent.change(input, { target: { value: 'abc' } });
    expect(setValue).not.toHaveBeenCalled();
  });

  it('leaves the value untouched during composition and lowercases on compositionend', () => {
    const { input, setValue } = setup();
    fireEvent.compositionStart(input);
    fireEvent.change(input, { target: { value: 'AB' } });
    expect(input.value).toBe('AB');
    expect(setValue).toHaveBeenLastCalledWith('AB');
    fireEvent.compositionEnd(input);
    expect(input.value).toBe('ab');
    expect(setValue).toHaveBeenLastCalledWith('ab');
  });

  it('lowercases a mixed-case value on blur', () => {
    const { input, setValue } = setup();
    fireEvent.compositionStart(input);
    fireEvent.change(input, { target: { value: 'Ab' } });
    fireEvent.blur(input);
    expect(input.value).toBe('ab');
    expect(setValue).toHaveBeenLastCalledWith('ab');
  });

  it('restores the caret after compositionend lowercases the value', () => {
    const { input } = setup();
    fireEvent.compositionStart(input);
    fireEvent.change(input, { target: { value: 'ABCD' } });
    Object.defineProperty(input, 'selectionStart', { configurable: true, get: () => 2 });
    const restore = vi.spyOn(input, 'setSelectionRange');
    fireEvent.compositionEnd(input);
    expect(input.value).toBe('abcd');
    expect(restore).toHaveBeenLastCalledWith(2, 2);
  });

  it('does not call setValue on blur when already lowercase', () => {
    const { input, setValue } = setup({ initial: 'abc' });
    fireEvent.blur(input);
    expect(setValue).not.toHaveBeenCalled();
  });
});
