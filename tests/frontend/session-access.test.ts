import test from "node:test";
import assert from "node:assert/strict";

import {
  sessionCanSend,
  sessionReadOnlyMessage,
} from "../../frontend/src/lib/session-access.ts";

test("sessionCanSend only allows normal active primary sessions", () => {
  assert.equal(
    sessionCanSend({ kind: "primary", active: true }, { historyMode: false }),
    true,
  );
  assert.equal(
    sessionCanSend({ kind: "primary", active: false }, { historyMode: false }),
    false,
  );
  assert.equal(
    sessionCanSend({ kind: "side", active: false }, { historyMode: false }),
    false,
  );
});

test("sessionCanSend treats history views as read-only even for the active session", () => {
  assert.equal(
    sessionCanSend({ kind: "primary", active: true }, { historyMode: true }),
    false,
  );
});

test("sessionReadOnlyMessage explains the active history case without activation", () => {
  assert.equal(
    sessionReadOnlyMessage(
      { kind: "primary", active: true },
      { historyMode: true },
    ),
    "History view is read-only",
  );
  assert.equal(
    sessionReadOnlyMessage(
      { kind: "primary", active: false },
      { historyMode: false },
    ),
    "Inactive primary session",
  );
  assert.equal(
    sessionReadOnlyMessage({ kind: "side", active: false }, { historyMode: false }),
    "Side session",
  );
});
