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
  assert.match(observablesPageSource, /<Link\s+to=\{`\/observables\/\$\{encodeURIComponent\(item\.id\)\}`\}/);
  assert.match(
    observablesPageSource,
    /className="absolute inset-0 z-0 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring\/35"/,
  );
  assert.doesNotMatch(observablesPageSource, /role="link"/);
  assert.doesNotMatch(observablesPageSource, /onClick=\{\(\) => onOpen\(item\.id\)\}/);
});

test("Observables table keeps action buttons above the row link", () => {
  assert.match(
    observablesPageSource,
    /<td className="relative z-20 px-3 py-2">/,
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
    /<td className="w-\[24rem\] max-w-\[24rem\] px-3 py-2">/,
  );
});
