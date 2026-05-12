// Local replacements for type-only imports that AI Elements pulls from
// the "ai" package. The juex spec §6 explains why we don't depend on
// the ai-sdk runtime package:
// docs/superpowers/specs/2026-05-12-web-ui-framework-design.md

export type UIMessageRole = "user" | "assistant" | "system";

export type UIMessagePart = {
  type: string;
  text?: string;
  [key: string]: unknown;
};

export type UIMessage = {
  role: UIMessageRole;
  parts: UIMessagePart[];
};

export type ToolUIPartState =
  | "approval-requested"
  | "approval-responded"
  | "input-streaming"
  | "input-available"
  | "output-available"
  | "output-denied"
  | "output-error";

export type ToolUIPart = {
  type: `tool-${string}`;
  state: ToolUIPartState;
  input?: unknown;
  output?: unknown;
  errorText?: string;
};

// DynamicToolUIPart has type "dynamic-tool" specifically (not tool-*)
export type DynamicToolUIPart = {
  type: "dynamic-tool";
  state: ToolUIPartState;
  input?: unknown;
  output?: unknown;
  errorText?: string;
};

export type ChatStatus = "submitted" | "streaming" | "ready" | "error";

export type FileUIPart = {
  type: "file";
  filename?: string;
  mediaType: string;
  url: string;
};

export type SourceDocumentUIPart = {
  type: "source-document";
  [key: string]: unknown;
};
