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
    /"absolute inset-0 z-0 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring\/35"/,
  );
  assert.match(observablesPageSource, /tabIndex=\{focusable \? undefined : -1\}/);
  assert.doesNotMatch(observablesPageSource, /role="link"/);
  assert.doesNotMatch(observablesPageSource, /onClick=\{\(\) => onOpen\(item\.id\)\}/);
});

test("Observables table does not anchor the link overlay to the row", () => {
  assert.doesNotMatch(
    observablesPageSource,
    /<tr className="[^"]*\brelative\b[^"]*"/,
  );
  assert.match(
    observablesPageSource,
    /<td className="relative cursor-default px-3 py-2">/,
  );
  assert.doesNotMatch(observablesPageSource, /event\.stopPropagation\(\)/);
});

test("Observables table gives the title column more room", () => {
  assert.match(
    observablesPageSource,
    /<th className="w-\[24rem\] px-3 py-2 font-medium">Observable<\/th>/,
  );
  assert.match(
    observablesPageSource,
    /<td className="relative w-\[24rem\] max-w-\[24rem\] px-3 py-2">/,
  );
});
