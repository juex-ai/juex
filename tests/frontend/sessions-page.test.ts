import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const sessionsSource = readFileSync(
  new URL("../../frontend/src/pages/Sessions.tsx", import.meta.url),
  "utf8",
);
const sessionSource = readFileSync(
  new URL("../../frontend/src/pages/Session.tsx", import.meta.url),
  "utf8",
);

test("landing-page commands carry their submission time into the session route", () => {
  const submitStart = sessionsSource.indexOf("onSubmit={async (msg) => {");
  const submitEnd = sessionsSource.indexOf("catch (e)", submitStart);
  assert.notEqual(submitStart, -1);
  assert.notEqual(submitEnd, -1);
  const submit = sessionsSource.slice(submitStart, submitEnd);

  assert.match(submit, /const submittedAt = new Date\(\)\.toISOString\(\)/);
  assert.ok(
    submit.indexOf("const submittedAt") < submit.indexOf("await startTurn"),
  );
  assert.match(
    submit,
    /\{[\s\S]*?commandInput: text,[\s\S]*?command: turn\.command,[\s\S]*?submittedAt,[\s\S]*?\}/,
  );
  assert.match(
    sessionSource,
    /projectInitialCommandOnce\([\s\S]*?state\.commandInput,[\s\S]*?state\.command,[\s\S]*?state\.submittedAt,[\s\S]*?\)/,
  );
});
