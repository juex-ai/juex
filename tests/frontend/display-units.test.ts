import assert from "node:assert/strict";
import test from "node:test";

import { messagesToGroups } from "../../frontend/src/lib/display-units.ts";
import {
  messageGroupCanCopy,
  messageGroupCopyText,
} from "../../frontend/src/lib/message-copy.ts";
import type { Message } from "../../frontend/src/types.ts";

test("messagesToGroups normalizes legacy text blocks without text", () => {
  const messages = [
    {
      id: "legacy-empty-text",
      role: "assistant",
      blocks: [{ type: "text" }],
    },
  ] as unknown as Message[];

  const groups = messagesToGroups(messages);

  assert.equal(groups.length, 1);
  assert.equal(groups[0].units.length, 1);
  assert.deepEqual(groups[0].units[0], {
    kind: "text",
    block: { type: "text", text: "" },
  });
  assert.equal(messageGroupCopyText(groups[0]), "");
  assert.equal(messageGroupCanCopy(groups[0]), false);
});
