import { useEffect, useState } from 'react'
import { api, errorText } from './api'

export function useResource<T>(path: string | null, revision = 0, pollInterval = 0): { key: string; data?: T; error?: string } {
  const key = `${path}:${revision}`
  const [state, setState] = useState<{ key: string; path: string; data?: T; error?: string }>({ key: '', path: '' })
  useEffect(() => {
    if (path === null) return
    const controller = new AbortController()
    let timer: number | undefined
    const read = () => { void api<T>(path, undefined, undefined, controller.signal)
      .then(data => { if (!controller.signal.aborted) setState({ key, path, data }) })
      .catch(error => { if (!controller.signal.aborted) setState({ key, path, error: errorText(error) }) })
      .finally(() => { if (pollInterval > 0 && !controller.signal.aborted) timer = window.setTimeout(read, pollInterval) })
    }
    read()
    return () => { controller.abort(); window.clearTimeout(timer) }
  }, [path, key, pollInterval])
  return state.path === path ? state : { key }
}
