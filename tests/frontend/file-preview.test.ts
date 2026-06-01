import assert from "node:assert/strict";
import test from "node:test";
import { getFileRawURL } from "../../frontend/src/api.ts";

test("getFileRawURL encodes workspace paths for image previews", () => {
  assert.equal(
    getFileRawURL("screenshots/space name.png"),
    "/api/files/raw?path=screenshots%2Fspace%20name.png",
  );
});
