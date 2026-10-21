// Parser for the Cloudflare Pages `_headers` format, shared by the unit test that
// pins `public/_headers` (src/headers.test.ts) and the e2e specs that replay those
// exact headers onto `vite preview` (e2e/support.ts), which does not apply `_headers`.
// An unindented line is a URL pattern; the indented `Name: value` lines under it are
// the headers applied to that pattern.

export type HeaderRules = Record<string, Record<string, string>>;

export function parseHeadersFile(text: string): HeaderRules {
  const rules: HeaderRules = {};
  let pattern: string | null = null;
  for (const line of text.split('\n')) {
    if (line.trim() === '' || line.trim().startsWith('#')) continue;
    if (!/^\s/.test(line)) {
      pattern = line.trim();
      if (pattern in rules) throw new Error(`duplicate URL pattern: ${pattern}`);
      rules[pattern] = {};
      continue;
    }
    if (pattern === null) throw new Error(`header line before any URL pattern: ${line}`);
    const separator = line.indexOf(':');
    if (separator < 1) throw new Error(`header line without a "Name: value" form: ${line}`);
    const name = line.slice(0, separator).trim();
    if (name in rules[pattern]) throw new Error(`duplicate header ${name} under ${pattern}`);
    rules[pattern][name] = line.slice(separator + 1).trim();
  }
  return rules;
}
