import { marked } from "marked";

import type { FileContentResponse, MediaRef } from "../types";

type HTMLNode = {
  children?: HTMLNode[];
  properties?: Record<string, unknown>;
  tagName?: string;
  type?: string;
  value?: string;
};

export function localMarkdownPath(value?: string | null): string | null {
  const raw = value?.trim();
  if (!raw || raw.startsWith("/") || raw.startsWith("//")) return null;
  if (raw.startsWith("#") || raw.startsWith("?") || hasURLScheme(raw)) {
    return null;
  }

  try {
    const decoded = decodeURIComponent(raw);
    if (
      !decoded ||
      decoded.startsWith("/") ||
      decoded.startsWith("//") ||
      hasURLScheme(decoded)
    ) {
      return null;
    }
    return decoded;
  } catch {
    return null;
  }
}

export function localMarkdownLinkTargets(markdown: string): string[] {
  if (!markdown.trim()) return [];

  const targets: string[] = [];
  const seen = new Set<string>();
  try {
    marked.walkTokens(marked.lexer(markdown), (token) => {
      if (token.type !== "link") return;
      const path = localMarkdownPath(token.href);
      if (!path || seen.has(path)) return;
      seen.add(path);
      targets.push(path);
    });
  } catch {
    return [];
  }
  return targets;
}

export function mediaRefFromFileContent(
  requestedPath: string,
  file: FileContentResponse,
): MediaRef | null {
  if (file.kind !== "image") return null;
  return {
    artifact_path: file.path || requestedPath,
    media_type: file.media_type,
    original_bytes: file.size,
  };
}

export type RewriteLocalMarkdownImagesOptions = {
  imagePaths?: readonly string[];
  mediaURL?: (path: string) => string;
};

export function rewriteLocalMarkdownImages(
  options: RewriteLocalMarkdownImagesOptions = {},
) {
  const imagePaths = new Set(options.imagePaths ?? []);
  const mediaURL = options.mediaURL ?? ((path: string) => path);
  return (tree: HTMLNode) => {
    rewriteLocalImages(tree, imagePaths, mediaURL);
  };
}

function rewriteLocalImages(
  node: HTMLNode,
  imagePaths: ReadonlySet<string>,
  mediaURL: (path: string) => string,
) {
  if (node.type === "element" && node.properties) {
    if (node.tagName === "img") {
      const path = stringProperty(node.properties.src);
      const localPath = localMarkdownPath(path);
      if (localPath) node.properties.src = mediaURL(localPath);
    }
    if (node.tagName === "a") {
      const path = localMarkdownPath(stringProperty(node.properties.href));
      if (path && imagePaths.has(path)) {
        const title = node.properties.title;
        node.tagName = "img";
        node.properties = {
          src: mediaURL(path),
          alt: htmlNodeText(node),
          ...(typeof title === "string" ? { title } : {}),
        };
        node.children = [];
      }
    }
  }
  for (const child of node.children ?? []) {
    rewriteLocalImages(child, imagePaths, mediaURL);
  }
}

function htmlNodeText(node: HTMLNode): string {
  if (typeof node.value === "string") return node.value;
  return node.children?.map(htmlNodeText).join("") ?? "";
}

function stringProperty(value: unknown): string | null {
  return typeof value === "string" ? value : null;
}

function hasURLScheme(value: string): boolean {
  return /^[a-z][a-z\d+.-]*:/i.test(value);
}
