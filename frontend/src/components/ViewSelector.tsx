import { useRef, type RefObject } from "react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

type ViewOption = { value: string; label: string; disabled?: boolean };

/** A current-view title that opens a menu, separate from form field styling. */
export function ViewSelector({ label, value, options, onValueChange, align = "start", triggerRef }: {
  label: string;
  value: string;
  options: readonly ViewOption[];
  onValueChange: (value: string) => void;
  align?: "start" | "end";
  triggerRef?: RefObject<HTMLButtonElement | null>;
}) {
  const localRef = useRef<HTMLButtonElement>(null);
  const focusRef = triggerRef ?? localRef;
  return <Select value={value} onValueChange={onValueChange}>
    <SelectTrigger ref={focusRef} aria-label={label} title={options.find((option) => option.value === value)?.label}
      className="min-h-11 min-w-0 max-w-full gap-3 rounded-sm border-transparent px-2.5 text-sm font-medium text-foreground hover:bg-muted focus-visible:ring-ring/35 data-[state=open]:bg-muted sm:min-h-9 pointer-coarse:min-h-11 [&_[data-slot=select-value]]:block [&_[data-slot=select-value]]:truncate">
      <SelectValue />
    </SelectTrigger>
    <SelectContent position="popper" align={align} collisionPadding={12}
      onCloseAutoFocus={(event) => { event.preventDefault(); focusRef.current?.focus(); }}
      className="min-w-44 max-w-[min(20rem,var(--radix-select-content-available-width))] rounded-md p-1 shadow-md">
      {options.map((option) => <SelectItem key={option.value} value={option.value} disabled={option.disabled}
        className="min-h-11 rounded-sm py-2 pl-3 text-sm data-[state=checked]:bg-muted data-[state=checked]:font-medium data-[state=checked]:text-primary focus:bg-accent sm:min-h-9 pointer-coarse:min-h-11 [&>span:last-child]:min-w-0 [&>span:last-child]:block [&>span:last-child]:truncate">
        {option.label}
      </SelectItem>)}
    </SelectContent>
  </Select>;
}
