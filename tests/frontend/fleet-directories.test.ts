import assert from "node:assert/strict";
import test from "node:test";

import {
  directoryCreateKeyAction,
  mergeCreatedDirectory,
  revealScrollableTail,
  shouldApplyDirectoryCreateResult,
  validateNewDirectoryName,
  workspacePathUpdate,
} from "../../frontend/src/lib/fleet-directories.ts";
import type {
  DirectoryEntry,
  DirectoryListing,
} from "../../frontend/src/types.ts";

const listing: DirectoryListing = {
  path: "/work",
  parent: "/",
  dirs: [
    { name: "alpha", path: "/work/alpha", registered: false },
    { name: "zeta", path: "/work/zeta", registered: true },
  ],
};

test("new directory validation rejects blank duplicate invalid and hidden names", () => {
  assert.equal(
    validateNewDirectoryName(listing, "   ", false).error,
    "Directory name is required.",
  );
  assert.equal(
    validateNewDirectoryName(listing, "alpha", false).error,
    "A directory named alpha already exists.",
  );
  for (const name of [".", "..", "a/b", String.raw`a\b`, "a\u0000b"]) {
    assert.equal(
      validateNewDirectoryName(listing, name, true).error,
      "Directory name must be one path component.",
    );
  }
  assert.equal(
    validateNewDirectoryName(listing, ".hidden", false).error,
    "Turn on Show hidden to create a hidden directory.",
  );
  assert.deepEqual(validateNewDirectoryName(listing, "  .hidden  ", true), {
    name: ".hidden",
    error: null,
  });
});

test("created directories merge only into their captured parent in sorted order", () => {
  const created: DirectoryEntry = {
    name: "middle",
    path: "/work/middle",
    registered: false,
  };
  const merged = mergeCreatedDirectory(listing, "/work", created);
  assert.deepEqual(
    merged.dirs.map((entry) => entry.name),
    ["alpha", "middle", "zeta"],
  );
  assert.equal(
    mergeCreatedDirectory(listing, "/other", created),
    listing,
    "late responses must not merge into another listing",
  );
});

test("request generation rejects late create results after cancel close or reopen", () => {
  const valid = {
    requestGeneration: 4,
    currentGeneration: 4,
    capturedParent: "/work",
    currentParent: "/work",
    dialogOpen: true,
    draftOpen: true,
  };
  assert.equal(shouldApplyDirectoryCreateResult(valid), true);
  for (const stale of [
    { ...valid, currentGeneration: 5 },
    { ...valid, currentParent: "/other" },
    { ...valid, dialogOpen: false },
    { ...valid, draftOpen: false },
  ]) {
    assert.equal(shouldApplyDirectoryCreateResult(stale), false);
  }
});

test("directory keyboard actions and path-tail policy are explicit", () => {
  assert.equal(directoryCreateKeyAction("Enter"), "create");
  assert.equal(directoryCreateKeyAction("Escape"), "cancel");
  assert.equal(directoryCreateKeyAction("Tab"), null);
  assert.deepEqual(workspacePathUpdate("/very/long/path", "browser"), {
    path: "/very/long/path",
    revealTail: true,
  });
  assert.deepEqual(workspacePathUpdate("/typed/path", "manual"), {
    path: "/typed/path",
    revealTail: false,
  });

  const scrollTarget = { scrollLeft: 0, scrollWidth: 640 };
  revealScrollableTail(scrollTarget);
  assert.equal(scrollTarget.scrollLeft, 640);
  revealScrollableTail(null);
});
