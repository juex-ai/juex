import { ArrowRight, ChevronDown, ChevronUp } from "lucide-react";
import type { MemoryDomain, MemoryRelation } from "@/api";
import { Button } from "@/components/ui/button";

function RelationRules({ relation: r }: { relation: MemoryRelation }) {
  return (
    <details className="border-t pt-3" key={r.predicate}>
      <summary className="cursor-pointer text-sm font-medium">
        Relation rules · {r.predicate.replaceAll("_", " ")}
      </summary>
      <div className="mt-3 space-y-2 break-words text-xs text-muted-foreground">
        <p>{r.description}</p>
        <p>
          <strong>
            {r.cardinality === "one" ? "Single value" : "Multiple values"}
          </strong>{" "}
          · Competes within: {r.competition.join(" + ")}
        </p>
        {r.qualifiers?.length ? (
          <p>Required qualifiers: {r.qualifiers.join(", ")}</p>
        ) : null}
        <p>
          Optional qualifiers: {r.optional_qualifiers?.join(", ") || "None"}.
          Evidence source types: {r.source_types?.join(", ") || "As declared"}.
        </p>
        <p>{r.temporal}</p>
        <p>Updates: {r.updates.join(", ")}</p>
        <p>{r.evidence}</p>
        <p>
          <strong>Example:</strong> {r.positive}
        </p>
        <p>
          <strong>Do not infer:</strong> {r.negative}
        </p>
      </div>
    </details>
  );
}

export function DomainStructure({
  domain,
  predicate,
  onSelect,
  open,
  onToggle,
}: {
  domain: MemoryDomain;
  predicate: string;
  onSelect: (predicate: string) => void;
  open: boolean;
  onToggle: () => void;
}) {
  const groups = new Map<string, MemoryRelation[]>();
  for (const relation of domain.relations ?? []) {
    const subject = relation.subjects.join(" / ");
    groups.set(subject, [...(groups.get(subject) ?? []), relation]);
  }
  const selected = domain.relations?.find((r) => r.predicate === predicate);
  return (
    <section
      aria-label="Domain structure"
      className="min-w-0 self-start rounded-lg border bg-card p-4"
    >
      <h2 className="hidden text-sm font-semibold lg:block">
        Domain structure
      </h2>
      <Button
        variant="ghost"
        className="-m-2 w-[calc(100%+1rem)] justify-between lg:hidden"
        aria-expanded={open}
        aria-controls="memory-domain-structure"
        onClick={onToggle}
      >
        Domain structure{" "}
        {open ? (
          <ChevronUp className="size-4" />
        ) : (
          <ChevronDown className="size-4" />
        )}
      </Button>
      <div
        id="memory-domain-structure"
        className={`${open ? "block" : "hidden"} space-y-4 pt-3 lg:block`}
      >
        <p className="text-xs leading-relaxed text-muted-foreground">
          {domain.description}
        </p>
        <p className="text-xs text-muted-foreground">
          Select a relation to filter facts.
        </p>
        <div className="space-y-4" aria-label="Relation map">
          {[...groups].map(([subject, relations]) => (
            <div key={subject}>
              <p className="mb-2 inline-flex rounded-md bg-muted px-2 py-1 text-xs font-medium">
                {subject}
              </p>
              <ul className="ml-2 space-y-1 border-l pl-3">
                {relations.map((r) => (
                  <li key={r.predicate}>
                    <button
                      type="button"
                      aria-label={`Filter by relation ${r.predicate}`}
                      aria-pressed={predicate === r.predicate}
                      onClick={() =>
                        onSelect(predicate === r.predicate ? "" : r.predicate)
                      }
                      className={`flex min-h-10 w-full items-center gap-2 rounded-md border px-2 py-2 text-left text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${predicate === r.predicate ? "border-primary/40 bg-primary/10 text-primary" : "border-transparent hover:bg-muted"}`}
                    >
                      <span className="min-w-0 flex-1 break-words font-medium">
                        {r.predicate.replaceAll("_", " ")}
                      </span>
                      <ArrowRight
                        aria-hidden
                        className="size-3 shrink-0 text-muted-foreground"
                      />
                      <span className="max-w-[45%] break-words rounded border bg-background px-1.5 py-0.5 text-muted-foreground">
                        {r.objects?.join(" / ") || `${r.value_type} value`}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
        {selected ? <RelationRules relation={selected} /> : null}
        <details className="border-t pt-3">
          <summary className="cursor-pointer text-xs font-medium">
            Domain policy
          </summary>
          <p className="mt-2 text-xs leading-relaxed text-muted-foreground">
            {domain.policy}
          </p>
        </details>
      </div>
    </section>
  );
}
