// Connection to a real DeltaView engine. The site works in two modes:
//  - simulation: the in-browser port of the operators (GitHub Pages default);
//  - live: a running engine's HTTP API (/status, /views, SSE, /metrics, /sql).
// When the engine itself serves the site, it injects <meta name="dv-engine">
// and the page connects to its own origin with no setup.
import { useSyncExternalStore } from 'react'

export type Column = { name: string; type: string }
export type ViewInfo = { name: string; sql: string; columns: Column[] }
export type Status = {
  ready: boolean
  applied_lsn: string
  applied_lsn_int: number
  heartbeat: number
  slot_lag_bytes: number
  queue_depth: number
  sql_console?: boolean
  views: string[]
}

export type EngineState = {
  mode: 'sim' | 'connecting' | 'live' | 'error'
  base: string
  token: string | null
  sameOrigin: boolean
  error?: string
  status?: Status
  views: ViewInfo[]
}

const meta = (name: string) => document.querySelector<HTMLMetaElement>(`meta[name="${name}"]`)?.content ?? null

function initial(): EngineState {
  const sameOrigin = meta('dv-engine') === 'same-origin'
  const hash = new URLSearchParams(location.hash.replace(/^#/, ''))
  let base = sameOrigin ? location.origin : hash.get('engine') ?? ''
  if (!base) {
    try { base = localStorage.getItem('dv.engine') ?? '' } catch { /* storage blocked */ }
  }
  return {
    mode: sameOrigin || hash.get('engine') ? 'connecting' : 'sim',
    base: base || 'http://localhost:8080',
    token: meta('dv-token') ?? hash.get('token'),
    sameOrigin,
    views: [],
  }
}

let state: EngineState = initial()
const listeners = new Set<() => void>()
const set = (patch: Partial<EngineState>) => {
  state = { ...state, ...patch }
  listeners.forEach((l) => l())
}

export function useEngine(): EngineState {
  return useSyncExternalStore((l) => (listeners.add(l), () => listeners.delete(l)), () => state)
}

const url = (path: string) => state.base.replace(/\/$/, '') + path

let poll: number | undefined

export async function connect(base?: string, token?: string | null) {
  if (base !== undefined) {
    set({ base })
    try { localStorage.setItem('dv.engine', base) } catch { /* ignore */ }
  }
  if (token !== undefined) set({ token })
  set({ mode: 'connecting', error: undefined })
  window.clearInterval(poll)
  try {
    const [st, views] = await Promise.all([getJSON<Status>('/status'), getJSON<ViewInfo[]>('/views')])
    set({ mode: 'live', status: st, views: views ?? [] })
    poll = window.setInterval(async () => {
      try { set({ status: await getJSON<Status>('/status') }) } catch { set({ mode: 'error', error: 'lost the engine' }) }
    }, 1500)
  } catch (e) {
    set({ mode: 'error', error: e instanceof Error ? e.message : String(e) })
  }
}

export function disconnect() {
  window.clearInterval(poll)
  set({ mode: 'sim', status: undefined, views: [], error: undefined })
}

async function getJSON<T>(path: string): Promise<T> {
  const r = await fetch(url(path), { cache: 'no-store' })
  if (!r.ok) throw new Error(`${path}: HTTP ${r.status}`)
  return r.json()
}

export type Change = { row: (string | number | boolean | null)[]; weight: number }

/** Follows one view: a snapshot at an LSN, then deltas with their LSNs. */
export function subscribe(view: string, h: {
  snapshot: (lsn: string, lsnInt: number, columns: Column[], rows: Change[]) => void
  delta: (lsn: string, lsnInt: number, changes: Change[]) => void
  error?: () => void
}) {
  let last = 0
  const es = new EventSource(url(`/views/${encodeURIComponent(view)}/subscribe`))
  es.addEventListener('snapshot', (ev) => {
    const d = JSON.parse((ev as MessageEvent).data)
    last = d.lsn_int
    h.snapshot(d.lsn, d.lsn_int, d.columns, d.changes)
  })
  es.addEventListener('delta', (ev) => {
    const d = JSON.parse((ev as MessageEvent).data)
    if (d.lsn_int <= last) return // delivery is at-least-once; drop what we have
    last = d.lsn_int
    h.delta(d.lsn, d.lsn_int, d.changes)
  })
  es.addEventListener('resync', () => es.close())
  es.onerror = () => h.error?.()
  return () => es.close()
}

export type SQLResult = { tag: string; columns?: string[]; rows?: (string | null)[][]; more?: boolean }

export async function runSQL(sql: string): Promise<{ results: SQLResult[]; ms: number; error?: string }> {
  const r = await fetch(url('/sql'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-DeltaView-Token': state.token ?? '' },
    body: JSON.stringify({ sql }),
  })
  const body = await r.json().catch(() => ({ error: `HTTP ${r.status}` }))
  if (!r.ok) return { results: [], ms: 0, error: body.error ?? `HTTP ${r.status}` }
  return body
}

/** Parses the engine's Prometheus text into name{labels} -> value. */
export async function metrics(): Promise<Map<string, number>> {
  const text = await (await fetch(url('/metrics'), { cache: 'no-store' })).text()
  const m = new Map<string, number>()
  for (const line of text.split('\n')) {
    if (!line || line.startsWith('#')) continue
    const i = line.lastIndexOf(' ')
    m.set(line.slice(0, i), Number(line.slice(i + 1)))
  }
  return m
}

/** Quantile from the difference of two cumulative Prometheus histograms. */
export function histQuantile(now: Map<string, number>, prev: Map<string, number> | null, name: string, q: number): number | null {
  const buckets: { le: number; c: number }[] = []
  now.forEach((v, k) => {
    const m = k.match(new RegExp(`^${name}_bucket\\{le="([^"]+)"\\}$`))
    if (m) buckets.push({ le: m[1] === '+Inf' ? Infinity : Number(m[1]), c: v - (prev?.get(k) ?? 0) })
  })
  buckets.sort((a, b) => a.le - b.le)
  const total = buckets.at(-1)?.c ?? 0
  if (total <= 0) return null
  const target = q * total
  let lastLe = 0, lastC = 0
  for (const b of buckets) {
    if (b.c >= target) {
      if (b.le === Infinity) return lastLe
      return lastLe + ((target - lastC) / Math.max(1, b.c - lastC)) * (b.le - lastLe)
    }
    lastLe = b.le
    lastC = b.c
  }
  return lastLe
}

// Kick off an automatic connection when the page is served by the engine or
// opened with #engine=... (the link the engine prints).
if (state.mode === 'connecting') void connect()
