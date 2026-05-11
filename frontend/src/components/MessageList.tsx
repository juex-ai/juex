import { useEffect, useLayoutEffect, useRef } from "react";
import type { Message } from "@/types";
import { MessageCard } from "./MessageCard";

export function MessageList({
  messages,
  model,
  scrollRequest,
}: {
  messages: Message[];
  model?: string;
  scrollRequest: ScrollRequest;
}) {
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const shouldStickRef = useRef(true);

  useEffect(() => {
    const scrollElement = scrollRef.current;
    if (!scrollElement) return;

    const updateStickiness = () => {
      shouldStickRef.current = isCloseToBottom(scrollElement);
    };

    updateStickiness();
    scrollElement.addEventListener("scroll", updateStickiness, {
      passive: true,
    });
    return () => {
      scrollElement.removeEventListener("scroll", updateStickiness);
    };
  }, []);

  useLayoutEffect(() => {
    if (scrollRequest.force || shouldStickRef.current) {
      const scrollElement = scrollRef.current;
      if (scrollElement) {
        scrollToBottomEdge(scrollElement);
      }
    }
  }, [scrollRequest]);

  useEffect(() => {
    if (!scrollRequest.force && !shouldStickRef.current) return;
    const scrollElement = scrollRef.current;
    if (!scrollElement) return;

    const frame = window.requestAnimationFrame(() => {
      scrollToBottomEdge(scrollElement);
    });
    return () => {
      window.cancelAnimationFrame(frame);
    };
  }, [scrollRequest]);

  return (
    <div
      ref={scrollRef}
      role="log"
      aria-live="polite"
      className="h-full w-full flex-1 overflow-y-auto"
    >
      <div className="flex w-full flex-col gap-2 px-6 py-3">
        {messages.length === 0 ? (
          <div className="text-muted-foreground p-8 text-center">
            No messages yet.
          </div>
        ) : (
          messages.map((m, i) => (
            <MessageCard key={i} message={m} model={model} />
          ))
        )}
        <div className="h-px w-full shrink-0 scroll-mt-4" aria-hidden="true" />
      </div>
    </div>
  );
}

type ScrollRequest = {
  version: number;
  force: boolean;
};

function isCloseToBottom(scrollElement: HTMLElement) {
  const scrollDifference =
    scrollElement.scrollHeight -
    scrollElement.clientHeight -
    scrollElement.scrollTop;
  return scrollDifference <= 96;
}

function scrollToBottomEdge(scrollElement: HTMLElement) {
  scrollElement.scrollTop = scrollElement.scrollHeight;
}
