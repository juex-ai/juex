import { useEffect, useState } from 'react'
import { api, errorText } from './api'

export function useResource<T>(path: string, revision = 0): { key: string; data?: T; error?: string } {
  const key = `${path}:${revision}`
  const [state, setState] = useState<{ key: string; path: string; data?: T; error?: string }>({ key: '', path: '' })
  useEffect(() => {
    const controller = new AbortController()
    api<T>(path, undefined, undefined, controller.signal)
      .then(data => setState({ key, path, data }))
      .catch(error => { if (!controller.signal.aborted) setState({ key, path, error: errorText(error) }) })
    return () => controller.abort()
  }, [path, key])
  return state.path === path ? state : { key }
}
