import type { NotesSnapshot } from "@/module-schema";
import { MessageResponse } from "@/components/ai-elements/message";
import { formatRuntimeTimestamp } from "@/lib/runtime-display";
import { ModuleStatusControl, ModuleStatusRow } from "../status-presentation";
import type { ModuleStatusProps } from "../types";
import { notesCheckboxProgress, notesBadgeLabel } from "./display";

export function NotesStatus({ state, readOnly, stale }: ModuleStatusProps) {
  const notes = state.value as NotesSnapshot | null;
  const progress = notesCheckboxProgress(notes ?? undefined);
  return <ModuleStatusControl title="Notes" label={notesBadgeLabel(notes ?? undefined)} active={Boolean(notes?.content?.trim())} readOnly={readOnly} stale={stale}>
    {notes?.content?.trim() ? <>
      <ModuleStatusRow label="updated" value={formatRuntimeTimestamp(notes.updated_at)} />
      {progress.total > 0 ? <div className="space-y-1.5">
        <ModuleStatusRow label="progress" value={`${progress.completed}/${progress.total} complete`} />
        <div aria-label="Notes task progress" aria-valuemax={progress.total} aria-valuemin={0} aria-valuenow={progress.completed} role="progressbar" className="h-1.5 overflow-hidden rounded-sm bg-muted">
          <div className="h-full bg-primary" style={{ width: `${progress.percent}%` }} />
        </div>
      </div> : null}
      <MessageResponse className="break-words text-xs leading-relaxed [&_h1]:!text-base [&_h2]:!text-sm [&_h3]:!text-xs">{notes.content}</MessageResponse>
    </> : <div className="text-muted-foreground">No working notes for this thread.</div>}
  </ModuleStatusControl>;
}
