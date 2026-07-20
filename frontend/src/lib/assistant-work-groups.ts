import type {
  DisplayUnit,
  MessageGroup,
  ToolDisplayUnit,
} from "./display-units";
import { toolDisplayName } from "./tool-display.ts";

type ProcessDisplayUnit = Extract<
  DisplayUnit,
  { kind: "reasoning" | "tool" | "tool_batch" }
>;

export type AssistantWorkToolSummary = {
  name: string;
  count: number;
};

export type AssistantWorkProcessGroup = Omit<MessageGroup, "units"> & {
  units: ProcessDisplayUnit[];
};

export type MessageTranscriptItem = {
  kind: "message";
  key: string;
  group: MessageGroup;
};

export type AssistantWorkItem = {
  kind: "assistant_work";
  key: string;
  model?: string;
  phase: "running" | "completed";
  processGroups: AssistantWorkProcessGroup[];
  contentGroup?: MessageGroup;
  latestTools: AssistantWorkToolSummary[];
  toolCount: number;
  startedAt?: string;
  completedAt?: string;
};

export type TranscriptItem = MessageTranscriptItem | AssistantWorkItem;

type WorkBuffer = {
  groups: MessageGroup[];
  effectiveModel?: string;
};

export function assistantWorkItems(
  groups: readonly MessageGroup[],
  { tailActive }: { tailActive: boolean },
): TranscriptItem[] {
  const items: TranscriptItem[] = [];
  let buffer: WorkBuffer | undefined;
  let index = 0;

  while (index < groups.length) {
    const group = groups[index];
    if (!buffer) {
      if (canStartWork(group)) {
        buffer = {
          groups: [group],
          effectiveModel: normalizedModel(group.model),
        };
      } else {
        items.push(messageItem(group));
      }
      index++;
      continue;
    }

    const candidateModel = normalizedModel(group.model);
    if (
      buffer.effectiveModel &&
      candidateModel &&
      candidateModel !== buffer.effectiveModel
    ) {
      flushOriginal(items, buffer.groups);
      buffer = undefined;
      continue;
    }
    const effectiveModel = buffer.effectiveModel ?? candidateModel;

    if (canCompleteWork(group)) {
      buffer.groups.push(group);
      items.push(buildWorkItem(buffer.groups, effectiveModel, "completed"));
      buffer = undefined;
      index++;
      continue;
    }

    if (canContinueWork(group)) {
      buffer.groups.push(group);
      buffer.effectiveModel = effectiveModel;
      index++;
      continue;
    }

    flushOriginal(items, buffer.groups);
    buffer = undefined;
  }

  if (buffer) {
    if (tailActive) {
      items.push(
        buildWorkItem(buffer.groups, buffer.effectiveModel, "running"),
      );
    } else {
      flushOriginal(items, buffer.groups);
    }
  }

  return items;
}

export function assistantWorkTitle(work: AssistantWorkItem): string {
  if (work.phase === "running") {
    const totalLatestTools = work.latestTools.reduce(
      (sum, tool) => sum + tool.count,
      0,
    );
    const names = work.latestTools
      .map((tool) => (tool.count > 1 ? `${tool.count} ${tool.name}` : tool.name))
      .join(", ");
    return totalLatestTools === 1
      ? `Working with tool: ${names || "tool"}`
      : `Working with tools: ${names || "tools"}`;
  }

  const toolNoun = work.toolCount === 1 ? "tool" : "tools";
  const duration = responseSpan(work.startedAt, work.completedAt);
  return duration
    ? `Worked for ${duration}, called ${work.toolCount} ${toolNoun}`
    : `Worked, called ${work.toolCount} ${toolNoun}`;
}

export function transcriptItemModelLabels(
  items: readonly TranscriptItem[],
): Array<string | undefined> {
  let previousAssistantModel: string | undefined;
  let inAssistantRun = false;

  return items.map((item) => {
    const group = item.kind === "message" ? item.group : undefined;
    const isNormalAssistant =
      item.kind === "assistant_work" ||
      (group?.role === "assistant" && !group.kind);
    const model =
      item.kind === "assistant_work" ? item.model : normalizedModel(group?.model);
    if (!isNormalAssistant || !model) {
      previousAssistantModel = undefined;
      inAssistantRun = false;
      return undefined;
    }
    if (inAssistantRun && previousAssistantModel === model) {
      return undefined;
    }
    previousAssistantModel = model;
    inAssistantRun = true;
    return model;
  });
}

function messageItem(group: MessageGroup): MessageTranscriptItem {
  return { kind: "message", key: group.key, group };
}

function flushOriginal(
  items: TranscriptItem[],
  groups: readonly MessageGroup[],
) {
  items.push(...groups.map(messageItem));
}

function canStartWork(group: MessageGroup): boolean {
  if (!isNormalAssistant(group) || hasNonEmptyText(group) || hasMedia(group)) {
    return false;
  }
  return hasReasoning(group) && toolCount(group) > 0;
}

function canContinueWork(group: MessageGroup): boolean {
  if (!isNormalAssistant(group) || hasNonEmptyText(group) || hasMedia(group)) {
    return false;
  }
  return (
    group.pending ||
    group.units.every(
      (unit) =>
        isProcessUnit(unit) ||
        (unit.kind === "text" && !unit.block.text.trim()),
    )
  );
}

function canCompleteWork(group: MessageGroup): boolean {
  return isNormalAssistant(group) && hasNonEmptyText(group);
}

function isNormalAssistant(group: MessageGroup): boolean {
  return group.role === "assistant" && !group.kind;
}

function hasNonEmptyText(group: MessageGroup): boolean {
  return group.units.some(
    (unit) => unit.kind === "text" && Boolean(unit.block.text.trim()),
  );
}

function hasReasoning(group: MessageGroup): boolean {
  return group.units.some((unit) => unit.kind === "reasoning");
}

function hasMedia(group: MessageGroup): boolean {
  return group.units.some((unit) => unit.kind === "image");
}

function isProcessUnit(unit: DisplayUnit): unit is ProcessDisplayUnit {
  return (
    unit.kind === "reasoning" ||
    unit.kind === "tool" ||
    unit.kind === "tool_batch"
  );
}

function buildWorkItem(
  groups: readonly MessageGroup[],
  effectiveModel: string | undefined,
  phase: AssistantWorkItem["phase"],
): AssistantWorkItem {
  const firstToolID = firstToolUseID(groups[0]);
  const key = `assistant-work:${firstToolID || groups[0].key}`;
  const processGroups: AssistantWorkProcessGroup[] = [];
  let processIndex = 0;
  for (const group of groups) {
    const units = group.units.filter(isProcessUnit);
    if (units.length === 0) continue;
    processGroups.push({
      ...group,
      key: `${key}:process:${processIndex}`,
      units,
    });
    processIndex++;
  }

  const completionGroup = phase === "completed" ? groups.at(-1) : undefined;
  const contentUnits = completionGroup?.units.filter(
    (unit) => unit.kind === "text" || unit.kind === "image",
  );
  const contentGroup =
    completionGroup && contentUnits?.length
      ? {
          ...completionGroup,
          key: `${key}:content`,
          units: contentUnits,
        }
      : undefined;

  return {
    kind: "assistant_work",
    key,
    model: effectiveModel,
    phase,
    processGroups,
    contentGroup,
    latestTools: latestToolSummary(groups),
    toolCount: groups.reduce((sum, group) => sum + toolCount(group), 0),
    startedAt: groups[0].createdAt,
    completedAt: completionGroup?.createdAt,
  };
}

function firstToolUseID(group: MessageGroup): string | undefined {
  for (const unit of group.units) {
    if (unit.kind === "tool") {
      const id = unit.use?.tool_use_id?.trim();
      if (id) return id;
    }
    if (unit.kind === "tool_batch") {
      for (const tool of unit.tools) {
        const id = tool.use?.tool_use_id?.trim();
        if (id) return id;
      }
    }
  }
  return undefined;
}

function toolCount(group: MessageGroup): number {
  return group.units.reduce((sum, unit) => {
    if (unit.kind === "tool") return sum + 1;
    if (unit.kind === "tool_batch") return sum + unit.tools.length;
    return sum;
  }, 0);
}

function latestToolSummary(
  groups: readonly MessageGroup[],
): AssistantWorkToolSummary[] {
  for (let index = groups.length - 1; index >= 0; index--) {
    const tools = groups[index].units.flatMap((unit) => {
      if (unit.kind === "tool") return [unit];
      if (unit.kind === "tool_batch") return unit.tools;
      return [];
    });
    if (tools.length > 0) return summarizeTools(tools);
  }
  return [];
}

function summarizeTools(
  tools: readonly ToolDisplayUnit[],
): AssistantWorkToolSummary[] {
  const summaries: AssistantWorkToolSummary[] = [];
  const byName = new Map<string, AssistantWorkToolSummary>();
  for (const tool of tools) {
    const rawName = tool.use?.tool_name?.trim() || "tool";
    const name = toolDisplayName(`tool-${rawName}`);
    const existing = byName.get(name);
    if (existing) {
      existing.count++;
      continue;
    }
    const summary = { name, count: 1 };
    summaries.push(summary);
    byName.set(name, summary);
  }
  return summaries;
}

function normalizedModel(model: string | undefined): string | undefined {
  const normalized = model?.trim();
  return normalized || undefined;
}

function responseSpan(
  startedAt: string | undefined,
  completedAt: string | undefined,
): string | undefined {
  if (!startedAt || !completedAt) return undefined;
  const start = Date.parse(startedAt);
  const end = Date.parse(completedAt);
  if (!Number.isFinite(start) || !Number.isFinite(end) || end < start) {
    return undefined;
  }
  let seconds = Math.floor((end - start) / 1000);
  const hours = Math.floor(seconds / 3600);
  seconds %= 3600;
  const minutes = Math.floor(seconds / 60);
  seconds %= 60;
  const parts: string[] = [];
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0) parts.push(`${minutes}min`);
  if (seconds > 0 || parts.length === 0) parts.push(`${seconds}s`);
  return parts.join(" ");
}
