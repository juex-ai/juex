import { createContext, useContext, useState, useSyncExternalStore, type ReactNode } from 'react'
import { createDraftStore } from './draft-store'

const DraftContext = createContext<ReturnType<typeof createDraftStore> | null>(null)

export function ConversationDrafts({ children }: { children: ReactNode }) {
  const [store] = useState(() => {
    try { return createDraftStore(window.sessionStorage) } catch { return createDraftStore() }
  })
  return <DraftContext.Provider value={store}>{children}</DraftContext.Provider>
}

export function useConversationDraft(key: string) {
  const store = useContext(DraftContext)
  if (!store) throw new Error('Conversation requires a workspace draft store')
  const draft = useSyncExternalStore(store.subscribe, () => store.read(key))
  return { draft, store }
}
