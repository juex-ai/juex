import assert from "node:assert/strict";
import test from "node:test";

import { createDirectory } from "../../frontend/src/api.ts";

test("createDirectory posts the captured parent and trimmed name", async () => {
  const originalFetch = globalThis.fetch;
  let capturedInput: RequestInfo | URL | undefined;
  let capturedInit: RequestInit | undefined;
  globalThis.fetch = (async (
    input: RequestInfo | URL,
    init?: RequestInit,
  ) => {
    capturedInput = input;
    capturedInit = init;
    return new Response(
      JSON.stringify({
        name: "workspace",
        path: "/work/workspace",
        registered: false,
      }),
      { status: 201, headers: { "Content-Type": "application/json" } },
    );
  }) as typeof fetch;

  try {
    const created = await createDirectory({
      parent: "/work",
      name: "workspace",
    });
    assert.equal(capturedInput, "/api/fs/dirs");
    assert.equal(capturedInit?.method, "POST");
    assert.equal(
      (capturedInit?.headers as Record<string, string>)["Content-Type"],
      "application/json",
    );
    assert.deepEqual(JSON.parse(String(capturedInit?.body)), {
      parent: "/work",
      name: "workspace",
    });
    assert.deepEqual(created, {
      name: "workspace",
      path: "/work/workspace",
      registered: false,
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
});
