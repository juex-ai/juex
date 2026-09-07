import { GoalStatus } from "./goal/GoalStatus";
import { NotesStatus } from "./notes/NotesStatus";
import { scratchpadFiles } from "./scratchpad/files";
import type { ModuleContribution } from "./types";

export const moduleContributions: readonly ModuleContribution[] = [
  { id: "goal.status", moduleID: "goal", version: 1, order: 10, label: "Goal", slot: "thread.status", Component: GoalStatus },
  { id: "notes.status", moduleID: "notes", version: 1, order: 20, label: "Notes", slot: "thread.status", Component: NotesStatus },
  scratchpadFiles,
];
