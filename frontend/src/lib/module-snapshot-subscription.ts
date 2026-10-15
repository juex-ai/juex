import type { ThreadModulesSnapshot } from "../module-schema.ts";

// Each subscription owns its request epoch. A stream baseline takes precedence
// over its concurrent GET; disposal rejects late callbacks from either source.
export function startModuleSnapshotSubscription(ports: {
  threadID: string;
  agentID?: string;
  load: (signal: AbortSignal) => Promise<ThreadModulesSnapshot>;
  subscribe: (receive: (snapshot: ThreadModulesSnapshot) => void, onError: (error: unknown) => void) => () => void;
  onSnapshot: (snapshot: ThreadModulesSnapshot) => void;
  onError: (error: unknown) => void;
}): () => void {
  let active = true;
  let receivedBaseline = false;
  let streamFailed = false;
  let revision = "";
  const abort = new AbortController();
  const receive = (snapshot: ThreadModulesSnapshot) => {
    if (!active || snapshot.thread_id !== ports.threadID ||
      (ports.agentID && snapshot.agent_id !== ports.agentID)) return false;
    if (snapshot.revision !== revision || streamFailed) {
      streamFailed = false;
      revision = snapshot.revision;
      ports.onSnapshot(snapshot);
    }
    return true;
  };
  const close = ports.subscribe((snapshot) => {
    if (receive(snapshot)) receivedBaseline = true;
  }, (error) => {
    if (!active) return;
    streamFailed = true;
    ports.onError(error);
  });
  void ports.load(abort.signal).then((snapshot) => {
    if (!receivedBaseline && !streamFailed) receive(snapshot);
  }).catch((error: unknown) => {
    if (active && !receivedBaseline) ports.onError(error);
  });
  return () => { active = false; abort.abort(); close(); };
}
