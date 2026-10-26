// Docs check for the website probe admin contract: the documented routes and
// field names must still exist in the delivery-api source, and the gating
// runbook must keep its shape and stay free of real identifiers.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '..');
const readRepoFile = (relativePath) => readFileSync(join(repoRoot, relativePath), 'utf8');

const serverSource = readRepoFile('delivery-api/internal/httpapi/server.go');
const heartbeatsSource = readRepoFile('delivery-api/internal/httpapi/heartbeats.go');
const architectureDoc = readRepoFile('ARCHITECTURE.md');
const guideDoc = readRepoFile('docs/app-integration-guide.md');

const PROBE_JSON_TAGS = [
  'url',
  'expected_statuses',
  'body_match',
  'timeout_seconds',
  'interval_seconds',
  'access_token',
  'client_id',
  'client_secret',
  'consecutive_failures',
  'failure_threshold',
  'has_access_token',
  'last_result',
  'down_reason',
];

const VPS_CAVEAT =
  'Probe checks run from the VPS, so they cannot see a VPS or Cloudflare Tunnel outage; the watchdog covers that, and external-vantage probing is follow-up #101.';

const GATING_HEADING = '#### Gating a health endpoint for probes';

// Every json tag declared anywhere in heartbeats.go.
export function jsonTagsOf(goSource) {
  return new Set(
    [...goSource.matchAll(/`json:"([a-z_]+)(?:,[a-z]+)?"`/g)].map((match) => match[1]),
  );
}

// Route patterns ("POST /v1/admin/heartbeats/{id}/pause" -> the path part) of the
// admin heartbeat HandleFunc registrations.
export function adminHeartbeatRoutesOf(goSource) {
  return new Set(
    [...goSource.matchAll(/HandleFunc\("[A-Z]+ (\/v1\/admin\/heartbeats[^"]*)"/g)].map(
      (match) => match[1],
    ),
  );
}

// Every /v1/admin/heartbeats path a doc mentions.
export function documentedAdminRoutes(docText) {
  return new Set(
    [...docText.matchAll(/\/v1\/admin\/heartbeats(?:\/\{id\})?(?:\/(?:pause|resume))?/g)].map(
      (match) => match[0],
    ),
  );
}

// Field names inside the doc's inline `{...}` body/view shapes that mention "url"
// (the probe shapes), returned when they are not a json tag in the source.
export function unknownDocumentedFields(docText, knownTags) {
  const unknown = new Set();
  for (const [, span] of docText.matchAll(/`([^`]+)`/g)) {
    if (!span.startsWith('{') || !span.includes('"url"')) continue;
    for (const [, field] of span.matchAll(/"([a-z_]+)"/g)) {
      if (!knownTags.has(field)) unknown.add(field);
    }
  }
  return [...unknown];
}

// The text from a heading to the next heading of the same or higher level.
export function sectionOf(docText, heading) {
  const start = docText.indexOf(`${heading}\n`);
  if (start === -1) return null;
  const level = heading.match(/^#+/)[0].length;
  const rest = docText.slice(start + heading.length + 1);
  const nextHeading = rest.match(new RegExp(`^#{1,${level}} `, 'm'));
  return nextHeading ? rest.slice(0, nextHeading.index) : rest;
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

test('every documented /v1/admin/heartbeats route exists in server.go', () => {
  const sourceRoutes = adminHeartbeatRoutesOf(serverSource);
  assert.ok(sourceRoutes.size >= 4, 'expected the admin heartbeat routes in server.go');
  for (const [name, doc] of [
    ['ARCHITECTURE.md', architectureDoc],
    ['docs/app-integration-guide.md', guideDoc],
  ]) {
    const documented = documentedAdminRoutes(doc);
    assert.ok(documented.size > 0, `${name} documents no admin heartbeat route`);
    for (const route of documented) {
      assert.ok(
        sourceRoutes.has(route),
        `${name} mentions ${route}, which server.go does not register`,
      );
    }
  }
});

test('documented probe field names exist as json tags in heartbeats.go', () => {
  const knownTags = jsonTagsOf(heartbeatsSource);
  for (const tag of PROBE_JSON_TAGS) {
    assert.ok(knownTags.has(tag), `heartbeats.go no longer has json tag ${tag}`);
  }
  assert.deepEqual(unknownDocumentedFields(architectureDoc, knownTags), [], 'ARCHITECTURE.md');
  assert.deepEqual(unknownDocumentedFields(guideDoc, knownTags), [], 'app-integration-guide.md');
});

test('every probe json tag appears in the integration guide', () => {
  for (const tag of PROBE_JSON_TAGS) {
    assert.ok(
      guideDoc.includes(`"${tag}"`) || guideDoc.includes(`\`${tag}\``),
      `guide misses ${tag}`,
    );
  }
});

test('ARCHITECTURE.md carries the VPS-outage caveat', () => {
  assert.ok(architectureDoc.includes(VPS_CAVEAT));
});

test('the gating runbook keeps its shape and has no real identifiers', () => {
  const section = sectionOf(architectureDoc, GATING_HEADING);
  assert.ok(section, `ARCHITECTURE.md misses the heading "${GATING_HEADING}"`);
  for (const label of ['**Pi apps.**', '**VPS apps.**', '**Workers/Pages.**']) {
    assert.ok(section.includes(label), `runbook misses ${label}`);
  }
  assert.ok(section.includes('developers.cloudflare.com'), 'runbook has no Cloudflare docs link');
  assert.deepEqual(realIdentifiersIn(section), []);
});

test('negative self-test: the matchers fail on bad input', () => {
  const knownTags = jsonTagsOf(heartbeatsSource);
  assert.deepEqual(
    unknownDocumentedFields('`{"url","bogus_field"}`', knownTags),
    ['bogus_field'],
    'a bogus documented field must be reported',
  );
  assert.ok(!adminHeartbeatRoutesOf(serverSource).has('/v1/admin/heartbeats/{id}/bogus'));
  assert.ok(documentedAdminRoutes('see /v1/admin/heartbeats/{id}/pause').size === 1);
  assert.equal(sectionOf('# nothing here\n', GATING_HEADING), null);
  const fixtureHex = `token id ${'a1'.repeat(16)}`;
  assert.equal(realIdentifiersIn(fixtureHex).length, 1);
  assert.equal(realIdentifiersIn('https://acme.cloudflareaccess.com/x').length, 1);
  assert.deepEqual(realIdentifiersIn('use <team-domain> and <probe-token>'), []);
});
