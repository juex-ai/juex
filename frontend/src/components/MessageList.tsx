import type { Message } from "@/types";
import { MessageCard } from "./MessageCard";
import {
  ChatContainerRoot,
  ChatContainerContent,
  ChatContainerScrollAnchor,
} from "@/components/prompt-kit/chat-container";

export function MessageList({ messages }: { messages: Message[] }) {
  return (
    <ChatContainerRoot
      aria-live="polite"
      className="h-full w-full flex-1"
    >
      <ChatContainerContent className="gap-3 px-6 py-4">
        {messages.length === 0 ? (
          <div className="text-muted-foreground p-8 text-center">
            No messages yet.
          </div>
        ) : (
          messages.map((m, i) => <MessageCard key={i} message={m} />)
        )}
        <ChatContainerScrollAnchor />
      </ChatContainerContent>
    </ChatContainerRoot>
  );
}
