import { ChevronRightIcon } from "lucide-react";

import { cn } from "@/lib/utils";

interface RuntimeDisclosureButtonProps {
  label: string;
  open: boolean;
  onToggle: () => void;
  className?: string;
}

export function RuntimeDisclosureButton({
  label,
  open,
  onToggle,
  className,
}: RuntimeDisclosureButtonProps) {
  return (
    <button
      type="button"
      aria-expanded={open}
      aria-label={`${open ? "Collapse" : "Expand"} ${label}`}
      className={cn(
        "inline-flex size-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground outline-none transition-colors hover:bg-muted hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/35",
        className,
      )}
      onClick={onToggle}
    >
      <ChevronRightIcon
        aria-hidden="true"
        className={cn("size-3.5 transition-transform", open && "rotate-90")}
      />
    </button>
  );
}
