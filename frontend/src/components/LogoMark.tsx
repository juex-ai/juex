import { cn } from "@/lib/utils";

export function LogoMark({ className }: { className?: string }) {
  return <span aria-hidden="true" className={cn("juex-logo", className)} />;
}
