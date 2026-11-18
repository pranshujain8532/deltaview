import { StrictMode, useEffect, useMemo, useRef, useState } from 'react'
import { createRoot } from 'react-dom/client'
import { AnimatePresence, MotionConfig, motion } from 'motion/react'
import './index.css'
import { connect, histQuantile, metrics, runSQL, useEngine, type SQLResult } from '@/lib/engine'
import { EnginePill } from '@/sections/Hero'
import { DeltaLog, ViewTable, useViewStream } from '@/sections/Live'
import { DeltaFlow, type DeltaFlowHandle } from '@/components/DeltaFlow'
import { wavesFromDelta } from '@/sections/ui'
import { GITHUB } from '@/lib/data'

const fmtBytes = (n: number) => (n < 1024 ? `${n} B` : n < 1 << 20 ? `${(n / 1024).toFixed(1)} KB` : n < 1 << 30 ? `${(n / (1 << 20)).toFixed(1)} MB` : `${(n / (1 << 30)).toFixed(2)} GB`)
const fmtMs = (s: number | null) => (s === null ? '—' : s < 0.001 ? `${(s * 1e6).toFixed(0)} µs` : `${(s * 1000).toFixed(s < 0.01 ? 2 : 1)} ms`)

function Spark({ data, className = '' }: { data: number[]; className?: string }) {
  const w = 120, h = 28
  if (data.length < 2) return <svg viewBox={`0 0 ${w} ${h}`} className={`h-7 w-[72px] sm:w-[120px] ${className}`} />
  const max = Math.max(...data, 1e-9)
  const pts = data.map((v, i) => [(i / (data.length - 1)) * w, h - 2 - (v / max) * (h - 4)])
  const d = pts.map(([x, y], i) => `${i ? 'L' : 'M'}${x.toFixed(1)},${y.toFixed(1)}`).join(' ')
  return (
    <svg viewBox={`0 0 ${w} ${h}`} preserveAspectRatio="none" className={`h-7 w-[72px] sm:w-[120px] ${className}`} aria-hidden>
      <path d={`${d} L${w},${h} L0,${h} Z`} fill="var(--color-lime)" opacity="0.08" />
      <path d={d} fill="none" stroke="var(--color-lime)" strokeWidth="1.5" strokeLinejoin="round" />
    </svg>
  )
}

type Health = { tps: number[]; rps: number[]; p50: number | null; p99: number | null; p50s: number[]; subs: number; state: number; commitP50: number | null }

function useHealth(live: boolean) {
  const [h, setH] = useState<Health>({ tps: [], rps: [], p50: null, p99: null, p50s: [], subs: 0, state: 0, commitP50: null })
  useEffect(() => {
    if (!live) return
    let prev: Map<string, number> | null = null
    let prevT = 0
    const tick = async () => {
      try {
        const m = await metrics()
        const t = performance.now()
        if (prev) {
          const dt = (t - prevT) / 1000
          const rate = (k: string) => Math.max(0, ((m.get(k) ?? 0) - (prev!.get(k) ?? 0)) / dt)
          const p50 = histQuantile(m, prev, 'deltaview_end_to_end_seconds', 0.5)
          const p99 = histQuantile(m, prev, 'deltaview_end_to_end_seconds', 0.99)
          const c50 = histQuantile(m, prev, 'deltaview_commit_seconds', 0.5)
          setH((o) => ({
            tps: [...o.tps, rate('deltaview_transactions_applied_total')].slice(-60),
            rps: [...o.rps, rate('deltaview_rows_applied_total')].slice(-60),
            p50: p50 ?? o.p50, p99: p99 ?? o.p99, commitP50: c50 ?? o.commitP50,
            p50s: [...o.p50s, (p50 ?? 0) * 1000].slice(-60),
            subs: m.get('deltaview_subscribers') ?? 0,
            state: m.get('deltaview_state_bytes') ?? 0,
          }))
        }
        prev = m
        prevT = t
      } catch { /* engine briefly unreachable */ }
    }
    void tick()
    const id = setInterval(tick, 2000)
    return () => clearInterval(id)
  }, [live])
  return h
}

function Stat({ label, value, sub, spark }: { label: string; value: string; sub?: string; spark?: number[] }) {
  return (
    <div className="panel flex flex-col justify-between gap-3 p-4">
      <p className="eyebrow">{label}</p>
      <div className="flex items-end justify-between gap-2">
        <div>
          <motion.p key={value} initial={{ opacity: 0.4, y: 4 }} animate={{ opacity: 1, y: 0 }} className="display text-[26px] leading-none tabular">{value}</motion.p>
          {sub && <p className="mt-1.5 font-mono text-[11px] text-fg-3">{sub}</p>}
        </div>
        {spark && <Spark data={spark} className="shrink-0" />}
      </div>
    </div>
  )
}

const cities = ['Delhi', 'Mumbai', 'Pune', 'Goa', 'Jaipur', 'Agra']
const rnd = (n: number) => Math.floor(Math.random() * n)
const presets = () => [
  { label: 'New paid order', sql: `INSERT INTO orders VALUES (${1000 + rnd(9000)}, ${1 + rnd(40)}, 'paid', ${(20 + Math.random() * 400).toFixed(2)}, now())` },
  { label: 'Customer moves city', sql: `UPDATE customers SET city = '${cities[rnd(cities.length)]}' WHERE id = (SELECT id FROM customers ORDER BY random() LIMIT 1)` },
  { label: 'Refund an order', sql: `UPDATE orders SET status = 'refunded' WHERE id = (SELECT id FROM orders WHERE status = 'paid' ORDER BY random() LIMIT 1)` },
  { label: 'Delete an order', sql: `DELETE FROM orders WHERE id = (SELECT id FROM orders ORDER BY random() LIMIT 1)` },
  { label: 'Ten orders at once', sql: `INSERT INTO orders SELECT 20000 + g + (random() * 1e6)::int, 1 + (random() * 39)::int, 'paid', round((random() * 300)::numeric, 2), now() FROM generate_series(1, 10) g ON CONFLICT DO NOTHING` },
  { label: 'Peek at orders', sql: `SELECT * FROM orders ORDER BY created_at DESC NULLS LAST LIMIT 8` },
]

function SQLConsole({ enabled, demo }: { enabled: boolean; demo: boolean }) {
  const [sql, setSql] = useState("UPDATE orders SET status = 'paid' WHERE id = 1")
  const [busy, setBusy] = useState(false)
  const [out, setOut] = useState<{ id: number; sql: string; results: SQLResult[]; ms: number; error?: string }[]>([])
  const ps = useMemo(presets, [out.length])
  const run = async (text = sql) => {
    setBusy(true)
    const r = await runSQL(text).catch((e) => ({ results: [], ms: 0, error: String(e) }))
    setOut((o) => [{ id: Date.now(), sql: text, ...r }, ...o].slice(0, 6))
    setBusy(false)
  }
  if (!enabled) {
    return (
      <div className="panel p-6">
        <p className="display text-xl">Run SQL from here</p>
        <p className="mt-2 max-w-2xl text-[14px] leading-relaxed text-fg-2">
          This engine was started without the SQL console, so the page is read-only. Make a change in <span className="font-mono text-fg">psql</span> and
          watch it arrive above, or restart the engine with the console on:
        </p>
        <pre className="mt-4 overflow-x-auto rounded-lg border hairline bg-[#080908] p-3 font-mono text-[12.5px] text-fg-2">./bin/deltaview --sql-console{'\n'}# or: DV_SQL_CONSOLE=1 docker compose up</pre>
        <p className="mt-3 text-[12.5px] text-fg-3">The console needs a token that the engine injects only into pages it serves, so no other website can run SQL on your database.</p>
      </div>
    )
  }
  return (
    <div className="panel overflow-hidden">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b hairline px-4 py-3">
        <p className="eyebrow">SQL · runs on Postgres, reaches the views through the WAL</p>
        {demo && (
          <div className="flex flex-wrap gap-1.5">
            {ps.map((p) => (
              <button key={p.label} onClick={() => { setSql(p.sql); void run(p.sql) }} disabled={busy}
                className="rounded-full border hairline px-2.5 py-1 text-[12px] text-fg-2 transition-colors hover:border-lime hover:text-lime disabled:opacity-40">
                {p.label}
              </button>
            ))}
          </div>
        )}
      </div>
      <div className="grid grid-cols-1 lg:grid-cols-2">
        <div className="border-b hairline p-4 lg:border-r lg:border-b-0">
          <textarea value={sql} onChange={(e) => setSql(e.target.value)} spellCheck={false} rows={7}
            onKeyDown={(e) => { if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') void run() }}
            className="w-full resize-y rounded-lg border hairline bg-[#080908] p-3 font-mono text-[13px] leading-6 text-fg outline-none focus:border-lime" />
          <div className="mt-3 flex items-center justify-between">
            <span className="font-mono text-[11px] text-fg-3">Ctrl/⌘ + Enter</span>
            <button onClick={() => void run()} disabled={busy} className="rounded-lg bg-lime px-4 py-2 text-[13px] font-semibold text-bg disabled:opacity-50">
              {busy ? 'Running…' : 'Run'}
            </button>
          </div>
        </div>
        <div className="max-h-[300px] overflow-auto p-4 font-mono text-[12px]">
          {out.length === 0 && <p className="text-fg-3">Results appear here. Writes show up in the view and the delta stream within milliseconds.</p>}
          <AnimatePresence initial={false}>
            {out.map((o) => (
              <motion.div key={o.id} initial={{ opacity: 0, y: -6 }} animate={{ opacity: 1, y: 0 }} className="mb-4 border-b border-line pb-3">
                <p className="truncate text-fg-3">=# {o.sql}</p>
                {o.error && <p className="mt-1 text-minus-hi">ERROR: {o.error}</p>}
                {o.results.map((r, i) => (
                  <div key={i} className="mt-1">
                    {r.columns && r.rows ? (
                      <table className="mt-1 w-full text-[11.5px]">
                        <thead><tr className="text-fg-3">{r.columns.map((c) => <th key={c} className="pr-3 text-left font-normal">{c}</th>)}</tr></thead>
                        <tbody>{r.rows.map((row, k) => <tr key={k}>{row.map((v, j) => <td key={j} className="pr-3 text-fg-2">{v ?? 'NULL'}</td>)}</tr>)}</tbody>
                      </table>
                    ) : null}
                    <p className="text-plus-hi">{r.tag} <span className="text-fg-3">· {o.ms} ms</span></p>
                  </div>
                ))}
              </motion.div>
            ))}
          </AnimatePresence>
        </div>
      </div>
    </div>
  )
}

function Disconnected() {
  const e = useEngine()
  const [url, setUrl] = useState(e.base)
  return (
    <div className="mx-auto max-w-3xl px-5 pt-32 pb-24 text-center">
      <p className="eyebrow">Console</p>
      <h1 className="display mt-4 text-[clamp(2.4rem,6vw,4.6rem)] leading-[0.95]">Connect your <span className="text-lime">engine</span>.</h1>
      <p className="mx-auto mt-6 max-w-xl text-lg text-fg-2">
        The console reads a running DeltaView: its views, live deltas, health and metrics. Nothing here is recorded or simulated.
      </p>
      <form className="mx-auto mt-10 flex max-w-lg gap-2" onSubmit={(ev) => { ev.preventDefault(); void connect(url) }}>
        <input value={url} onChange={(ev) => setUrl(ev.target.value)} aria-label="Engine URL"
          className="min-w-0 flex-1 rounded-lg border hairline bg-bg-2 px-3 py-3 font-mono text-[14px] outline-none focus:border-lime" />
        <button className="rounded-lg bg-lime px-5 text-[14px] font-semibold text-bg">{e.mode === 'connecting' ? 'Connecting…' : 'Connect'}</button>
      </form>
      {e.mode === 'error' && <p className="mt-3 font-mono text-[12px] text-minus-hi">{e.error}</p>}
      <div className="panel mx-auto mt-14 max-w-xl p-5 text-left">
        <p className="eyebrow">No engine yet? Two commands</p>
        <pre className="mt-3 overflow-x-auto font-mono text-[12.5px] leading-6 text-fg-2">{`git clone ${GITHUB}
cd deltaview && docker compose up
# then open http://localhost:8080/console`}</pre>
        <p className="mt-3 text-[12.5px] leading-relaxed text-fg-3">
          Opened from the engine itself, this page connects on its own. From this public site, your browser may ask to allow access to
          localhost; DeltaView's API is read-only, and running SQL needs the token the engine prints.
        </p>
      </div>
    </div>
  )
}

function Dashboard() {
  const e = useEngine()
  const h = useHealth(e.mode === 'live')
  const [view, setView] = useState<string | null>(null)
  useEffect(() => {
    if (e.views.length && !e.views.some((v) => v.name === view)) setView(e.views[0].name)
  }, [e.views, view])
  const s = useViewStream(view)
  const flow = useRef<DeltaFlowHandle>(null)
  const seen = useRef(0)
  useEffect(() => {
    const l = s.log[0]
    if (l && l.id !== seen.current) {
      seen.current = l.id
      flow.current?.push(wavesFromDelta(l.changes.map((c) => c.w)), `${view}: ${l.changes.length} changed rows at ${l.lsn}`)
    }
  }, [s.log, view])
  const info = e.views.find((v) => v.name === view)
  const st = e.status
  const demo = e.views.some((v) => /\borders\b/.test(v.sql))
  const last = (a: number[]) => (a.length ? a[a.length - 1] : 0)

  return (
    <div className="mx-auto max-w-[1500px] px-5 pt-20 pb-24 md:px-8">
      <div className="grid grid-cols-2 gap-3 md:grid-cols-4 xl:grid-cols-8">
        <Stat label="Applied LSN" value={st?.applied_lsn ?? '—'} sub={st?.ready ? 'snapshot done' : 'snapshotting…'} />
        <Stat label="Heartbeat" value={String(st?.heartbeat ?? 0)} sub="dv_heartbeat.seq" />
        <Stat label="Slot lag" value={fmtBytes(st?.slot_lag_bytes ?? 0)} sub="WAL pinned by the slot" />
        <Stat label="Queue" value={String(st?.queue_depth ?? 0)} sub="txns waiting to apply" />
        <Stat label="Transactions/s" value={last(h.tps).toFixed(1)} spark={h.tps} />
        <Stat label="Row changes/s" value={last(h.rps).toFixed(1)} spark={h.rps} />
        <Stat label="Commit → view" value={fmtMs(h.p50)} sub={`p99 ${fmtMs(h.p99)}`} spark={h.p50s} />
        <Stat label="State" value={fmtBytes(h.state)} sub={`${h.subs} subscribers`} />
      </div>

      <div className="mt-3 panel overflow-hidden">
        <div className="flex items-center justify-between border-b hairline px-4 py-2.5">
          <span className="eyebrow">{view} · deltas as they commit</span>
          <span className="font-mono text-[11px] text-fg-3">valid at <span className="text-fg">{s.lsn}</span> · {s.count} deltas this session</span>
        </div>
        <DeltaFlow ref={flow} height={220} />
      </div>

      <div className="mt-3 grid grid-cols-12 gap-3">
        <aside className="panel col-span-12 p-3 lg:col-span-3">
          <p className="eyebrow px-2 pt-1 pb-3">Views · {e.views.length}</p>
          {e.views.map((v) => (
            <button key={v.name} onClick={() => setView(v.name)}
              className={`mb-1 flex w-full items-center justify-between rounded-lg px-3 py-2 text-left transition-colors ${v.name === view ? 'bg-lime text-bg' : 'text-fg-2 hover:bg-bg-3 hover:text-fg'}`}>
              <span className="truncate font-mono text-[12.5px]">{v.name}</span>
              <span className={`font-mono text-[10px] ${v.name === view ? 'text-bg/70' : 'text-fg-3'}`}>{v.columns.length} cols</span>
            </button>
          ))}
        </aside>
        <section className="panel col-span-12 p-5 lg:col-span-6">
          <div className="mb-4 flex items-baseline justify-between gap-4">
            <h2 className="display text-2xl">{view}</h2>
            <span className="font-mono text-[11px] text-fg-3">{s.rows.size} distinct rows</span>
          </div>
          <ViewTable columns={s.columns} rows={s.rows} flash={s.flash} max={18} />
          {info && <pre className="mt-5 overflow-x-auto rounded-lg border hairline bg-[#080908] p-3 font-mono text-[11.5px] leading-5 text-fg-3">{info.sql}</pre>}
        </section>
        <section className="col-span-12 lg:col-span-3">
          <DeltaLog log={s.log} className="h-full min-h-[420px]" />
        </section>
      </div>

      <div className="mt-3">
        <SQLConsole enabled={!!st?.sql_console} demo={demo} />
      </div>
    </div>
  )
}

function App() {
  const e = useEngine()
  return (
    <MotionConfig reducedMotion="user">
      <div className="grain min-h-screen grid-bg">
        <header className="fixed inset-x-0 top-0 z-50 border-b hairline bg-bg/80 backdrop-blur-xl">
          <div className="mx-auto flex h-14 max-w-[1500px] items-center justify-between px-5 md:px-8">
            <a href="./" className="flex items-center gap-2.5">
              <svg width="20" height="20" viewBox="0 0 32 32" aria-hidden><path d="M16 5 28 27H4Z" fill="none" stroke="var(--color-lime)" strokeWidth="2.6" strokeLinejoin="round" /></svg>
              <span className="display text-[16px]">DeltaView</span>
              <span className="font-mono text-[12px] text-fg-3">/ console</span>
            </a>
            <div className="flex items-center gap-3">
              <a href="./" className="hidden text-[13px] text-fg-2 hover:text-fg sm:block">How it works</a>
              <EnginePill />
            </div>
          </div>
        </header>
        {e.mode === 'live' ? <Dashboard /> : <Disconnected />}
      </div>
    </MotionConfig>
  )
}

createRoot(document.getElementById('root')!).render(<StrictMode><App /></StrictMode>)
