import { useParams } from "react-router-dom";

export function Session() {
  const { id } = useParams<{ id: string }>();
  return (
    <div className="flex flex-1 flex-col p-6">
      <div className="text-muted-foreground text-xs">
        session: <code className="font-mono">{id}</code>
      </div>
      <div className="mt-4 flex flex-1 items-center justify-center text-muted-foreground">
        Transcript and composer will live here (Tasks 5-9).
      </div>
    </div>
  );
}
