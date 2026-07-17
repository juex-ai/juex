import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const observablesPageSource = readFileSync(
  new URL("../../frontend/src/pages/Observables.tsx", import.meta.url),
  "utf8",
);
const observableDetailSource = readFileSync(
  new URL("../../frontend/src/pages/ObservableDetail.tsx", import.meta.url),
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

test("Observables table keeps desktop columns compact and actions visible", () => {
  assert.match(
    observablesPageSource,
    /"grid-cols-\[minmax\(10rem,1\.15fr\)_5\.5rem_minmax\(10rem,1fr\)_minmax\(10rem,1fr\)_8rem\]"/,
  );
  assert.match(observablesPageSource, /observableGridColumns/);
  assert.match(
    observablesPageSource,
    /const observableGridMinWidth = "min-w-\[44rem\]";/,
  );
  assert.match(
    observablesPageSource,
    /className=\{cn\("w-full text-left text-sm", observableGridMinWidth\)\}/,
  );
  assert.doesNotMatch(observablesPageSource, /min-w-\[76rem\]/);
  assert.match(
    observablesPageSource,
    /"sticky right-0 z-20 border-l bg-muted\/95 px-3 py-2 text-right font-medium"/,
  );
  assert.match(
    observablesPageSource,
    /"sticky right-0 z-20 cursor-default border-l bg-card px-3 py-2 group-hover:bg-muted\/35 group-focus-within:bg-muted\/40"/,
  );
});

test("Observables table exposes complete truncated content in a bounded tooltip", () => {
  assert.match(observablesPageSource, /TooltipProvider/);
  assert.match(observablesPageSource, /TooltipTrigger asChild/);
  assert.match(
    observablesPageSource,
    /max-h-64 max-w-md overscroll-contain overflow-y-auto whitespace-normal break-words/,
  );
  assert.match(observablesPageSource, /\{item\.name \|\| item\.id\}/);
  assert.match(observablesPageSource, /\{sourceSummary\(item\)\}/);
  assert.match(observablesPageSource, /\{last\.content \|\| "-"\}/);
  assert.match(observablesPageSource, /aria-keyshortcuts="ArrowUp ArrowDown PageUp PageDown Home End"/);
  assert.match(observablesPageSource, /scrollTooltipContent\(event, tooltipContentRef\.current\)/);
  assert.match(observablesPageSource, /content\.scrollTo\(\{ top: nextTop, behavior: "smooth" \}\)/);
});

test("Schedule rows and details offer a distinct Run action", () => {
  assert.match(observablesPageSource, /import \{[^}]*Zap[^}]*\} from "lucide-react"/);
  assert.match(observablesPageSource, /runObservable/);
  assert.match(observablesPageSource, /item\.source_type === "schedule"/);
  assert.match(observablesPageSource, /aria-label="Run schedule now"/);
  assert.match(observablesPageSource, /onAction\(item\.id, "run"\)/);

  assert.match(observableDetailSource, /import \{[^}]*Zap[^}]*\} from "lucide-react"/);
  assert.match(observableDetailSource, /runObservable/);
  assert.match(observableDetailSource, /observable\?\.source_type === "schedule"/);
  assert.match(observableDetailSource, /void runAction\("run"\)/);
  assert.match(observableDetailSource, />\s*Run\s*<\/Button>/);
  assert.match(
    observableDetailSource,
    /className="flex flex-wrap items-center justify-end gap-1"/,
  );
});
