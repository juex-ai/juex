import assert from "node:assert/strict";
import test from "node:test";
import {
  getFileRawURL,
  getMediaMetadata,
  getMediaURL,
} from "../../frontend/src/api.ts";

test("getFileRawURL encodes workspace paths for image previews", () => {
  assert.equal(
    getFileRawURL("screenshots/space name.png"),
    "/api/files/raw?path=screenshots%2Fspace%20name.png",
  );
});

test("getMediaURL encodes explicit media roots and paths", () => {
	assert.equal(
		getMediaURL("sessions/s/media/image 1.png", "artifact"),
		"/api/media?root=artifact&path=sessions%2Fs%2Fmedia%2Fimage%201.png",
	);
});

test("getMediaMetadata probes image headers without downloading content", async () => {
  const originalFetch = globalThis.fetch;
  const calls: Array<{ input: string; method?: string }> = [];
  globalThis.fetch = (async (
    input: RequestInfo | URL,
    init?: RequestInit,
  ) => {
    calls.push({ input: String(input), method: init?.method });
    return new Response(null, {
      status: 200,
      headers: {
        "Content-Length": "128",
        "Content-Type": "image/png",
      },
    });
  }) as typeof fetch;

  try {
    assert.deepEqual(await getMediaMetadata("screenshots/preview.png"), {
      artifact_path: "screenshots/preview.png",
      media_type: "image/png",
      original_bytes: 128,
    });
  } finally {
    globalThis.fetch = originalFetch;
  }

  assert.deepEqual(calls, [
    {
		input: "/api/media?root=workspace&path=screenshots%2Fpreview.png",
      method: "HEAD",
    },
  ]);
});
