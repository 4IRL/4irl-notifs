// @vitest-environment node
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

import { screenshots } from './screenshots.ts';

// The manifest gate: every screenshot the page references must exist under public/,
// be a real PNG whose IHDR dimensions match the manifest's width/height (the <img>
// reserves layout space from those numbers), and stay small enough for a phone on
// mobile data. It fails with ENOENT when a file is missing.

const MAX_BYTES = 200 * 1024;
const MAX_WIDTH = 720;
const PNG_SIGNATURE = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a];
const IHDR_TYPE_OFFSET = 12;
const IHDR_WIDTH_OFFSET = 16;
const IHDR_HEIGHT_OFFSET = 20;

function publicPath(src: string): string {
  // `src` is a site-root path (`/screenshots/x.png`); the file lives in `public/`.
  return fileURLToPath(new URL(`../public${src}`, import.meta.url));
}

describe('screenshot manifest', () => {
  it('lists every PNG under public/screenshots/ and nothing else', () => {
    const onDisk = readdirSync(publicPath('/screenshots')).sort();
    const inManifest = Object.values(screenshots)
      .map((asset) => asset.src.replace('/screenshots/', ''))
      .sort();
    expect(inManifest.length).toBeGreaterThan(0);
    expect(onDisk).toEqual(inManifest);
  });

  it('has exactly the expected keys', () => {
    expect(Object.keys(screenshots).sort()).toEqual([
      'androidAddUser',
      'iosAddUser',
      'iosDefaultServer',
    ]);
  });

  it('gives every entry non-empty, distinct alt text and positive integer dimensions', () => {
    const alts = Object.values(screenshots).map((asset) => asset.alt);
    for (const alt of alts) {
      expect(alt.trim().length).toBeGreaterThan(0);
    }
    expect(new Set(alts).size).toBe(alts.length);
    for (const { width, height } of Object.values(screenshots)) {
      expect(Number.isInteger(width) && width > 0).toBe(true);
      expect(Number.isInteger(height) && height > 0).toBe(true);
    }
  });

  it.each(Object.entries(screenshots))(
    '%s points at a same-origin /screenshots/ path',
    (_key, asset) => {
      expect(asset.src).toMatch(/^\/screenshots\/[a-z0-9-]+\.png$/);
    },
  );

  it.each(Object.entries(screenshots))(
    '%s file is a valid PNG with the manifest dimensions',
    (_key, asset) => {
      const bytes = readFileSync(publicPath(asset.src));
      expect([...bytes.subarray(0, PNG_SIGNATURE.length)]).toEqual(PNG_SIGNATURE);
      expect(bytes.subarray(IHDR_TYPE_OFFSET, IHDR_TYPE_OFFSET + 4).toString('ascii')).toBe('IHDR');
      expect(bytes.readUInt32BE(IHDR_WIDTH_OFFSET)).toBe(asset.width);
      expect(bytes.readUInt32BE(IHDR_HEIGHT_OFFSET)).toBe(asset.height);
    },
  );

  it.each(Object.entries(screenshots))(
    '%s file is at most 200 KB and 720px wide',
    (_key, asset) => {
      expect(statSync(publicPath(asset.src)).size).toBeLessThanOrEqual(MAX_BYTES);
      expect(asset.width).toBeLessThanOrEqual(MAX_WIDTH);
    },
  );
});
