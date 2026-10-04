import { describe, expect, it } from 'vitest';
import { webPorts } from './worktree-ports.ts';

const DEFAULTS = { apiPort: 8091, webPort: 5173, e2ePort: 4173 };

describe('webPorts', () => {
  it('returns defaults with no file and empty env', () => {
    expect(webPorts({ fileText: null, env: {} })).toEqual(DEFAULTS);
  });

  it('file overrides defaults per key', () => {
    const fileText = 'WEB_PORT=5300\nAPI_PORT=8100\n';
    expect(webPorts({ fileText, env: {} })).toEqual({
      apiPort: 8100,
      webPort: 5300,
      e2ePort: 4173,
    });
  });

  it('env overrides file per key', () => {
    const fileText = 'WEB_PORT=5300\nAPI_PORT=8100\nE2E_PORT=4300\n';
    expect(webPorts({ fileText, env: { WEB_PORT: '5400' } })).toEqual({
      apiPort: 8100,
      webPort: 5400,
      e2ePort: 4300,
    });
  });

  it('skips comment and blank lines', () => {
    const fileText = '# comment\n\n  \nE2E_PORT=4300\n# WEB_PORT=1\n';
    expect(webPorts({ fileText, env: {} })).toEqual({
      ...DEFAULTS,
      e2ePort: 4300,
    });
  });

  it('treats an empty-string env value as unset', () => {
    const fileText = 'WEB_PORT=5300\n';
    expect(webPorts({ fileText, env: { WEB_PORT: '', API_PORT: '' } })).toEqual({
      ...DEFAULTS,
      webPort: 5300,
    });
  });

  it.each(['abc', '80.5', '0', '65536'])('throws on invalid file value %s', (value) => {
    expect(() => webPorts({ fileText: `WEB_PORT=${value}\n`, env: {} })).toThrow(
      new RegExp(`WEB_PORT.*${value.replace('.', '\\.')}`),
    );
  });

  it.each(['abc', '80.5', '0', '65536'])('throws on invalid env value %s', (value) => {
    expect(() => webPorts({ fileText: null, env: { API_PORT: value } })).toThrow(
      new RegExp(`API_PORT.*${value.replace('.', '\\.')}`),
    );
  });
});
