import { describe, expect, it } from 'vitest';
import { setupWebPorts } from './worktree-ports.ts';

const DEFAULTS = { webPort: 5473, e2ePort: 4473 };

describe('setupWebPorts', () => {
  it('returns defaults with no file and empty env', () => {
    expect(setupWebPorts({ fileText: null, env: {} })).toEqual(DEFAULTS);
  });

  it('file overrides defaults per key', () => {
    expect(setupWebPorts({ fileText: 'SETUP_WEB_PORT=5594\n', env: {} })).toEqual({
      webPort: 5594,
      e2ePort: 4473,
    });
  });

  it('env overrides file per key', () => {
    const fileText = 'SETUP_WEB_PORT=5594\nSETUP_E2E_PORT=4594\n';
    expect(setupWebPorts({ fileText, env: { SETUP_WEB_PORT: '5600' } })).toEqual({
      webPort: 5600,
      e2ePort: 4594,
    });
  });

  it('ignores the admin UI keys and unrelated lines', () => {
    const fileText = '# comment\n\nWEB_PORT=5300\nE2E_PORT=4300\nSETUP_E2E_PORT=4594\n';
    expect(setupWebPorts({ fileText, env: {} })).toEqual({ webPort: 5473, e2ePort: 4594 });
  });

  it('treats an empty-string env value as unset', () => {
    expect(
      setupWebPorts({ fileText: 'SETUP_WEB_PORT=5594\n', env: { SETUP_WEB_PORT: '' } }),
    ).toEqual({ webPort: 5594, e2ePort: 4473 });
  });

  it.each(['abc', '80.5', '0', '65536'])('throws on invalid file value %s', (value) => {
    expect(() => setupWebPorts({ fileText: `SETUP_WEB_PORT=${value}\n`, env: {} })).toThrow(
      new RegExp(`SETUP_WEB_PORT.*${value.replace('.', '\\.')}`),
    );
  });

  it.each(['abc', '80.5', '0', '65536'])('throws on invalid env value %s', (value) => {
    expect(() => setupWebPorts({ fileText: null, env: { SETUP_E2E_PORT: value } })).toThrow(
      new RegExp(`SETUP_E2E_PORT.*${value.replace('.', '\\.')}`),
    );
  });
});
