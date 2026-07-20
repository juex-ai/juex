import assert from "node:assert/strict";
import test from "node:test";

import {
  localMarkdownLinkTargets,
  localMarkdownPath,
  mediaRefFromFileContent,
  rewriteLocalMarkdownImages,
} from "../../frontend/src/lib/markdown-media.ts";

test("localMarkdownPath accepts workspace-relative paths only", () => {
  assert.equal(localMarkdownPath("screenshots/dream.png"), "screenshots/dream.png");
  assert.equal(localMarkdownPath("./dream%20pink.png"), "./dream pink.png");
  assert.equal(localMarkdownPath("https://example.com/image.png"), null);
  assert.equal(localMarkdownPath("data:image/png;base64,abc"), null);
  assert.equal(localMarkdownPath("//example.com/image.png"), null);
  assert.equal(localMarkdownPath("/tmp/image.png"), null);
  assert.equal(localMarkdownPath("#preview"), null);
  assert.equal(localMarkdownPath("?file=image.png"), null);
  assert.equal(localMarkdownPath("%2Ftmp%2Fimage.png"), null);
});

test("localMarkdownLinkTargets extracts ordinary local links without images", () => {
  const markdown = [
    "[preview](screenshots/dream.png)",
    "[preview again](screenshots/dream.png)",
    "![explicit](screenshots/explicit.png)",
    "[external](https://example.com/image.png)",
    "[encoded](screenshots/dream%20pink.png)",
    "See [inline preview](screenshots/inline.png) here.",
  ].join("\n\n");

  assert.deepEqual(localMarkdownLinkTargets(markdown), [
    "screenshots/dream.png",
    "screenshots/dream pink.png",
  ]);
});

test("mediaRefFromFileContent accepts only backend-confirmed images", () => {
  assert.deepEqual(
    mediaRefFromFileContent("draft.png", {
      path: "screenshots/draft.png",
      content: "",
      kind: "image",
      media_type: "image/png",
      size: 128,
      truncated: false,
    }),
    {
      artifact_path: "screenshots/draft.png",
      media_type: "image/png",
      original_bytes: 128,
    },
  );
  assert.equal(
    mediaRefFromFileContent("draft.png", {
      path: "draft.png",
      content: "not an image",
      kind: "text",
      size: 12,
      truncated: false,
    }),
    null,
  );
});

test("rewriteLocalMarkdownImages converts only standalone confirmed links", () => {
  const tree = {
    type: "root",
    children: [
      {
        type: "element",
        tagName: "p",
        properties: {},
        children: [
          {
            type: "element",
            tagName: "a",
            properties: {
              href: "screenshots/dream.png",
            },
            children: [{ type: "text", value: "Dream pink" }],
          },
        ],
      },
      {
        type: "element",
        tagName: "p",
        properties: {},
        children: [
          { type: "text", value: "See " },
          {
            type: "element",
            tagName: "a",
            properties: {
              href: "screenshots/dream.png",
            },
            children: [{ type: "text", value: "inline preview" }],
          },
          { type: "text", value: " here." },
        ],
      },
      {
        type: "element",
        tagName: "p",
        properties: {},
        children: [
          {
            type: "element",
            tagName: "a",
            properties: {
              href: "notes/readme.md",
            },
            children: [{ type: "text", value: "notes" }],
          },
        ],
      },
    ],
  };

  rewriteLocalMarkdownImages({
    imagePaths: ["screenshots/dream.png"],
    mediaURL: (path) => `/api/media?path=${encodeURIComponent(path)}`,
  })(tree);

  assert.deepEqual(tree.children[0], {
    type: "element",
    tagName: "div",
    properties: {},
    children: [
      {
        type: "element",
        tagName: "img",
        properties: {
          src: "/api/media?path=screenshots%2Fdream.png",
          alt: "Dream pink",
          "data-juex-image-block": true,
        },
        children: [],
      },
    ],
  });
  assert.deepEqual(tree.children[1]?.children?.[1], {
    type: "element",
    tagName: "a",
    properties: {
      href: "screenshots/dream.png",
    },
    children: [{ type: "text", value: "inline preview" }],
  });
  assert.deepEqual(tree.children[2]?.children?.[0], {
    type: "element",
    tagName: "a",
    properties: {
      href: "notes/readme.md",
    },
    children: [{ type: "text", value: "notes" }],
  });
});

test("rewriteLocalMarkdownImages rewrites explicit local images before URL hardening", () => {
  const tree = {
    type: "root",
    children: [
      {
        type: "element",
        tagName: "p",
        properties: {},
        children: [
          {
            type: "element",
            tagName: "img",
            properties: {
              src: "screenshots/explicit.png",
              alt: "Explicit",
            },
            children: [],
          },
        ],
      },
      {
        type: "element",
        tagName: "p",
        properties: {},
        children: [
          { type: "text", value: "See " },
          {
            type: "element",
            tagName: "img",
            properties: {
              src: "screenshots/inline.png",
              alt: "Inline",
            },
            children: [],
          },
          { type: "text", value: " here." },
        ],
      },
      {
        type: "element",
        tagName: "p",
        properties: {},
        children: [
          {
            type: "element",
            tagName: "img",
            properties: {
              src: "https://example.com/external.png",
              alt: "External",
            },
            children: [],
          },
        ],
      },
    ],
  };

  rewriteLocalMarkdownImages({
    mediaURL: (path) => `/api/media?path=${encodeURIComponent(path)}`,
  })(tree);

  assert.equal(tree.children[0]?.tagName, "div");
  assert.equal(
    tree.children[0]?.children?.[0]?.properties?.src,
    "/api/media?path=screenshots%2Fexplicit.png",
  );
  assert.equal(
    tree.children[0]?.children?.[0]?.properties?.["data-juex-image-block"],
    true,
  );
  assert.equal(
    tree.children[1]?.children?.[1]?.properties?.src,
    "/api/media?path=screenshots%2Finline.png",
  );
  assert.equal(
    tree.children[1]?.children?.[1]?.properties?.["data-juex-image-block"],
    undefined,
  );
  assert.equal(
    tree.children[2]?.children?.[0]?.properties?.src,
    "https://example.com/external.png",
  );
});
