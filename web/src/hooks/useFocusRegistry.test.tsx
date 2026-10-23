import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';

import { useFocusRegistry } from './useFocusRegistry';

function Harness({ showLate }: { showLate: boolean }) {
  const { registerControl, requestFocus } = useFocusRegistry();
  return (
    <div>
      <button type="button" onClick={() => requestFocus('early')}>
        go early
      </button>
      <button type="button" onClick={() => requestFocus('late')}>
        go late
      </button>
      <button type="button" ref={(element) => registerControl({ key: 'early', element })}>
        early
      </button>
      {showLate && (
        <button type="button" ref={(element) => registerControl({ key: 'late', element })}>
          late
        </button>
      )}
    </div>
  );
}

describe('useFocusRegistry', () => {
  it('focuses a registered control on request', async () => {
    const user = userEvent.setup();
    render(<Harness showLate={false} />);

    await user.click(screen.getByText('go early'));

    expect(screen.getByText('early')).toHaveFocus();
  });

  it('keeps a request pending until its target mounts', async () => {
    const user = userEvent.setup();
    const { rerender } = render(<Harness showLate={false} />);

    await user.click(screen.getByText('go late'));
    expect(screen.getByText('early')).not.toHaveFocus();

    rerender(<Harness showLate />);

    expect(screen.getByText('late')).toHaveFocus();
  });

  it('hands a request for a hidden control to its row’s visible button', async () => {
    const user = userEvent.setup();
    function HiddenHarness() {
      const { registerControl, requestFocus } = useFocusRegistry();
      return (
        <div>
          <button type="button" onClick={() => requestFocus('edit')}>
            go edit
          </button>
          <table>
            <tbody>
              <tr>
                <td>
                  <button
                    type="button"
                    hidden
                    ref={(element) => registerControl({ key: 'edit', element })}
                  >
                    edit
                  </button>
                  <button type="button">confirm</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      );
    }
    render(<HiddenHarness />);

    await user.click(screen.getByText('go edit'));

    expect(screen.getByText('confirm')).toHaveFocus();
  });

  it('forgets an unmounted control', async () => {
    const user = userEvent.setup();
    const { rerender } = render(<Harness showLate />);
    rerender(<Harness showLate={false} />);

    await user.click(screen.getByText('go late'));

    expect(screen.queryByText('late')).toBeNull();
    expect(screen.getByText('go late')).toHaveFocus();
  });
});
