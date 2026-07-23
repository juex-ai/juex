import {
  createElement,
  type ComponentType,
  type ReactNode,
  useEffect,
  useMemo,
  useState,
} from "react";
import {
  CheckIcon,
  ChevronRightIcon,
  CircleAlertIcon,
  CopyIcon,
  LoaderCircleIcon,
  RadioIcon,
} from "lucide-react";

import { AssistantMarkdown } from "@/components/AssistantMarkdown";
import { ImageBlock } from "@/components/ImageBlock";
import {
  Message,
  MessageAction,
  MessageActions,
  MessageContent,
  MessageResponse,
} from "@/components/ai-elements/message";
import { Separator } from "@/components/ui/separator";
import {
  assistantWorkTitle,
  type AssistantWorkItem,
  type TranscriptItem,
} from "@/lib/assistant-work-groups";
import { writeClipboardText } from "@/lib/clipboard";
import {
  LOCAL_COMPACT_PENDING_KIND,
  PENDING_COMPACT_LABEL,
} from "@/lib/compact-ui";
import {
  toolState,
  type MessageGroup,
  type ToolDisplayUnit,
} from "@/lib/display-units";
import {
  formatMCPEventForDisplay,
  formatObservationEventForDisplay,
} from "@/lib/mcp-events";
import {
  COMPACT_COPIED_TOOLTIP,
  compactSummaryText,
  copyButtonDefaultTooltipMode,
  copyButtonTooltip,
  messageGroupCanCopy,
  messageGroupCopyText,
  type CopyTooltipMode,
} from "@/lib/message-copy";
import { formatModelFallbackNotice } from "@/lib/model-fallback";
import {
  externalEventBodyClassName,
  externalEventCopyClassName,
  externalEventRowClassName,
  processDisclosureBodyClassName,
  processDisclosureChevronClassName,
  processDisclosureClassName,
  processDisclosureSummaryClassName,
  thinkingDisclosureBodyClassName,
  thinkingDisclosureSummaryClassName,
} from "@/lib/message-rendering";
import {
  messageGroupRendererKey,
  type MessageGroupRendererKey,
} from "@/lib/session-transcript-renderers";
import {
  aggregateToolProcessStatus,
  formatToolBatchTitle,
  formatToolProcessResult,
  thinkingProcessDisplay,
  thinkingProcessVisibleText,
  toolDisplayName,
  toolProcessStatus,
  toolProcessStatusLabel,
  toolTimeoutLabel,
  type ToolProcessStatus,
} from "@/lib/tool-display";
import { cn } from "@/lib/utils";
import type { MediaRef } from "@/types";

type MessageGroupRendererProps = {
  compactCommand?: string;
  group: MessageGroup;
  modelLabel?: string;
};

const messageGroupRendererRegistry: Record<
  MessageGroupRendererKey,
  ComponentType<MessageGroupRendererProps>
> = {
  default: DefaultMessageGroup,
  mcp_event: MCPEventGroup,
  observation: ObservationEventGroup,
  hook_event: HookEventGroup,
  model_fallback: ModelFallbackGroup,
  system_notice: SystemNoticeGroup,
  compact: CompactGroup,
  [LOCAL_COMPACT_PENDING_KIND]: PendingCompactGroup,
};

export function SessionTranscript({
  compactCommandInputs,
  items,
  modelLabels,
}: {
  compactCommandInputs: Record<string, string>;
  items: readonly TranscriptItem[];
  modelLabels: readonly (string | undefined)[];
}) {
  return items.map((item, index) => {
    const modelLabel = modelLabels[index];
    if (item.kind === "assistant_work") {
      return (
        <AssistantWorkGroupView
          key={item.key}
          work={item}
          modelLabel={modelLabel}
        />
      );
    }
    return (
      <MessageGroupRow
        key={item.key}
        group={item.group}
        modelLabel={modelLabel}
        compactCommand={
          item.group.id ? compactCommandInputs[item.group.id] : undefined
        }
      />
    );
  });
}

function MessageGroupRow(props: MessageGroupRendererProps) {
  const Renderer = messageGroupRendererRegistry[
    messageGroupRendererKey(props.group)
  ];
  return createElement(Renderer, props);
}

function MCPEventGroup({ group }: MessageGroupRendererProps) {
  return <ExternalEventGroup group={group} eventKind="mcp" />;
}

function ObservationEventGroup({ group }: MessageGroupRendererProps) {
  return <ExternalEventGroup group={group} eventKind="observation" />;
}

function CompactGroup({
  compactCommand,
  group,
}: MessageGroupRendererProps) {
  const textUnit = group.units.find((unit) => unit.kind === "text");
  const text = textUnit?.kind === "text" ? textUnit.block.text : "";
  return (
    <>
      {compactCommand ? <SlashCommandMessage text={compactCommand} /> : null}
      <CompactMessage text={text} />
    </>
  );
}

function PendingCompactGroup() {
  return <CompactMessage text={PENDING_COMPACT_LABEL} state="pending" />;
}

function AssistantWorkGroupView({
  work,
  modelLabel,
}: {
  work: AssistantWorkItem;
  modelLabel?: string;
}) {
  const [isOpen, setIsOpen] = useState(false);
  const content = work.contentGroup;
  const copyText = content ? messageGroupCopyText(content) : "";
  const canCopy = content ? messageGroupCanCopy(content) : false;

  return (
    <Message from="assistant">
      <div className="flex w-full flex-col gap-2">
        {modelLabel ? (
          <span className="font-mono text-[11px] text-muted-foreground">
            {modelLabel}
          </span>
        ) : null}
        <details
          open={isOpen}
          onToggle={(event) => setIsOpen(event.currentTarget.open)}
          className="group/work-row w-full"
        >
          <summary className={processDisclosureSummaryClassName()}>
            <span className="min-w-0 truncate">
              {assistantWorkTitle(work)}
            </span>
            <ChevronRightIcon
              className="size-3 shrink-0 transition-transform motion-reduce:transition-none group-open/work-row:rotate-90"
              aria-hidden="true"
            />
          </summary>
          <div className={processDisclosureBodyClassName()}>
            {work.processGroups.flatMap((group) =>
              group.units.map((unit, index) => {
                const key = `${group.key}:${index}`;
                if (unit.kind === "reasoning") {
                  return (
                    <ThinkingProcessRow
                      key={key}
                      redacted={unit.block.redacted}
                      text={thinkingProcessVisibleText(unit.block)}
                    />
                  );
                }
                if (unit.kind === "tool_batch") {
                  return (
                    <ToolBatchProcessRow key={key} tools={unit.tools} />
                  );
                }
                return <ToolProcessRow key={key} tool={unit} />;
              }),
            )}
          </div>
        </details>
        {content ? <AssistantWorkContent group={content} /> : null}
        {canCopy ? <MessageCopyAction text={copyText} align="start" /> : null}
      </div>
    </Message>
  );
}

function AssistantWorkContent({ group }: { group: MessageGroup }) {
  return group.units.map((unit, index) => {
    if (unit.kind === "text") {
      return unit.block.text.trim() ? (
        <AssistantPlainText key={index} text={unit.block.text} />
      ) : null;
    }
    if (unit.kind !== "image") return null;
    if (index > 0 && group.units[index - 1]?.kind === "image") return null;
    const media: Array<MediaRef | null> = [];
    for (let cursor = index; cursor < group.units.length; cursor++) {
      const candidate = group.units[cursor];
      if (candidate.kind !== "image") break;
      media.push(candidate.block.media ?? null);
    }
    return <MessageImageGallery key={index} media={media} role="assistant" />;
  });
}

function DefaultMessageGroup({
  group,
  modelLabel,
}: MessageGroupRendererProps) {
  const copyText = messageGroupCopyText(group);
  const canCopyMessage = messageGroupCanCopy(group);
  const isEmpty = group.units.length === 0;
  const userImageMedia =
    group.role === "user"
      ? group.units.flatMap((unit) =>
          unit.kind === "image" ? [unit.block.media ?? null] : [],
        )
      : [];

  return (
    <Message from={group.role}>
      <div className="flex w-full flex-col gap-2">
        {modelLabel ? (
          <span className="font-mono text-[11px] text-muted-foreground">
            {modelLabel}
          </span>
        ) : null}
        {userImageMedia.length > 0 ? (
          <MessageImageGallery media={userImageMedia} role="user" />
        ) : null}
        {group.units.map((unit, index) => {
          if (unit.kind === "text") {
            if (group.role === "assistant") {
              return (
                <AssistantPlainText key={index} text={unit.block.text} />
              );
            }
            return (
              <MessageContent key={index}>
                <MessageResponse>{unit.block.text}</MessageResponse>
              </MessageContent>
            );
          }
          if (unit.kind === "reasoning") {
            return (
              <ThinkingProcessRow
                key={index}
                redacted={unit.block.redacted}
                text={thinkingProcessVisibleText(unit.block)}
              />
            );
          }
          if (unit.kind === "image") {
            if (group.role === "user") return null;
            if (
              index > 0 &&
              group.units[index - 1]?.kind === "image"
            ) {
              return null;
            }
            const media: Array<MediaRef | null> = [];
            for (
              let cursor = index;
              cursor < group.units.length;
              cursor++
            ) {
              const candidate = group.units[cursor];
              if (candidate.kind !== "image") break;
              media.push(candidate.block.media ?? null);
            }
            return (
              <MessageImageGallery
                key={index}
                media={media}
                role={group.role}
              />
            );
          }
          if (unit.kind === "tool_batch") {
            return <ToolBatchProcessRow key={index} tools={unit.tools} />;
          }
          return <ToolProcessRow key={index} tool={unit} />;
        })}
        {group.pending && isEmpty ? (
          <div className="animate-pulse text-sm text-muted-foreground motion-reduce:animate-none">
            ...
          </div>
        ) : null}
        {canCopyMessage ? (
          <MessageCopyAction
            text={copyText}
            align={group.role === "user" ? "end" : "start"}
          />
        ) : null}
      </div>
    </Message>
  );
}

function MessageImageGallery({
  media,
  role,
}: {
  media: Array<MediaRef | null>;
  role: MessageGroup["role"];
}) {
  if (media.length === 0) return null;
  return (
    <div
      className={cn(
        role === "user"
          ? "flex w-full flex-wrap justify-end gap-2"
          : "grid w-fit max-w-full gap-2",
        role !== "user" && media.length > 1 && "grid-cols-2",
        role === "user" ? "ml-auto" : "mr-auto",
      )}
    >
      {media.map((item, index) => (
        <ImageBlock
          key={`${item?.artifact_path ?? "image"}-${index}`}
          media={item}
          variant="thumbnail"
        />
      ))}
    </div>
  );
}

function AssistantPlainText({ text }: { text: string }) {
  return (
    <div className="max-w-[min(100%,42rem)] text-[14.5px] leading-7 text-foreground">
      <AssistantMarkdown>{text}</AssistantMarkdown>
    </div>
  );
}

function ThinkingProcessRow({
  redacted,
  text,
}: {
  redacted?: boolean;
  text: string;
}) {
  const display = thinkingProcessDisplay(text, redacted);
  return (
    <details className="group/thinking-row w-full">
      <summary className={thinkingDisclosureSummaryClassName()}>
        <span className="min-w-0 truncate">Thinking</span>
        <ChevronRightIcon
          className="size-3 shrink-0 transition-transform group-open/thinking-row:rotate-90"
          aria-hidden="true"
        />
      </summary>
      <div className={thinkingDisclosureBodyClassName()}>
        <MessageResponse className="break-words">
          {display.content || "-"}
        </MessageResponse>
      </div>
    </details>
  );
}

function ToolBatchProcessRow({ tools }: { tools: ToolDisplayUnit[] }) {
  const title = formatToolBatchTitle(tools.map(toolProcessName));
  const status = aggregateToolProcessStatus(
    tools.map((tool) => toolState(tool.use, tool.result, tool.state)),
  );

  return (
    <ProcessDisclosure status={status} title={title || "tool batch"}>
      <div className="flex flex-col gap-1.5">
        {tools.map((tool, index) => (
          <ToolProcessRow
            key={tool.use?.tool_use_id ?? tool.result?.tool_use_id ?? index}
            tool={tool}
            nested
          />
        ))}
      </div>
    </ProcessDisclosure>
  );
}

function ToolProcessRow({
  nested = false,
  tool,
}: {
  nested?: boolean;
  tool: ToolDisplayUnit;
}) {
  const state = toolState(tool.use, tool.result, tool.state);
  const status = toolProcessStatus(state);
  const name = toolProcessName(tool);
  const hasContent = Boolean(tool.use || tool.result);

  return (
    <ProcessDisclosure
      status={status}
      title={name}
      nested={nested}
      detail={toolTimeoutLabel(tool.use?.timeout_seconds)}
    >
      {hasContent ? (
        <div className="flex flex-col gap-2">
          {tool.use ? (
            <ProcessPayload
              label="Parameters"
              value={formatToolInput(tool.use.input)}
            />
          ) : null}
          {tool.result ? <ToolResultPayload result={tool.result} /> : null}
        </div>
      ) : null}
    </ProcessDisclosure>
  );
}

function ToolResultPayload({
  result,
}: {
  result: NonNullable<ToolDisplayUnit["result"]>;
}) {
  const text = formatToolProcessResult(result);
  return (
    <div className="flex min-w-0 flex-col gap-2">
      {text ? (
        <ProcessPayload
          label={result.is_error ? "Error" : "Result"}
          tone={result.is_error ? "error" : "muted"}
          value={text}
        />
      ) : null}
      {result.media ? (
        <ImageBlock media={result.media} variant="thumbnail" />
      ) : null}
      {!text && !result.media ? (
        <ProcessPayload
          label={result.is_error ? "Error" : "Result"}
          tone={result.is_error ? "error" : "muted"}
          value="-"
        />
      ) : null}
    </div>
  );
}

function ProcessDisclosure({
  children,
  detail,
  nested = false,
  status,
  title,
}: {
  children: ReactNode;
  detail?: string;
  nested?: boolean;
  status: ToolProcessStatus;
  title: string;
}) {
  const [isOpen, setIsOpen] = useState(false);
  return (
    <details
      open={isOpen}
      onToggle={(event) => setIsOpen(event.currentTarget.open)}
      className={processDisclosureClassName(nested)}
    >
      <summary className={processDisclosureSummaryClassName()}>
        <ProcessStatusIndicator status={status} />
        <span className="sr-only">{toolProcessStatusLabel(status)}</span>
        <span className="min-w-0 truncate">{title}</span>
        {detail ? (
          <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
            {detail}
          </span>
        ) : null}
        <ChevronRightIcon
          className={processDisclosureChevronClassName(nested)}
          aria-hidden="true"
        />
      </summary>
      <div className={processDisclosureBodyClassName()}>{children}</div>
    </details>
  );
}

function ProcessStatusIndicator({ status }: { status: ToolProcessStatus }) {
  if (status === "running") {
    return (
      <LoaderCircleIcon
        className="size-3 shrink-0 animate-spin text-muted-foreground motion-reduce:animate-none"
        aria-hidden="true"
      />
    );
  }
  return (
    <span
      className={cn(
        "grid size-4 shrink-0 place-items-center rounded-full",
        status === "failed"
          ? "bg-juex-error-bg text-juex-error"
          : "bg-juex-success-bg text-juex-done",
      )}
      aria-hidden="true"
    >
      {status === "failed" ? (
        <CircleAlertIcon className="size-3" />
      ) : (
        <CheckIcon className="size-3" />
      )}
    </span>
  );
}

function ProcessPayload({
  label,
  tone = "muted",
  value,
}: {
  label: string;
  tone?: "muted" | "error";
  value: string;
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <div
        className={cn(
          "font-mono text-[10px] uppercase tracking-normal",
          tone === "error" ? "text-juex-error" : "text-muted-foreground",
        )}
      >
        {label}
      </div>
      <pre
        className={cn(
          "max-h-72 overflow-auto whitespace-pre-wrap break-words rounded border px-2 py-1.5 font-mono text-[11px] leading-relaxed",
          tone === "error"
            ? "border-juex-error/25 bg-juex-error-bg/40 text-juex-error"
            : "border-border/60 bg-muted/35 text-foreground",
        )}
      >
        {value}
      </pre>
    </div>
  );
}

function toolProcessName(tool: ToolDisplayUnit): string {
  const raw = tool.use?.tool_name ?? "tool";
  return toolDisplayName(`tool-${raw}`);
}

function formatToolInput(input: Record<string, unknown> | undefined): string {
  if (input === undefined) return "{}";
  try {
    return JSON.stringify(input, null, 2);
  } catch {
    return String(input);
  }
}

function SlashCommandMessage({ text }: { text: string }) {
  return (
    <Message from="user">
      <div className="flex w-full flex-col gap-2">
        <MessageContent>
          <MessageResponse>{text}</MessageResponse>
        </MessageContent>
        <MessageCopyAction text={text} align="end" />
      </div>
    </Message>
  );
}

function ExternalEventGroup({
  eventKind,
  group,
}: {
  eventKind: "mcp" | "observation";
  group: MessageGroup;
}) {
  const isEmpty = group.units.length === 0;
  return (
    <div className="flex w-full justify-center px-2 py-0.5">
      <div className="flex w-full max-w-[min(34rem,100%)] flex-col gap-2">
        {group.units.map((unit, index) =>
          unit.kind === "text" ? (
            <ExternalEventMessage
              key={index}
              eventKind={eventKind}
              text={unit.block.text}
            />
          ) : null,
        )}
        {group.pending && isEmpty ? (
          <div className="text-center text-sm text-muted-foreground">...</div>
        ) : null}
      </div>
    </div>
  );
}

function HookEventGroup({ group }: MessageGroupRendererProps) {
  const text = groupText(group);
  if (!text && !group.pending) return null;
  return (
    <div className="flex w-full justify-center px-2 py-0.5">
      <div
        className="max-w-full truncate rounded-full bg-muted/60 px-2.5 py-1 font-mono text-[11px] text-muted-foreground"
        title={text}
      >
        {text || "..."}
      </div>
    </div>
  );
}

function SystemNoticeGroup({ group }: MessageGroupRendererProps) {
  const text = groupText(group);
  if (!text && !group.pending) return null;
  return (
    <div
      className="flex w-full justify-center px-2 py-0.5"
      data-system-notice-message
    >
      <div className="w-full max-w-[min(42rem,100%)]">
        <ProcessDisclosure status="done" title="Automated notice">
          <MessageResponse className="break-words text-[13px] leading-6 text-muted-foreground">
            {text || "..."}
          </MessageResponse>
        </ProcessDisclosure>
      </div>
    </div>
  );
}

function ModelFallbackGroup({ group }: MessageGroupRendererProps) {
  const text = groupText(group);
  if (!text && !group.pending) return null;
  const display = formatModelFallbackNotice(text);
  return (
    <div
      className="flex w-full justify-center px-2 py-0.5"
      data-model-fallback-message
    >
      <div className="w-full max-w-[min(42rem,100%)]">
        <ProcessDisclosure status="done" title={display.title}>
          <MessageResponse className="break-words text-[13px] leading-6 text-muted-foreground">
            {display.content || "..."}
          </MessageResponse>
        </ProcessDisclosure>
      </div>
    </div>
  );
}

function groupText(group: MessageGroup): string {
  return group.units
    .filter((unit) => unit.kind === "text")
    .map((unit) => (unit.kind === "text" ? unit.block.text : ""))
    .filter(Boolean)
    .join("\n");
}

function CompactMessage({
  text,
  state = "complete",
}: {
  text: string;
  state?: "complete" | "pending";
}) {
  if (state === "pending") {
    return (
      <div className="flex w-full items-center gap-3 px-2 py-3">
        <Separator className="flex-1 opacity-60" />
        <span className="rounded-full border border-border/70 bg-background/70 px-3 py-1 font-mono text-[11px] text-muted-foreground/70 shadow-[var(--shadow-xs)]">
          {text}
        </span>
        <Separator className="flex-1 opacity-60" />
      </div>
    );
  }
  const summary = compactSummaryText(text);
  return (
    <div className="flex w-full items-center gap-3 px-2 py-3">
      <Separator className="flex-1" />
      <CopyTextButton
        text={summary}
        className="h-7 rounded-full border border-border bg-background px-3 font-mono text-[11px] text-muted-foreground shadow-[var(--shadow-xs)] hover:text-foreground"
        copiedTooltip={COMPACT_COPIED_TOOLTIP}
        idleTooltip="Copy compacted context"
        label="Copy compacted context"
        size="sm"
        tooltipMode="copied-only"
      >
        Context compacted
      </CopyTextButton>
      <Separator className="flex-1" />
    </div>
  );
}

function MessageCopyAction({
  text,
  align,
}: {
  text: string;
  align: "start" | "end";
}) {
  return (
    <MessageActions
      className={cn(
        "opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100",
        align === "end" ? "justify-end pr-1" : "justify-start pl-1",
      )}
    >
      <CopyTextButton
        text={text}
        className="size-6 text-muted-foreground hover:text-foreground"
        copiedTooltip="Copied to clipboard"
        idleTooltip="Copy message"
        label="Copy message"
        size="icon-xs"
        tooltipMode="none"
      />
    </MessageActions>
  );
}

function CopyTextButton({
  text,
  className,
  idleTooltip,
  copiedTooltip,
  label,
  size = "icon-sm",
  tooltipMode,
  children,
}: {
  text: string;
  className?: string;
  idleTooltip: string;
  copiedTooltip: string;
  label?: string;
  size?:
    | "default"
    | "xs"
    | "sm"
    | "lg"
    | "icon"
    | "icon-xs"
    | "icon-sm"
    | "icon-lg";
  tooltipMode?: CopyTooltipMode;
  children?: ReactNode;
}) {
  const [copySignal, setCopySignal] = useState(0);
  const copied = copySignal > 0;
  const effectiveTooltipMode =
    tooltipMode ??
    copyButtonDefaultTooltipMode({ hasVisibleLabel: Boolean(children) });
  const tooltip = copyButtonTooltip({
    copied,
    mode: effectiveTooltipMode,
    idleTooltip,
    copiedTooltip,
  });
  const tooltipOpen =
    effectiveTooltipMode === "copied-only" ? copied : undefined;

  useEffect(() => {
    if (!copySignal) return;
    const reset = window.setTimeout(() => setCopySignal(0), 1800);
    return () => window.clearTimeout(reset);
  }, [copySignal]);

  async function copyText() {
    if (!text) return;
    try {
      await writeClipboardText(text);
      setCopySignal((current) => current + 1);
    } catch (error) {
      console.error("copy text failed", error);
    }
  }

  return (
    <MessageAction
      className={className}
      label={label ?? idleTooltip}
      onClick={() => void copyText()}
      size={size}
      tooltip={tooltip}
      tooltipOpen={tooltipOpen}
      variant="ghost"
    >
      {children ??
        (copied ? (
          <CheckIcon className="size-3.5" aria-hidden="true" />
        ) : (
          <CopyIcon className="size-3.5" aria-hidden="true" />
        ))}
    </MessageAction>
  );
}

function ExternalEventMessage({
  eventKind,
  text,
}: {
  eventKind: "mcp" | "observation";
  text: string;
}) {
  const [expanded, setExpanded] = useState(false);
  const event = useMemo(
    () =>
      eventKind === "observation"
        ? formatObservationEventForDisplay(text)
        : formatMCPEventForDisplay(text),
    [eventKind, text],
  );
  const eventName =
    eventKind === "observation" ? "observation event" : "MCP event";
  const toggleLabel = expanded ? `Collapse ${eventName}` : `Expand ${eventName}`;

  return (
    <details
      open={expanded}
      onToggle={(event) => setExpanded(event.currentTarget.open)}
      className="group/external-event w-full"
      data-external-event-message
      data-external-event-kind={eventKind}
      data-mcp-event-message={eventKind === "mcp" ? "" : undefined}
    >
      <summary
        className={externalEventRowClassName()}
        title={toggleLabel}
        data-external-event-toggle
        data-mcp-event-toggle={eventKind === "mcp" ? "" : undefined}
      >
        <RadioIcon className="size-3.5 shrink-0" aria-hidden="true" />
        <span
          className="min-w-0 max-w-[48%] shrink-0 truncate font-mono font-semibold sm:max-w-[18rem]"
          data-external-event-label
          data-mcp-event-label={eventKind === "mcp" ? "" : undefined}
        >
          {event.label}
        </span>
        <span
          className="size-1 shrink-0 rounded-full bg-current opacity-45"
          aria-hidden="true"
        />
        <span
          className="min-w-0 flex-1 truncate text-[12px] text-current opacity-75"
          data-external-event-preview
          data-mcp-event-preview={eventKind === "mcp" ? "" : undefined}
        >
          {event.preview}
        </span>
        <ChevronRightIcon
          className="size-3.5 shrink-0 transition-transform group-open/external-event:rotate-90"
          aria-hidden="true"
        />
      </summary>
      {expanded ? (
        <div
          className={externalEventBodyClassName()}
          data-external-event-body
          data-mcp-event-body={eventKind === "mcp" ? "" : undefined}
        >
          <span
            data-external-event-copy
            data-mcp-event-copy={eventKind === "mcp" ? "" : undefined}
          >
            <CopyTextButton
              text={event.copyText}
              className={externalEventCopyClassName()}
              copiedTooltip="Copied to clipboard"
              idleTooltip="Copy event content"
              label="Copy event content"
              size="icon-sm"
            />
          </span>
          <MessageResponse className="break-words">
            {event.content}
          </MessageResponse>
        </div>
      ) : null}
    </details>
  );
}
