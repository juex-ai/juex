import { useEffect, useLayoutEffect, useState } from "react";
import {
  ImagePlusIcon,
  LoaderCircleIcon,
  SendHorizontalIcon,
  SquareIcon,
  XIcon,
} from "lucide-react";

import { AgentRuntimeStateBar } from "@/components/fleet/AgentRuntimeStateBar";
import {
  PromptInput,
  PromptInputButton,
  PromptInputFooter,
  PromptInputSubmit,
  PromptInputTextarea,
  PromptInputTools,
  usePromptInputAttachments,
} from "@/components/ai-elements/prompt-input";
import { QueuedInputStack } from "@/components/QueuedInputStack";
import { SessionStatusPanel } from "@/components/session/SessionStatusPanel";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  QUEUE_FULL_SUBMIT_HINT,
  composerErrorMessage,
  composerSubmitAction,
  settleSubmittedComposerText,
  type ComposerSubmitAction,
} from "@/lib/composer-submit";
import { sessionComposerClearance } from "@/lib/conversation-scroll";
import type { LiveSessionProjection } from "@/lib/live-session-projection";
import { sessionReadOnlyMessage } from "@/lib/session-access";
import { cn } from "@/lib/utils";
import type {
  ActiveContextSnapshot,
  AgentRuntimeStatusSnapshot,
  MediaRef,
  SessionShowResponse,
} from "@/types";

type PromptAttachmentFile = {
  filename?: string;
  mediaType?: string;
  url: string;
};

export function SessionComposer({
  activeContext,
  agentRuntimeHealthy,
  canSend,
  composerHint,
  data,
  onClearanceChange,
  onInterrupt,
  onPromptInput,
  onSend,
  onShowHint,
  onUploadAttachment,
  queuedInputs,
  runtimeStatus,
  submitError,
}: {
  activeContext?: ActiveContextSnapshot | null;
  agentRuntimeHealthy: boolean;
  canSend: boolean;
  composerHint: string | null;
  data: SessionShowResponse;
  onClearanceChange: (clearance: number) => void;
  onInterrupt: () => void;
  onPromptInput: () => void;
  onSend: (prompt: string, attachments: MediaRef[]) => Promise<boolean>;
  onShowHint: (message: string) => void;
  onUploadAttachment: (file: File) => Promise<MediaRef>;
  queuedInputs: LiveSessionProjection["queuedInput"]["items"];
  runtimeStatus?: AgentRuntimeStatusSnapshot;
  submitError: string | null;
}) {
  const [draft, setDraft] = useState("");
  const [attachmentCount, setAttachmentCount] = useState(0);
  const [overlayNode, setOverlayNode] = useState<HTMLDivElement | null>(null);

  useLayoutEffect(() => {
    if (!canSend || !overlayNode) {
      onClearanceChange(0);
      return;
    }
    const measure = () => {
      const height = Math.ceil(overlayNode.getBoundingClientRect().height);
      onClearanceChange(sessionComposerClearance(height));
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(overlayNode);
    return () => observer.disconnect();
  }, [canSend, onClearanceChange, overlayNode]);

  const submitAction = composerSubmitAction({
    status: runtimeStatus,
    text: draft,
    attachmentCount,
  });
  const composerError = composerErrorMessage({
    status: runtimeStatus,
    localError: submitError ?? undefined,
  });

  if (!canSend) {
    return (
      <div className="shrink-0 px-4 py-3 md:px-6">
        <div className="mx-auto w-full max-w-[760px]">
          <QueuedInputStack items={queuedInputs} />
          {!agentRuntimeHealthy ? (
            <AgentRuntimeStateBar />
          ) : (
            <ReadOnlySessionBar data={data} />
          )}
        </div>
      </div>
    );
  }

  return (
    <div
      className="pointer-events-none absolute inset-0 z-20 flex items-end"
      data-testid="session-composer-overlay"
    >
      <div className="flex max-h-full w-full flex-col overflow-visible px-4 md:px-6">
        <div
          className="pointer-events-none relative mx-auto flex min-h-0 w-full max-w-[760px] flex-col pb-[max(0.75rem,env(safe-area-inset-bottom))] md:pb-[max(1.25rem,env(safe-area-inset-bottom))]"
          data-testid="session-composer-obstruction"
          ref={setOverlayNode}
        >
          <div
            data-testid="session-composer-fade"
            className="pointer-events-none absolute inset-x-0 -top-12 h-12 bg-linear-to-b from-transparent to-background/95"
            aria-hidden="true"
          />
          <div
            className="pointer-events-auto flex min-h-0 flex-col overflow-hidden"
            data-testid="session-composer-stack"
          >
            <QueuedInputStack items={queuedInputs} />
            <PromptInput
              accept="image/*"
              className="shrink-0"
              maxFileSize={10 * 1024 * 1024}
              maxFiles={8}
              multiple
              onError={(error) => onShowHint(error.message)}
              onSubmit={async (message) => {
                if (submitAction === "loading") {
                  throw new Error("Loading session status");
                }
                if (submitAction === "queue-full") {
                  throw new Error(QUEUE_FULL_SUBMIT_HINT);
                }
                const submittedText = message.text ?? "";
                const text = submittedText.trim();
                const files = message.files ?? [];
                if (!text && files.length === 0) {
                  onShowHint("Enter a message or attach an image");
                  return;
                }
                const attachments = await uploadPromptAttachments(
                  files,
                  onUploadAttachment,
                );
                const sent = await onSend(text, attachments);
                if (!sent) {
                  throw new Error("start turn failed");
                }
                setDraft((current) =>
                  settleSubmittedComposerText(current, submittedText),
                );
              }}
            >
              <ComposerAttachmentStrip onCountChange={setAttachmentCount} />
              <PromptInputTextarea
                className="max-h-[min(12rem,30dvh)]"
                onChange={(event) => {
                  setDraft(event.currentTarget.value);
                  onPromptInput();
                }}
                placeholder="Ask juex anything..."
              />
              {composerHint || composerError ? (
                <div className="border-t border-border/60 px-2.5 py-1.5">
                  {composerError ? (
                    <ComposerFeedback tone="error">
                      {composerError}
                    </ComposerFeedback>
                  ) : composerHint ? (
                    <ComposerFeedback tone="hint">
                      {composerHint}
                    </ComposerFeedback>
                  ) : null}
                </div>
              ) : null}
              <PromptInputFooter className="flex-nowrap items-end gap-2">
                <TooltipProvider>
                  <PromptInputTools className="min-w-0 flex-1 flex-wrap gap-2">
                    <div
                      className="flex shrink-0 items-center gap-1"
                      aria-label="Composer actions"
                      role="group"
                    >
                      <ComposerAttachmentButton />
                    </div>
                    <Separator
                      className="h-4 self-center"
                      orientation="vertical"
                      decorative
                    />
                    <div
                      className="flex min-w-0 items-center gap-1"
                      aria-label="Session status"
                      role="group"
                    >
                      <SessionStatusPanel
                        activeContext={activeContext}
                        data={data}
                        runtimeStatus={runtimeStatus}
                      />
                    </div>
                  </PromptInputTools>
                  <div className="flex shrink-0 items-center gap-1">
                    <ComposerSubmitButton
                      action={submitAction}
                      onEmpty={() =>
                        onShowHint("Enter a message or attach an image")
                      }
                      onQueueFull={() => onShowHint(QUEUE_FULL_SUBMIT_HINT)}
                      onStop={onInterrupt}
                    />
                  </div>
                </TooltipProvider>
              </PromptInputFooter>
            </PromptInput>
          </div>
        </div>
      </div>
    </div>
  );
}

async function uploadPromptAttachments(
  files: PromptAttachmentFile[],
  upload: (file: File) => Promise<MediaRef>,
): Promise<MediaRef[]> {
  return Promise.all(
    files.map(async (file) => upload(await filePartToFile(file))),
  );
}

async function filePartToFile(part: PromptAttachmentFile): Promise<File> {
  const response = await fetch(part.url);
  if (!response.ok) {
    throw new Error("Unable to read attached image");
  }
  const blob = await response.blob();
  const type = part.mediaType || blob.type || "application/octet-stream";
  const name = part.filename || "image";
  return new File([blob], name, { type });
}

function ComposerAttachmentButton() {
  const attachments = usePromptInputAttachments();
  return (
    <PromptInputButton
      aria-label="Attach images"
      onClick={() => attachments.openFileDialog()}
      tooltip="Attach images"
    >
      <ImagePlusIcon className="size-4" aria-hidden="true" />
    </PromptInputButton>
  );
}

function ComposerAttachmentStrip({
  onCountChange,
}: {
  onCountChange: (count: number) => void;
}) {
  const attachments = usePromptInputAttachments();
  const files = attachments.files;
  useEffect(() => {
    onCountChange(files.length);
  }, [files.length, onCountChange]);

  if (files.length === 0) return null;
  return (
    <ul
      aria-label="Attached images"
      className="flex max-h-[min(10.5rem,24dvh)] w-full flex-wrap items-start justify-start gap-2 overflow-y-auto overscroll-contain px-2.5 pt-2"
    >
      {files.map((file) => (
        <li
          key={file.id}
          className="relative size-20 shrink-0 overflow-hidden rounded-md border border-border/70 bg-muted"
        >
          <img
            src={file.url}
            alt={file.filename ?? "attached image"}
            className="size-full object-cover"
          />
          <Button
            aria-label={`Remove ${file.filename ?? "attached image"}`}
            className="absolute right-1 top-1 size-6 rounded-full bg-foreground text-background shadow-[var(--shadow-xs)] hover:bg-foreground/80 hover:text-background"
            onClick={() => attachments.remove(file.id)}
            size="icon"
            type="button"
            variant="ghost"
          >
            <XIcon className="size-3.5" aria-hidden="true" />
          </Button>
        </li>
      ))}
    </ul>
  );
}

function ComposerSubmitButton({
  action,
  onEmpty,
  onQueueFull,
  onStop,
}: {
  action: ComposerSubmitAction;
  onEmpty: () => void;
  onQueueFull: () => void;
  onStop: () => void;
}) {
  const isLoading = action === "loading";
  const isEmpty = action === "empty";
  const isQueueFull = action === "queue-full";
  const isStop = action === "stop";
  const tooltip =
    action === "loading"
      ? "Loading session status"
      : action === "empty"
        ? "Enter a message or attach an image"
        : action === "queue-full"
          ? QUEUE_FULL_SUBMIT_HINT
          : action === "stop"
            ? "Stop current turn"
            : action === "queue"
              ? "Queue message"
              : "Send message";
  const ariaLabel =
    action === "loading"
      ? "Loading session status"
      : action === "empty"
        ? "Enter a message or attach an image before sending"
        : action === "queue-full"
          ? QUEUE_FULL_SUBMIT_HINT
          : action === "stop"
            ? "Stop current turn"
            : action === "queue"
              ? "Queue message"
              : "Send message";

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <PromptInputSubmit
          aria-disabled={isLoading || isEmpty || isQueueFull}
          aria-label={ariaLabel}
          disabled={isLoading}
          className={cn(
            (isLoading || isEmpty || isQueueFull) &&
              "cursor-not-allowed opacity-50",
          )}
          onClick={(event) => {
            if (isEmpty) {
              event.preventDefault();
              onEmpty();
              return;
            }
            if (isQueueFull) {
              event.preventDefault();
              onQueueFull();
              return;
            }
            if (isStop) {
              event.preventDefault();
              onStop();
            }
          }}
          type={
            isLoading || isEmpty || isQueueFull || isStop ? "button" : "submit"
          }
        >
          {isLoading ? (
            <LoaderCircleIcon
              className="size-4 animate-spin motion-reduce:animate-none"
              aria-hidden="true"
            />
          ) : isStop ? (
            <SquareIcon className="size-4" aria-hidden="true" />
          ) : (
            <SendHorizontalIcon className="size-4" aria-hidden="true" />
          )}
        </PromptInputSubmit>
      </TooltipTrigger>
      <TooltipContent>{tooltip}</TooltipContent>
    </Tooltip>
  );
}

function ComposerFeedback({
  children,
  tone,
}: {
  children: string;
  tone: "hint" | "error";
}) {
  return (
    <div
      className={cn(
        "min-w-0 break-words font-mono text-[11px]",
        tone === "error" ? "text-juex-error" : "text-muted-foreground",
      )}
      role={tone === "error" ? "alert" : "status"}
      aria-live={tone === "error" ? "assertive" : "polite"}
    >
      {children}
    </div>
  );
}

function ReadOnlySessionBar({ data }: { data: SessionShowResponse }) {
  return (
    <div className="flex min-h-[52px] flex-wrap items-center gap-3 rounded-md border bg-muted/50 px-3 py-2 text-sm">
      <div className="min-w-0 flex-1 text-muted-foreground">
        {sessionReadOnlyMessage(data)}
      </div>
    </div>
  );
}
