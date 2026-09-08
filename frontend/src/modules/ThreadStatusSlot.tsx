import { Component, type ReactNode } from "react";
import { useThreadModules } from "@/hooks/use-thread-modules";
import { moduleContributions } from "./registry";
import { resolveContributions } from "./resolve";

export function ThreadStatusSlot({ threadID }: { threadID: string }) {
  const { snapshot, error, scope } = useThreadModules(threadID);
  const resolved = resolveContributions(snapshot, moduleContributions);
  return <>
    {error ? <span role="status" title={error} className="text-xs text-muted-foreground">Module state unavailable</span> : null}
    {!snapshot && !error ? <span role="status" className="text-xs text-muted-foreground">Loading modules…</span> : null}
    {resolved.diagnostics.map((message) => <span key={message} role="status" className="text-xs text-muted-foreground">{message}</span>)}
    {resolved.status.map(({ definition, state }) => <RendererBoundary key={`${scope}:${snapshot?.composition_revision}:${definition.id}`} label={definition.label} revision={state.revision}>
      <definition.Component state={state} readOnly={snapshot?.read_only ?? true} stale={error} />
    </RendererBoundary>)}
  </>;
}

type RendererBoundaryProps = { label: string; revision: string; children: ReactNode };

class RendererBoundary extends Component<RendererBoundaryProps, { failed: boolean; revision: string }> {
  state = { failed: false, revision: this.props.revision };
  static getDerivedStateFromProps(props: RendererBoundaryProps, state: { revision: string }) {
    return props.revision === state.revision ? null : { failed: false, revision: props.revision };
  }
  static getDerivedStateFromError() { return { failed: true }; }
  render() {
    return this.state.failed ? <span role="status" className="text-xs text-muted-foreground">{this.props.label} unavailable: display error</span> : this.props.children;
  }
}
