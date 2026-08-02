import { Outlet, useLocation, useNavigate, useParams } from "react-router-dom";

import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  runtimeSectionFromPath,
  runtimeSectionPath,
  runtimeSections,
  type RuntimeSection,
} from "@/lib/runtime-navigation";

export function RuntimeLayout() {
  const { agentId = "" } = useParams<{ agentId: string }>();
  const location = useLocation();
  const navigate = useNavigate();
  const section = runtimeSectionFromPath(location.pathname);

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden bg-background">
      <div className="flex shrink-0 items-center justify-between gap-3 border-b bg-card px-4 py-3 md:px-6">
        <h1 className="font-serif text-xl italic leading-none text-primary">
          Runtime
        </h1>
        <Select
          value={section}
          onValueChange={(value) =>
            navigate(runtimeSectionPath(agentId, value as RuntimeSection))
          }
        >
          <SelectTrigger
            size="sm"
            className="min-w-36"
            aria-label="Runtime section"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent align="end">
            {runtimeSections.map((item) => (
              <SelectItem key={item.id} value={item.id}>
                {item.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <Outlet />
    </div>
  );
}
