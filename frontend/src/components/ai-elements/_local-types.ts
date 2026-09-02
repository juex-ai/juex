// Local replacements for the type-only "ai" imports used by copied AI
// Elements components. Juex does not otherwise use that runtime package, so
// keep these narrow structural types instead of adding an unused dependency.

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
