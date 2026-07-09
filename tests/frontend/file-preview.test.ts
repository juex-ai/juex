import assert from "node:assert/strict";
import test from "node:test";
import { getFileRawURL, getMediaURL } from "../../frontend/src/api.ts";

test("getFileRawURL encodes workspace paths for image previews", () => {
  assert.equal(
    getFileRawURL("screenshots/space name.png"),
    "/api/files/raw?path=screenshots%2Fspace%20name.png",
  );
});

test("getMediaURL encodes transcript media paths", () => {
  assert.equal(
    getMediaURL(".juex/artifacts/media/s/image 1.png"),
    "/api/media?path=.juex%2Fartifacts%2Fmedia%2Fs%2Fimage%201.png",
  );
});
