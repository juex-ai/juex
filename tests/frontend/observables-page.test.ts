import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const observablesPageSource = readFileSync(
  new URL("../../frontend/src/pages/Observables.tsx", import.meta.url),
  "utf8",
);

test("Observables table title column does not render source icons", () => {
  assert.equal(
    /\bActivity\b|\bCalendarClock\b|<Link\b/.test(observablesPageSource),
    false,
  );
});

test("Observables table uses the row as the detail navigation target", () => {
  assert.match(observablesPageSource, /role="link"/);
  assert.match(observablesPageSource, /tabIndex=\{0\}/);
  assert.match(observablesPageSource, /onClick=\{\(\) => onOpen\(item\.id\)\}/);
  assert.match(
    observablesPageSource,
    /if \(event\.target !== event\.currentTarget\) return;/,
  );
});

test("Observables table action buttons do not trigger row navigation", () => {
  const stopPropagationCount =
    observablesPageSource.match(/event\.stopPropagation\(\)/g)?.length ?? 0;

  assert.equal(stopPropagationCount, 3);
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
