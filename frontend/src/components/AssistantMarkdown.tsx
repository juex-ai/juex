import {
  type ComponentProps,
  useEffect,
  useMemo,
  useState,
} from "react";
import { defaultRehypePlugins } from "streamdown";

import { getFileContent, getMediaURL } from "@/api";
import {
  MessageResponse,
  type MessageResponseProps,
} from "@/components/ai-elements/message";
import { ImageBlock } from "@/components/ImageBlock";
import {
  localMarkdownLinkTargets,
  localMarkdownPath,
  mediaRefFromFileContent,
  rewriteLocalMarkdownImages,
} from "@/lib/markdown-media";
import { cn } from "@/lib/utils";
import type { MediaRef } from "@/types";

const emptyMediaByPath: ReadonlyMap<string, MediaRef> = new Map();

export function AssistantMarkdown({ children }: { children: string }) {
  const targetKey = useMemo(
    () => JSON.stringify(localMarkdownLinkTargets(children)),
    [children],
  );
  const resolved = useResolvedLocalImages(targetKey);
  const mediaByPath =
    resolved.targetKey === targetKey ? resolved.mediaByPath : emptyMediaByPath;
  const imagePaths = useMemo(
    () => new Set(mediaByPath.keys()),
    [mediaByPath],
  );
  const mediaRenderKey = useMemo(
    () => JSON.stringify([...imagePaths]),
    [imagePaths],
  );
  const rehypePlugins = useMemo(
    () => {
      const rewrite = [
        rewriteLocalMarkdownImages,
        {
          imagePaths: [...imagePaths],
          mediaURL: absoluteMediaURL,
        },
      ];
      const plugins = Object.entries(defaultRehypePlugins).flatMap(
        ([name, plugin]) => (name === "harden" ? [rewrite, plugin] : [plugin]),
      );
      if (!Object.hasOwn(defaultRehypePlugins, "harden")) plugins.push(rewrite);
      return plugins as MessageResponseProps["rehypePlugins"];
    },
    [imagePaths],
  );
  const components = useMemo<NonNullable<MessageResponseProps["components"]>>(
    () => ({
      img: (props) => (
        <AssistantMarkdownImage {...props} mediaByPath={mediaByPath} />
      ),
    }),
    [mediaByPath],
  );

  return (
    <MessageResponse
      key={mediaRenderKey}
      components={components}
      rehypePlugins={rehypePlugins}
    >
      {children}
    </MessageResponse>
  );
}

type ResolvedLocalImages = {
  targetKey: string;
  mediaByPath: ReadonlyMap<string, MediaRef>;
};

function useResolvedLocalImages(targetKey: string): ResolvedLocalImages {
  const targets = useMemo<string[]>(() => JSON.parse(targetKey), [targetKey]);
  const [resolved, setResolved] = useState<ResolvedLocalImages>({
    targetKey: "",
    mediaByPath: emptyMediaByPath,
  });

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    setResolved({ targetKey, mediaByPath: emptyMediaByPath });

    for (const path of targets) {
      void (async () => {
        try {
          const file = await getFileContent(path, controller.signal);
          const media = mediaRefFromFileContent(path, file);
          if (!active || !media) return;
          setResolved((current) => {
            const mediaByPath =
              current.targetKey === targetKey
                ? new Map(current.mediaByPath)
                : new Map<string, MediaRef>();
            mediaByPath.set(path, media);
            return { targetKey, mediaByPath };
          });
        } catch {
          // Missing, rejected, and non-image local links remain ordinary links.
        }
      })();
    }

    return () => {
      active = false;
      controller.abort();
    };
  }, [targetKey, targets]);

  return resolved;
}

type AssistantMarkdownImageProps = ComponentProps<"img"> & {
  "data-juex-image-block"?: boolean | string;
  mediaByPath: ReadonlyMap<string, MediaRef>;
  node?: unknown;
};

function AssistantMarkdownImage({
  alt,
  className,
  "data-juex-image-block": imageBlock,
  loading,
  mediaByPath,
  node: _node,
  src,
  ...props
}: AssistantMarkdownImageProps) {
  const path = markdownMediaPath(src);
  if (path && imageBlock) {
    return (
      <ImageBlock
        alt={alt}
        media={mediaByPath.get(path) ?? { artifact_path: path }}
      />
    );
  }

  return (
    <img
      {...props}
      alt={alt ?? ""}
      className={cn("my-4 max-w-full rounded-md", className)}
      loading={loading ?? "lazy"}
      src={src}
    />
  );
}

function absoluteMediaURL(path: string): string {
  return new URL(getMediaURL(path), window.location.origin).toString();
}

function markdownMediaPath(src?: string): string | null {
  const directPath = localMarkdownPath(src);
  if (directPath) return directPath;
  if (!src) return null;

  try {
    const url = new URL(src, window.location.origin);
    if (
      url.origin !== window.location.origin ||
      !url.pathname.endsWith("/api/media")
    ) {
      return null;
    }
    return localMarkdownPath(url.searchParams.get("path"));
  } catch {
    return null;
  }
}
