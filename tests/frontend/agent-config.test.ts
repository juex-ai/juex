import assert from "node:assert/strict";
import test from "node:test";

import { resolveConfigSaveFailure } from "../../frontend/src/lib/agent-config.ts";

const persistedConfig = {
  path: "/workspace/.juex/juex.yaml",
  content: "model: local:new\n",
  exists: true,
};

test("config save failure recognizes a persisted update after restart fails", () => {
  assert.deepEqual(
    resolveConfigSaveFailure(
      "model: local:new\n",
      persistedConfig,
      "agent failed to start",
    ),
    {
      config: persistedConfig,
      saved: true,
      message:
        "Config was saved, but the agent could not be restarted: agent failed to start",
    },
  );
});

test("config save failure stays neutral when persistence is not confirmed", () => {
  assert.deepEqual(
    resolveConfigSaveFailure(
      "model: local:new\n",
      { ...persistedConfig, content: "model: local:old\n" },
      "invalid workspace config",
    ),
    {
      config: { ...persistedConfig, content: "model: local:old\n" },
      saved: false,
      message: "Save or restart failed: invalid workspace config",
    },
  );
  assert.equal(
    resolveConfigSaveFailure(
      "model: local:new\n",
      null,
      "request failed",
    ).saved,
    false,
  );
});
