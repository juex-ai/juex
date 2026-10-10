import { useEffect, useRef, useState } from 'react'
import { APIError, api, errorText } from './api'
import { reconcileProgress } from './timeline'
import type { Event, ModelProgress, Thread, Timeline } from './schema'

type State = { events: Event[]; progress: ModelProgress[]; thread?: Thread; error?: string; previousSequence: number; hasPrevious: boolean; loadingPrevious: boolean; historyError?: string }
const empty: State = { events: [], progress: [], previousSequence: 0, hasPrevious: false, loadingPrevious: false }

export function useTimeline(base: string, thread: string, revision: number) {
  const [state, setState] = useState<State>(empty)
  const loadPrevious = useRef<() => Promise<void>>(async () => {})
  useEffect(() => {
    const controller = new AbortController()
    let cursor: number | undefined
    let previousSequence = 0
    let hasPrevious = false
    let loadingPrevious = false
    let events: Event[] = []
    let progress: ModelProgress[] = []
    let timer: number | undefined
    let failures = 0
    const merge = (page: Event[]) => { events = [...new Map([...events, ...page].map(event => [event.id, event])).values()].sort((a, b) => a.sequence - b.sequence) }
    const endpoint = `${base}/threads/${thread}/events`
    const poll = async () => {
      try {
        const initial = cursor === undefined
        const value = await api<Timeline>(`${endpoint}?${initial ? 'before=0' : `after=${cursor}`}&limit=200`, undefined, undefined, controller.signal)
        if (controller.signal.aborted) return
        cursor = value.next_sequence; merge(value.events)
        if (initial) { previousSequence = value.previous_sequence ?? value.events[0]?.sequence ?? 0; hasPrevious = value.has_previous ?? false }
        progress = reconcileProgress(progress, value)
        failures = 0
        setState(previous => ({ ...previous, events, progress, thread: value.thread, error: undefined, previousSequence, hasPrevious }))
        timer = window.setTimeout(() => void poll(), value.has_more ? 0 : 750)
      } catch (error) {
        if (controller.signal.aborted) return
        setState(previous => ({ ...previous, error: errorText(error) }))
        if (!(error instanceof APIError) || error.status >= 500 || error.status === 429) {
          failures += 1
          timer = window.setTimeout(() => void poll(), Math.min(15_000, 1000 * 2 ** Math.min(failures, 4)))
        }
      }
    }
    loadPrevious.current = async () => {
      if (loadingPrevious || !hasPrevious || !previousSequence) return
      loadingPrevious = true
      setState(previous => ({ ...previous, loadingPrevious: true, historyError: undefined }))
      try {
        const value = await api<Timeline>(`${endpoint}?before=${previousSequence}&limit=200`, undefined, undefined, controller.signal)
        if (controller.signal.aborted) return
        merge(value.events)
        previousSequence = value.previous_sequence ?? previousSequence
        hasPrevious = value.has_previous ?? false
        // The history response may precede a newer poll. It never replaces the
        // current Thread or progress, and it never moves the forward cursor.
        setState(previous => ({ ...previous, events, previousSequence, hasPrevious }))
      } catch (error) {
        if (!controller.signal.aborted) setState(previous => ({ ...previous, historyError: errorText(error) }))
      } finally {
        loadingPrevious = false
        if (!controller.signal.aborted) setState(previous => ({ ...previous, loadingPrevious: false }))
      }
    }
    setState(empty)
    void poll()
    return () => { controller.abort(); window.clearTimeout(timer); loadPrevious.current = async () => {} }
  }, [base, thread, revision])
  return { ...state, loadPrevious: () => loadPrevious.current() }
}
