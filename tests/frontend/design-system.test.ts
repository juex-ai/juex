import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { extname, join } from "node:path";
import test from "node:test";

const frontendRoot = new URL("../../frontend/src/", import.meta.url);

function source(path: string): string {
  return readFileSync(new URL(path, import.meta.url), "utf8");
}

function productionSources(directory: URL): Array<[string, string]> {
  const rows: Array<[string, string]> = [];
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const path = new URL(entry.name, directory);
    if (entry.isDirectory()) {
      rows.push(...productionSources(new URL(`${entry.name}/`, directory)));
      continue;
    }
    if (![".css", ".ts", ".tsx"].includes(extname(entry.name))) continue;
    rows.push([
      join(directory.pathname, entry.name),
      readFileSync(path, "utf8"),
    ]);
  }
  return rows;
}

const cssSource = source("../../frontend/src/index.css");
const dialogSource = source("../../frontend/src/components/ui/dialog.tsx");
const buttonSource = source("../../frontend/src/components/ui/button.tsx");
const inputSource = source("../../frontend/src/components/ui/input.tsx");

test("the design system uses a restrained radius scale with conversational exceptions", () => {
  for (const declaration of [
    "--radius-sm: 2px",
    "--radius-md: 4px",
    "--radius-lg: 6px",
    "--radius-xl: 8px",
  ]) {
    assert.match(cssSource, new RegExp(declaration));
  }
  assert.match(cssSource, /--radius: 6px/);

  const forbidden = /rounded-(?:xl|2xl|3xl|4xl)!?|rounded-\[(?:1[0-9]|[2-9][0-9])px\]/g;
  const oversized = productionSources(frontendRoot)
    .flatMap(([path, contents]) =>
      [...contents.matchAll(forbidden)].map(
        (match) =>
          `${path.slice(frontendRoot.pathname.length)}: ${match[0]}`,
      ),
    )
    .sort();
  assert.deepEqual(oversized, [
    "components/QueuedInputStack.tsx: rounded-[16px]",
    "components/ai-elements/prompt-input.tsx: rounded-[16px]",
    "lib/message-rendering.ts: rounded-[16px]",
  ]);
});

test("shared controls and dialogs use the same compact geometry", () => {
  assert.match(buttonSource, /rounded-md/);
  assert.doesNotMatch(buttonSource, /rounded-\[(?:min|calc|10px|12px)/);
  assert.match(inputSource, /rounded-md/);
  assert.match(dialogSource, /rounded-lg/);
  assert.doesNotMatch(
    dialogSource,
    /rounded-b-[^\s"]+ border-t bg-muted\/50/,
    "dialog actions must not sit inside a rounded muted footer band",
  );
});

test("production UI uses semantic status colors", () => {
  const forbidden = /(?:emerald|amber)-[0-9]+/g;
  const violations = productionSources(frontendRoot)
    .flatMap(([path, contents]) =>
      [...contents.matchAll(forbidden)].map((match) => `${path}: ${match[0]}`),
    );
  assert.deepEqual(violations, []);
  for (const token of [
    "--status-success",
    "--status-warning",
    "--status-working",
  ]) {
    assert.match(cssSource, new RegExp(token));
  }
});
