import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

function source(path: string): string {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

const makefileSource = source("../../Makefile");
const ciSource = source("../../.github/workflows/ci.yml");
const agentsSource = source("../../AGENTS.md");
const packageSource = source("../../frontend/package.json");

test("the frontend gate has one local entry point for all required checks", () => {
  assert.match(makefileSource, /\.PHONY:[^\n]*\bweb-check\b/);
  assert.match(
    makefileSource,
    /web-check:\n\tcd frontend && pnpm install --frozen-lockfile\n\tcd frontend && pnpm exec tsc -b\n\tcd frontend && pnpm test\n\tcd frontend && pnpm lint\n\tcd frontend && pnpm build/,
  );
  assert.match(
    makefileSource,
    /web-check\s+install, type-check, test, lint, and build the frontend/,
  );
});

test("CI runs the frontend gate separately without slowing Go jobs", () => {
  assert.match(
    ciSource,
    /frontend:\n\s+if: github\.event_name != 'workflow_dispatch'\n\s+runs-on: ubuntu-latest[\s\S]*uses: pnpm\/action-setup@v4[\s\S]*version: 11\.6\.0[\s\S]*uses: actions\/setup-node@v4[\s\S]*node-version: 24[\s\S]*cache: pnpm[\s\S]*cache-dependency-path: frontend\/pnpm-lock\.yaml[\s\S]*run: make web-check/,
  );
  assert.equal(
    ciSource.match(/Prepare embedded web dist/g)?.length,
    2,
    "the existing Go jobs must keep using the lightweight embedded-web stub",
  );
  assert.equal(
    ciSource.match(/run: make web-stub/g)?.length,
    2,
    "CI and local candidate verification must share the web-stub target",
  );
});

test("frontend tool versions and verification authority stay explicit", () => {
  const packageJSON = JSON.parse(packageSource) as {
    packageManager?: string;
  };
  assert.equal(packageJSON.packageManager, "pnpm@11.6.0");
  assert.match(
    agentsSource,
    /The authoritative verification workflow is\s+`\.agents\/skills\/juex-localtest\/SKILL\.md`\./,
  );
  assert.match(agentsSource, /Visible Web\s+changes also require browser verification\./);
});
