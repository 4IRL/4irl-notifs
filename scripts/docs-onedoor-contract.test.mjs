// Keeps docs/app-integration-guide.md honest about the one-door provisioning model: every
// documented door/admin route is registered in delivery-api, the retired door is never named,
// and every json example in the door sections only uses fields the route's own structs accept.
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

import { jsonTagsOf, realIdentifiersIn } from './docs-heartbeat-probes.test.mjs';

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '..');
const readRepoFile = (relativePath) => readFileSync(join(repoRoot, relativePath), 'utf8');

const guideDoc = readRepoFile('docs/app-integration-guide.md');
const serverSource = readRepoFile('delivery-api/internal/httpapi/server.go');

const DOOR_HEADING = '## Provisioning your users through the door';
const LOGIN_HEADING = '## Subscriber login — the generated password';
const BROADCAST_HEADING = '## Broadcast / site-wide announcements';

const DOOR_ROUTES = [
  'provision',
  'deprovision',
  'set-password',
  'reset-password',
  'ntfy-publisher',
];
const DOOR_KINDS = ['request', 'response', 'error'];

const DOOR_SECTION_HEADINGS = [
  { section: DOOR_HEADING, prefix: '### Provision a user' },
  { section: DOOR_HEADING, prefix: '### Receive notifications (subscriber side)' },
  { section: DOOR_HEADING, prefix: '### Mint or rotate the publisher' },
  { section: DOOR_HEADING, prefix: '### Deprovision a user' },
  { section: LOGIN_HEADING, prefix: '### `POST /v1/apps/{app_id}/reset-password`' },
  { section: LOGIN_HEADING, prefix: '### `POST /v1/apps/{app_id}/set-password`' },
];

const RETIRED_TERMS = [
  'notifs-api',
  '/v1/provision-app',
  '/v1/deprovision-app',
  'Mint your publisher token',
  'publisher-token',
];
const RETIRED_ROUTE_PATTERN = /\/v1\/(provision|deprovision|reset-password|set-password)(?![\w-])/;

const HTTP_METHODS = 'GET|POST|PUT|PATCH|DELETE';

// The request and response struct that own each door route's json keys.
const ROUTE_STRUCTS = {
  provision: {
    request: ['delivery-api/internal/httpapi/apps_provisioning.go', 'appProvisionRequestBody'],
    response: ['provisioning-api/internal/httpapi/server.go', 'provisionResponseBody'],
  },
  deprovision: {
    request: ['delivery-api/internal/httpapi/apps_provisioning.go', 'appDeprovisionRequestBody'],
    response: ['provisioning-api/internal/httpapi/server.go', 'deprovisionResponseBody'],
  },
  'set-password': {
    request: ['delivery-api/internal/httpapi/apps_provisioning.go', 'appSetPasswordRequestBody'],
    response: ['provisioning-api/internal/httpapi/server.go', 'setPasswordResponseBody'],
  },
  'reset-password': {
    request: ['delivery-api/internal/httpapi/apps_provisioning.go', 'appResetPasswordRequestBody'],
    response: ['provisioning-api/internal/httpapi/server.go', 'resetPasswordResponseBody'],
  },
  'ntfy-publisher': {
    request: ['delivery-api/internal/httpapi/apps_publishers.go', 'appMintPublisherRequestBody'],
    response: ['delivery-api/internal/httpapi/publishers.go', 'publisherResponseBody'],
  },
};
const PUBLISHER_VIEW = ['delivery-api/internal/publisher/types.go', 'PublisherView'];

// The json tags of one struct: the source from `type <name> struct {` to the first following
// line that is exactly `}`. Throws when the struct is gone so a Go rename is caught.
export function structTagsOf(goSource, typeName) {
  const opener = `type ${typeName} struct {`;
  const start = goSource.indexOf(opener);
  if (start === -1) throw new Error(`struct ${typeName} not found`);
  const rest = goSource.slice(start + opener.length);
  const end = rest.search(/^\}$/m);
  return jsonTagsOf(end === -1 ? rest : rest.slice(0, end));
}

function loadTagMap() {
  const tagsFor = ([file, name]) => structTagsOf(readRepoFile(file), name);
  const tagMap = {};
  for (const [route, { request, response }] of Object.entries(ROUTE_STRUCTS)) {
    tagMap[route] = {
      request: tagsFor(request),
      response: tagsFor(response),
      error: new Set(['error']),
    };
  }
  tagMap.publisherView = tagsFor(PUBLISHER_VIEW);
  return tagMap;
}

// ---------------------------------------------------------------- routes

// Params become `{}`: `{name}`, `<name>`, and the concrete app id in an example URL such as
// `/v1/apps/urls4irl/provision`.
const normalizeParams = (path) =>
  path
    .replace(/\{[^{},]*\}/g, '{}')
    .replace(/<[^>]*>/g, '{}')
    .replace(/^(\/v1\/(?:admin\/)?apps\/)[^/]+/, '$1{}');

// Registered "METHOD /path" strings (params normalized to {}) from server.go.
export function registeredRoutesOf(goSource) {
  return new Set(
    [...goSource.matchAll(/HandleFunc\(\s*"([A-Z]+) ([^"]+)"/g)].map(
      (match) => `${match[1]} ${normalizeParams(match[2])}`,
    ),
  );
}

// Expand flat `{a,b}` comma groups into one path per member. A comma group containing a
// nested brace is rejected: the guide must write such routes out separately.
export function expandGroups(path) {
  let depth = 0;
  let start = -1;
  let nested = false;
  for (let index = 0; index < path.length; index += 1) {
    const char = path[index];
    if (char === '{') {
      if (depth === 0) {
        start = index;
        nested = false;
      } else {
        nested = true;
      }
      depth += 1;
    } else if (char === '}' && depth > 0) {
      depth -= 1;
      if (depth === 0) {
        const content = path.slice(start + 1, index);
        if (content.includes(',')) {
          if (nested) throw new Error(`nested brace group in route ${path}`);
          return content
            .split(',')
            .flatMap((member) =>
              expandGroups(path.slice(0, start) + member.trim() + path.slice(index + 1)),
            );
        }
      }
    }
  }
  return [path];
}

const ALTERNATION = new RegExp(
  `\\b(?:${HTTP_METHODS})(?:\\\\?\\|(?:${HTTP_METHODS}))+\\s+/(?:v1|healthz)/`,
);
const ROUTE_PATTERN = new RegExp(
  `(?:\\b(${HTTP_METHODS})\\s+)?(/(?:v1/(?:apps|admin|app-identity)/|healthz/provisioning)[^\\s\`"'()\\[\\];?|\\\\…]*)`,
  'g',
);

// Every door/admin/app-identity/provisioning-health route a doc writes, as {method, path}
// with groups expanded and params normalized. Prefixes and wildcards are skipped.
export function documentedRoutes(docText) {
  if (ALTERNATION.test(docText)) {
    throw new Error('method alternation before a route: write one route per method');
  }
  const routes = [];
  for (const [, method, rawPath] of docText.matchAll(ROUTE_PATTERN)) {
    const path = rawPath.replace(/[.,:]+$/, '');
    if (path.endsWith('/') || path.endsWith('*')) continue;
    for (const expanded of expandGroups(path)) {
      routes.push({ method: method ?? null, path: normalizeParams(expanded) });
    }
  }
  return routes;
}

export function unregisteredRoutes(docText, registered) {
  const registeredPaths = new Set([...registered].map((route) => route.split(' ')[1]));
  return documentedRoutes(docText)
    .filter(({ method, path }) =>
      method === null ? !registeredPaths.has(path) : !registered.has(`${method} ${path}`),
    )
    .map(({ method, path }) => `${method ?? '(any)'} ${path}`);
}

export function retiredTermsIn(docText) {
  const found = RETIRED_TERMS.filter((term) => docText.includes(term));
  const route = docText.match(RETIRED_ROUTE_PATTERN);
  if (route) found.push(route[0]);
  return found;
}

// ---------------------------------------------------------------- sections and fences

const FENCE_OPENER = /^(\s*)(`{3,}|~{3,})(.*)$/;

// Iterate lines with their fenced-or-not state; a `#` line inside a fence is not a heading.
function* linesWithFenceState(docText) {
  let fence = null;
  for (const line of stripHtmlComments(docText).split('\n')) {
    const opener = line.match(FENCE_OPENER);
    if (fence === null) {
      yield { line, fenced: false };
      if (opener) fence = opener[2];
    } else {
      yield { line, fenced: true };
      const closer = line.match(/^\s*(`{3,}|~{3,})\s*$/);
      if (closer && closer[1][0] === fence[0] && closer[1].length >= fence.length) fence = null;
    }
  }
}

// The text from a heading line to the next heading of the same or higher level, ignoring
// `#` lines inside fenced blocks. Null when the heading is absent.
export function fencedSectionOf(docText, heading) {
  const level = heading.match(/^#+/)[0].length;
  const collected = [];
  let inSection = false;
  for (const { line, fenced } of linesWithFenceState(docText)) {
    if (!fenced && !inSection && line === heading) {
      inSection = true;
      continue;
    }
    if (inSection) {
      if (!fenced && new RegExp(`^#{1,${level}} `).test(line)) break;
      collected.push(line);
    }
  }
  return inSection ? collected.join('\n') : null;
}

export const hasHeadingWithPrefix = (sectionText, prefix) =>
  [...linesWithFenceState(sectionText)].some(
    ({ line, fenced }) => !fenced && line.startsWith(prefix),
  );

const stripHtmlComments = (text) => text.replace(/<!--[\s\S]*?-->/g, '');

// Fenced json blocks (indented fences included): {tag, body}. Other languages are skipped.
export function jsonFencesOf(sectionText) {
  const blocks = [];
  let open = null;
  for (const line of stripHtmlComments(sectionText).split('\n')) {
    if (open === null) {
      const opener = line.match(FENCE_OPENER);
      if (!opener) continue;
      const [language, tag] = opener[3].trim().split(/\s+/);
      open = { fence: opener[2], isJson: language === 'json', tag: tag ?? null, lines: [] };
    } else {
      const closer = line.match(/^\s*(`{3,}|~{3,})\s*$/);
      if (closer && closer[1][0] === open.fence[0] && closer[1].length >= open.fence.length) {
        if (open.isJson) blocks.push({ tag: open.tag, body: open.lines.join('\n') });
        open = null;
      } else {
        open.lines.push(line);
      }
    }
  }
  return blocks;
}

// ---------------------------------------------------------------- door json examples

function parseTag(tag) {
  if (tag === null) return null;
  const [route, kind, ...extra] = tag.split('.');
  if (extra.length > 0 || !DOOR_ROUTES.includes(route) || !DOOR_KINDS.includes(kind)) return null;
  return { route, kind };
}

// Problems with the json fences in one section, plus how many non-elided request/response
// examples each route has.
export function checkDoorExamples(sectionText, tagMap) {
  const problems = [];
  const counts = Object.fromEntries(DOOR_ROUTES.map((route) => [route, 0]));
  for (const { tag, body } of jsonFencesOf(sectionText)) {
    const parsedTag = parseTag(tag);
    if (parsedTag === null) {
      problems.push(`json fence with missing or unknown tag: ${tag ?? '(untagged)'}`);
      continue;
    }
    // Elided only when `...` appears outside string values.
    if (body.replace(/"(?:[^"\\]|\\.)*"/g, '""').includes('...')) continue;
    let value;
    try {
      value = JSON.parse(body);
    } catch {
      problems.push(`${tag}: example is not valid JSON`);
      continue;
    }
    if (value === null || typeof value !== 'object' || Array.isArray(value)) {
      problems.push(`${tag}: example is not a JSON object`);
      continue;
    }
    const { route, kind } = parsedTag;
    const allowed = tagMap[route][kind];
    for (const [key, child] of Object.entries(value)) {
      if (!allowed.has(key)) problems.push(`${tag}: unknown field "${key}"`);
      const isPublisher = route === 'ntfy-publisher' && kind === 'response' && key === 'publisher';
      if (isPublisher && (child === null || typeof child !== 'object' || Array.isArray(child))) {
        problems.push(`${tag}: "publisher" must be an object`);
        continue;
      }
      if (child === null || typeof child !== 'object') continue;
      if (!isPublisher || Array.isArray(child)) {
        problems.push(`${tag}: nested value under "${key}" is not allowed`);
        continue;
      }
      for (const [nestedKey, grandChild] of Object.entries(child)) {
        if (!tagMap.publisherView.has(nestedKey)) {
          problems.push(`${tag}: unknown publisher field "${nestedKey}"`);
        }
        if (grandChild !== null && typeof grandChild === 'object') {
          problems.push(`${tag}: nested value under "${nestedKey}" is not allowed`);
        }
      }
    }
    if (kind !== 'error') counts[route] += 1;
  }
  return { problems, counts };
}

// Floor assertions: sections and headings exist, every route has a checked example.
export function floorProblems(docText, tagMap) {
  const problems = [];
  const totals = Object.fromEntries(DOOR_ROUTES.map((route) => [route, 0]));
  for (const heading of [DOOR_HEADING, LOGIN_HEADING]) {
    const section = fencedSectionOf(docText, heading);
    if (section === null) {
      problems.push(`missing section ${heading}`);
      continue;
    }
    for (const route of DOOR_ROUTES)
      totals[route] += checkDoorExamples(section, tagMap).counts[route];
    for (const { section: owner, prefix } of DOOR_SECTION_HEADINGS) {
      if (owner === heading && !hasHeadingWithPrefix(section, prefix)) {
        problems.push(`missing heading ${prefix}`);
      }
    }
  }
  for (const route of DOOR_ROUTES) {
    if (totals[route] < 1) problems.push(`route ${route} has no non-elided tagged example`);
  }
  return problems;
}

function doorExampleProblems(docText, tagMap) {
  return [DOOR_HEADING, LOGIN_HEADING].flatMap((heading) => {
    const section = fencedSectionOf(docText, heading);
    return section === null ? [] : checkDoorExamples(section, tagMap).problems;
  });
}

// ---------------------------------------------------------------- the guide

const tagMap = loadTagMap();
const registeredRoutes = registeredRoutesOf(serverSource);

test('every route the guide documents is registered in server.go', () => {
  assert.ok(registeredRoutes.size > 20, 'server.go routes were not parsed');
  assert.deepEqual(unregisteredRoutes(guideDoc, registeredRoutes), []);
});

test('the guide names no retired door, route or term', () => {
  assert.deepEqual(retiredTermsIn(guideDoc), []);
});

test('door examples only use fields of their own route structs', () => {
  assert.deepEqual(doorExampleProblems(guideDoc, tagMap), []);
});

test('door sections exist with their headings and every route has a checked example', () => {
  assert.deepEqual(floorProblems(guideDoc, tagMap), []);
});

export function identifierProblems(docText) {
  return [DOOR_HEADING, LOGIN_HEADING].flatMap((heading) =>
    realIdentifiersIn(fencedSectionOf(docText, heading) ?? ''),
  );
}

export function broadcastProblems(docText) {
  const section = fencedSectionOf(docText, BROADCAST_HEADING);
  if (section === null) return ['broadcast section missing'];
  return ['#174', 'issues/174'].filter((needle) => !section.includes(needle));
}

test('door sections contain no real identifiers', () => {
  assert.deepEqual(identifierProblems(guideDoc), []);
});

test('the broadcast section links the tracking issue #174', () => {
  assert.deepEqual(broadcastProblems(guideDoc), []);
});

// ---------------------------------------------------------------- negative self-tests

const REGISTERED = new Set([
  'POST /v1/apps/{}/provision',
  'POST /v1/apps/{}/deprovision',
  'GET /v1/admin/users',
  'GET /healthz/provisioning',
]);

const block = (tag, body) => `\`\`\`json${tag ? ` ${tag}` : ''}\n${body}\n\`\`\`\n`;
const sectionDoc = (body) => `${DOOR_HEADING}\n\n${body}\n`;

test('negative self-test: route checks fire', () => {
  assert.deepEqual(unregisteredRoutes('`POST /v1/apps/{app_id}/provision`', REGISTERED), []);
  assert.deepEqual(
    unregisteredRoutes('`/v1/admin/users` and `/healthz/provisioning`', REGISTERED),
    [],
  );
  assert.deepEqual(unregisteredRoutes('`GET /v1/admin/users/<id>`', REGISTERED), [
    'GET /v1/admin/users/{}',
  ]);
  assert.deepEqual(unregisteredRoutes('`POST /v1/apps/{app_id}/nope`', REGISTERED), [
    'POST /v1/apps/{}/nope',
  ]);
  assert.deepEqual(unregisteredRoutes('`GET /v1/apps/{app_id}/provision`', REGISTERED), [
    'GET /v1/apps/{}/provision',
  ]);
  assert.deepEqual(
    unregisteredRoutes('`POST /v1/apps/{app_id}/{provision,deprovision}`', REGISTERED),
    [],
  );
  assert.deepEqual(unregisteredRoutes('`POST /v1/apps/{app_id}/{provision,rotate}`', REGISTERED), [
    'POST /v1/apps/{}/rotate',
  ]);
  assert.deepEqual(
    unregisteredRoutes('prefixes `/v1/apps/{app_id}/`, `/v1/admin/*`', REGISTERED),
    [],
  );
  assert.throws(() => documentedRoutes('`POST|GET /v1/admin/users`'), /alternation/);
  assert.throws(() => documentedRoutes('`POST\\|GET /v1/admin/users`'), /alternation/);
  assert.throws(() => documentedRoutes('`POST /v1/apps/{app_id}/{a,{b,c}}`'), /nested/);
});

test('negative self-test: retired terms fire', () => {
  assert.deepEqual(retiredTermsIn('talk to notifs-api'), ['notifs-api']);
  assert.deepEqual(retiredTermsIn('POST /v1/deprovision-app'), ['/v1/deprovision-app']);
  assert.deepEqual(retiredTermsIn('POST /v1/provision now'), ['/v1/provision']);
  assert.deepEqual(retiredTermsIn('POST /v1/apps/{app_id}/provision and /v1/admin/provision'), []);
});

test('negative self-test: section helper is fence-aware and floors fire', () => {
  const doc = `${DOOR_HEADING}\n\n\`\`\`sh\n# not a heading\n\`\`\`\n\n### Provision a user\n\ntext\n\n## Next\n`;
  assert.ok(fencedSectionOf(doc, DOOR_HEADING).includes('### Provision a user'));
  assert.ok(!fencedSectionOf(doc, DOOR_HEADING).includes('## Next'));
  assert.equal(fencedSectionOf(doc, '## Absent'), null);
  assert.ok(
    hasHeadingWithPrefix(
      '### `POST /v1/apps/{app_id}/reset-password` — generate',
      '### `POST /v1/apps/{app_id}/reset-password`',
    ),
  );
  assert.ok(!hasHeadingWithPrefix('```\n### Provision a user\n```', '### Provision a user'));
  const problems = floorProblems(doc, tagMap);
  assert.ok(problems.some((problem) => problem.includes(LOGIN_HEADING)));
  assert.ok(problems.some((problem) => problem.includes('missing heading ### Mint or rotate')));
  assert.ok(problems.some((problem) => problem.includes('route set-password')));
});

test('negative self-test: door example checks fire', () => {
  const check = (body) => checkDoorExamples(sectionDoc(body), tagMap);
  assert.deepEqual(check(block('provision.request', '{ "email": "a@b.co" }')).problems, []);
  assert.equal(check(block('provision.request', '{ "email": "a@b.co" }')).counts.provision, 1);
  assert.match(
    check(block('provision.request', '{ "email": "a@b.co", "x": 1 }')).problems[0],
    /unknown field "x"/,
  );
  assert.match(
    check(block('set-password.request', '{ "email": "a@b.co", "rotate": true }')).problems[0],
    /"rotate"/,
  );
  assert.match(check(block('provision.request', '{ "token": "tk_x" }')).problems[0], /"token"/);
  assert.match(check(block(null, '{ "email": "a@b.co" }')).problems[0], /untagged/);
  assert.match(
    check(block('provision.requset', '{ "email": "a@b.co" }')).problems[0],
    /unknown tag/,
  );
  assert.match(check(block('rotate.request', '{}')).problems[0], /unknown tag/);
  assert.match(check(block('provision.request', '{ "email": ')).problems[0], /not valid JSON/);
  assert.match(
    check(block('provision.request', '{ "email": { "a": 1 } }')).problems.join(),
    /nested value under "email"/,
  );
  assert.match(
    check(block('provision.error', '{ "error": "x", "email": "a" }')).problems[0],
    /"email"/,
  );
  assert.deepEqual(check(block('provision.error', '{ "error": "x" }')).problems, []);

  const elided = check(block('provision.response', '{ "user_id": "u", ... }'));
  assert.deepEqual(elided.problems, []);
  assert.equal(elided.counts.provision, 0);

  const publisher = (inner) => block('ntfy-publisher.response', `{ "publisher": ${inner} }`);
  assert.deepEqual(check(publisher('{ "app_id": "a", "topic_pattern": "a-*" }')).problems, []);
  assert.match(check(publisher('{ "token": "x" }')).problems[0], /unknown publisher field "token"/);
  assert.match(
    check(block('provision.response', '{ "publisher": { "app_id": "a" } }')).problems.join(),
    /"publisher"/,
  );

  const indented = '- item\n\n  ```json provision.request\n  { "email": "a", "bogus": 1 }\n  ```\n';
  assert.match(check(indented).problems[0], /"bogus"/);

  const hidden = '<!-- ```json provision.request\n{ "bogus": 1 }\n``` -->\n';
  assert.deepEqual(check(hidden).problems, []);
});

test('negative self-test: identifier, broadcast, floor and comment checks fire', () => {
  const hex = 'a'.repeat(32);
  assert.deepEqual(identifierProblems(sectionDoc(`id ${hex}`)), [hex]);
  assert.deepEqual(identifierProblems(sectionDoc('no ids')), []);
  assert.ok(identifierProblems(sectionDoc('team.cloudflareaccess.com')).length > 0);

  const broadcast = (body) => `${BROADCAST_HEADING}\n\n${body}\n`;
  assert.deepEqual(broadcastProblems(broadcast('see #174 at issues/174')), []);
  assert.deepEqual(broadcastProblems(broadcast('no link')), ['#174', 'issues/174']);
  assert.deepEqual(broadcastProblems('nothing'), ['broadcast section missing']);

  // A heading hidden in an HTML comment does not satisfy the floor.
  assert.ok(!hasHeadingWithPrefix('<!--\n### Provision a user\n-->', '### Provision a user'));

  // A full doc with one example per route passes the floor; eliding one route's only example fails.
  const bodies = {
    provision: '{ "email": "a@b.co" }',
    deprovision: '{ "person_hash": "x" }',
    'set-password': '{ "email": "a@b.co", "password": "p" }',
    'reset-password': '{ "email": "a@b.co" }',
    'ntfy-publisher': '{ "rotate": true }',
  };
  const fullDoc = (elided) => {
    const examples = Object.entries(bodies)
      .map(([route, body]) => block(`${route}.request`, route === elided ? '{ ... }' : body))
      .join('\n');
    const headings = DOOR_SECTION_HEADINGS.map(({ section, prefix }) => ({ section, prefix }));
    const part = (section) =>
      `${section}\n\n${headings
        .filter((entry) => entry.section === section)
        .map((entry) => `${entry.prefix} x\n`)
        .join('\n')}\n`;
    return `${part(DOOR_HEADING)}\n${examples}\n${part(LOGIN_HEADING)}`;
  };
  assert.deepEqual(floorProblems(fullDoc(null), tagMap), []);
  assert.deepEqual(floorProblems(fullDoc('set-password'), tagMap), [
    'route set-password has no non-elided tagged example',
  ]);

  // A publisher that is not an object fails; a literal ... inside a string does not elide.
  assert.match(
    checkDoorExamples(sectionDoc(block('ntfy-publisher.response', '{ "publisher": "x" }')), tagMap)
      .problems[0],
    /must be an object/,
  );
  assert.match(
    checkDoorExamples(
      sectionDoc(block('provision.request', '{ "email": "a...", "bogus": 1 }')),
      tagMap,
    ).problems[0],
    /"bogus"/,
  );
});

test('negative self-test: structTagsOf resolves a struct and fails on a rename', () => {
  const source =
    'type thing struct {\n\tA string `json:"a"`\n}\n\ntype other struct {\n\tB string `json:"b"`\n}\n';
  assert.deepEqual([...structTagsOf(source, 'thing')], ['a']);
  assert.throws(() => structTagsOf(source, 'renamed'), /not found/);
  assert.ok(tagMap['set-password'].request.has('password'));
  assert.ok(!tagMap['set-password'].request.has('rotate'));
});
