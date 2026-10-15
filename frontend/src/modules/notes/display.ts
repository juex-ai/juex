import type { NotesSnapshot } from "../../module-schema";

export interface NotesCheckboxProgress {
  completed: number;
  total: number;
  percent: number;
}

export function notesCheckboxProgress(notes?: NotesSnapshot): NotesCheckboxProgress {
  let completed = 0;
  let total = 0;
  for (const match of notes?.content?.matchAll(/^\s*-\s+\[([ xX])\]\s+/gm) ?? []) {
    total += 1;
    if (match[1].toLowerCase() === "x") completed += 1;
  }
  return { completed, total, percent: total > 0 ? (completed / total) * 100 : 0 };
}

export function notesBadgeLabel(notes?: NotesSnapshot): string {
  const progress = notesCheckboxProgress(notes);
  if (progress.total > 0) return `notes ${progress.completed}/${progress.total}`;
  return notes?.content?.trim() ? "notes active" : "notes empty";
}
