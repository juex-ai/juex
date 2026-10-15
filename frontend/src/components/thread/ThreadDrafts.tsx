import { createContext, useCallback, useContext, useState, useSyncExternalStore, type ReactNode } from "react";

// Unsent text belongs to the mounted application, not to a route or server history.
function createDraftStore() {
  const drafts = new Map<string, string>();
  const listeners = new Set<() => void>();
  return {
    read: (key: string) => drafts.get(key) ?? "",
    update(key: string, value: string | ((current: string) => string)) {
      const current = drafts.get(key) ?? "";
      const next = typeof value === "function" ? value(current) : value;
      if (current === next) return;
      if (next) drafts.set(key, next);
      else drafts.delete(key);
      listeners.forEach((listener) => listener());
    },
    subscribe(listener: () => void) {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    },
  };
}
const DraftContext = createContext<ReturnType<typeof createDraftStore> | null>(null);

export function ThreadDraftsProvider({ children }: { children: ReactNode }) {
  const [store] = useState(createDraftStore);
  return <DraftContext.Provider value={store}>{children}</DraftContext.Provider>;
}

export function useThreadDraft(agentID: string, threadID: string) {
  const store = useContext(DraftContext);
  if (!store) throw new Error("Thread drafts require ThreadDraftsProvider");
  const key = JSON.stringify([agentID, threadID]);
  const draft = useSyncExternalStore(store.subscribe, () => store.read(key));
  const setDraft = useCallback((value: string | ((current: string) => string)) => {
    store.update(key, value);
  }, [key, store]);
  return [draft, setDraft] as const;
}
