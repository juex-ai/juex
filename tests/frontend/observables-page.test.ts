import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const observablesPageSource = readFileSync(
  new URL("../../frontend/src/pages/Observables.tsx", import.meta.url),
  "utf8",
);

test("Observables table title column does not render source icons", () => {
  assert.equal(
    /\bActivity\b|\bCalendarClock\b/.test(observablesPageSource),
    false,
  );
});

test("Observables table uses an accessible full-row link target", () => {
  assert.match(observablesPageSource, /import \{ Link, useNavigate \}/);
  assert.match(
    observablesPageSource,
    /const detailHref = `\/observables\/\$\{encodeURIComponent\(item\.id\)\}`;/,
  );
  assert.match(observablesPageSource, /<Link\s+to=\{detailHref\}/);
  assert.match(
    observablesPageSource,
    /className="absolute inset-0 z-0 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring\/35"/,
  );
  assert.doesNotMatch(observablesPageSource, /role="link"/);
  assert.doesNotMatch(observablesPageSource, /onClick=\{\(\) => onOpen\(item\.id\)\}/);
});

test("Observables table anchors the link overlay to a non-table grid row", () => {
  assert.match(observablesPageSource, /role="table"/);
  assert.match(observablesPageSource, /role="rowgroup"/);
  assert.match(observablesPageSource, /role="columnheader"/);
  assert.match(observablesPageSource, /role="cell"/);
  assert.match(
    observablesPageSource,
    /"group relative grid cursor-pointer border-t transition-colors hover:bg-muted\/35 focus-within:bg-muted\/40"/,
  );
  assert.doesNotMatch(observablesPageSource, /<table\b/);
  assert.doesNotMatch(observablesPageSource, /<tr\b/);
  assert.doesNotMatch(observablesPageSource, /<td\b/);
  assert.doesNotMatch(observablesPageSource, /event\.stopPropagation\(\)/);
});

test("Observables table gives the title column more room", () => {
  assert.match(
    observablesPageSource,
    /"grid-cols-\[24rem_minmax\(8rem,0\.6fr\)_minmax\(18rem,1\.2fr\)_minmax\(18rem,1fr\)_8rem\]"/,
  );
  assert.match(
    observablesPageSource,
    /observableGridColumns/,
  );
});
