import assert from "node:assert/strict";
import test from "node:test";
import {
  messageContentBaseClassName,
  messageContentRoleClassName,
  messageResponseClassName,
} from "../../frontend/src/lib/message-rendering.ts";

test("messageResponseClassName preserves explicit paragraph newlines", () => {
  assert.match(messageResponseClassName(), /\[&_p\]:whitespace-pre-wrap/);
});

test("user message chrome uses a weak card treatment", () => {
  const base = messageContentBaseClassName();
  const user = messageContentRoleClassName("user");

  assert.match(base, /text-\[14\.5px\]/);
  assert.match(user, /group-\[\.is-user\]:bg-card/);
  assert.match(user, /group-\[\.is-user\]:border-border/);
  assert.match(user, /group-\[\.is-user\]:text-card-foreground/);
  assert.match(user, /group-\[\.is-user\]:ml-auto/);
  assert.doesNotMatch(user, /bg-juex-user/);
  assert.doesNotMatch(user, /text-juex-user-foreground/);
});

test("assistant message chrome keeps card treatment", () => {
  const assistant = messageContentRoleClassName("assistant");

  assert.match(assistant, /group-\[\.is-assistant\]:bg-card/);
  assert.match(assistant, /group-\[\.is-assistant\]:border-border/);
  assert.match(assistant, /group-\[\.is-assistant\]:text-card-foreground/);
});
