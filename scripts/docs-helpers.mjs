// Shared helpers for the docs contract tests (not a test file, so importing it runs no tests).

// Every json tag declared anywhere in a Go source.
export function jsonTagsOf(goSource) {
  return new Set(
    [...goSource.matchAll(/`json:"([a-z_]+)(?:,[a-z]+)?"`/g)].map((match) => match[1]),
  );
}

// Heuristic: catches a 32-hex-character id and a *.cloudflareaccess.com hostname.
export function realIdentifiersIn(text) {
  const found = [];
  const hex = text.match(/\b[0-9a-f]{32}\b/i);
  if (hex) found.push(hex[0]);
  const accessHost = text.match(/[\w.-]+\.cloudflareaccess\.com/i);
  if (accessHost) found.push(accessHost[0]);
  return found;
}
