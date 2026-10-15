"use client";

import { Badge } from "@/components/ui/badge";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { cn } from "@/lib/utils";
import { formatToolPayload } from "@/lib/tool-payload";
import { formatToolResultText } from "@/lib/tool-result-output";
import {
  toolDisplayName,
  toolStatusLabel,
  toolTimeoutLabel,
} from "@/lib/tool-display";
import type { DynamicToolUIPart, ToolUIPart } from "./_local-types";
import {
  ChevronDownIcon,
  CircleIcon,
} from "lucide-react";
import type { ComponentProps, ReactNode } from "react";
import { isValidElement } from "react";

import { CodeBlock } from "./code-block";

export type ToolProps = ComponentProps<typeof Collapsible>;

export const Tool = ({ className, ...props }: ToolProps) => (
  <Collapsible
    className={cn(
      "group not-prose mb-4 w-full max-w-full overflow-hidden rounded-lg border border-juex-tool-border bg-juex-tool-surface shadow-[var(--shadow-xs)] sm:max-w-[88%]",
      className
    )}
    {...props}
  />
);

export type ToolPart = ToolUIPart | DynamicToolUIPart;

export type ToolHeaderProps = {
  title?: string;
  className?: string;
  timeoutSeconds?: number;
} & (
  | { type: ToolUIPart["type"]; state: ToolUIPart["state"]; toolName?: never }
  | {
      type: DynamicToolUIPart["type"];
      state: DynamicToolUIPart["state"];
      toolName: string;
    }
);

export const getStatusBadge = (status: ToolPart["state"]) => (
  <Badge
    className={cn(
      "gap-1.5 rounded-full font-mono text-[11px]",
      status === "output-error" || status === "output-denied"
        ? "border-juex-error/25 bg-juex-error-bg text-juex-error"
        : "border-juex-tool/20 bg-juex-tool-bg text-juex-tool"
    )}
    variant="outline"
  >
    <CircleIcon className="size-2.5 fill-current" />
    {toolStatusLabel(status)}
  </Badge>
);

export const ToolHeader = ({
  className,
  title,
  type,
  state,
  timeoutSeconds,
  toolName,
  ...props
}: ToolHeaderProps) => {
  const derivedName = toolDisplayName(type, toolName);
  const timeoutLabel =
    state === "input-available" || state === "input-streaming"
      ? toolTimeoutLabel(timeoutSeconds)
      : undefined;

  return (
    <CollapsibleTrigger
      className={cn(
        "flex w-full items-center justify-between gap-4 border-b border-juex-tool-border bg-juex-tool-header px-3.5 py-2.5 text-juex-tool transition-colors hover:bg-juex-tool-header/80 motion-reduce:transition-none",
        className
      )}
      {...props}
    >
      <div className="flex min-w-0 items-center gap-2">
        <span className="size-1.5 shrink-0 rounded-full bg-current" />
        <span className="truncate font-mono text-[12.5px] font-semibold">
          {title ?? derivedName}
        </span>
        {getStatusBadge(state)}
        {timeoutLabel ? (
          <Badge
            className="rounded-full border-juex-tool/15 bg-background/70 font-mono text-[11px] text-muted-foreground"
            variant="outline"
          >
            {timeoutLabel}
          </Badge>
        ) : null}
      </div>
      <ChevronDownIcon className="size-4 shrink-0 opacity-80 transition-transform group-hover:opacity-100 motion-reduce:transition-none group-data-[state=open]:rotate-180" />
    </CollapsibleTrigger>
  );
};

export type ToolContentProps = ComponentProps<typeof CollapsibleContent>;

export const ToolContent = ({ className, ...props }: ToolContentProps) => (
  <CollapsibleContent
    className={cn(
      "data-[state=closed]:fade-out-0 data-[state=closed]:slide-out-to-top-2 data-[state=open]:slide-in-from-top-2 space-y-4 p-4 text-card-foreground outline-none motion-reduce:data-[state=closed]:animate-none motion-reduce:data-[state=open]:animate-none data-[state=closed]:animate-out data-[state=open]:animate-in",
      className
    )}
    {...props}
  />
);

export type ToolInputProps = ComponentProps<"div"> & {
  input: ToolPart["input"];
};

export const ToolInput = ({ className, input, ...props }: ToolInputProps) => (
  <div className={cn("space-y-2 overflow-hidden", className)} {...props}>
    <h4 className="text-[11px] font-medium uppercase tracking-[0.14em] text-muted-foreground dark:text-juex-forest-200">
      Parameters
    </h4>
    <div className="rounded-md">
      <CodeBlock
        className="[&_code]:text-xs [&_pre]:p-3 [&_pre]:text-xs"
        code={formatToolPayload(input)}
        language="json"
      />
    </div>
  </div>
);

export type ToolOutputProps = ComponentProps<"div"> & {
  output: ToolPart["output"];
  errorText: ToolPart["errorText"];
};

export const ToolOutput = ({
  className,
  output,
  errorText,
  ...props
}: ToolOutputProps) => {
  if (output == null && !errorText) {
    return null;
  }

  let Output: ReactNode = null;
  let outputIsCodeBlock = false;
  if (output != null) {
    if (typeof output === "object" && !isValidElement(output)) {
      Output = (
        <CodeBlock
          className="rounded-md [&>div]:max-h-80 [&>div]:overflow-auto [&_code]:text-xs [&_pre]:p-3 [&_pre]:text-xs"
          code={formatToolPayload(output, "null")}
          language="json"
        />
      );
      outputIsCodeBlock = true;
    } else if (typeof output === "string") {
      const formatted = formatToolResultText(output);
      Output = (
        <CodeBlock
          className="rounded-md [&>div]:max-h-80 [&>div]:overflow-auto [&_code]:text-xs [&_pre]:p-3 [&_pre]:text-xs"
          code={formatted.text}
          language="log"
        />
      );
      outputIsCodeBlock = true;
    } else {
      Output = <div>{output as ReactNode}</div>;
    }
  }

  return (
    <div className={cn("space-y-2", className)} {...props}>
      <h4 className="text-[11px] font-medium uppercase tracking-[0.14em] text-muted-foreground dark:text-juex-forest-200">
        {errorText ? "Error" : "Result"}
      </h4>
      <div
        className={cn(
          "rounded-md text-xs [&_table]:w-full",
          errorText
            ? "overflow-x-auto border border-destructive/25 bg-destructive/10 p-3 text-destructive"
            : outputIsCodeBlock
              ? "bg-transparent"
              : "overflow-x-auto border border-juex-tool-border bg-juex-tool-header/45 p-3 text-card-foreground"
        )}
      >
        {errorText && (
          <div className="whitespace-pre-wrap break-words">{errorText}</div>
        )}
        {Output}
      </div>
    </div>
  );
};
