import { AnimatePresence, motion } from 'motion/react'
import { useEffect, useRef, useState } from 'react'
import { Sim, fmtLSN, type Entry } from '@/lib/ivm'
import { subscribe, useEngine, type Change, type Column } from '@/lib/engine'
import { EnginePill } from './Hero'
import { Hi, SectionHead, W, fmtNum, fmtRow } from './ui'

type Row = Entry['row']
export type Log = { id: number; lsn: string; head: string; changes: Entry[] }

const numeric = new Set(['smallint', 'integer', 'bigint', 'numeric', 'real', 'double precision'])

/** Rows of one view, kept up to date from a snapshot plus deltas. */
export function useViewStream(view: string | null) {
  const e = useEngine()
  const [columns, setColumns] = useState<Column[]>([])
  const [rows, setRows] = useState<Map<string, Entry>>(new Map())
  const [lsn, setLsn] = useState('—')
  const [count, setCount] = useState(0)
  const [log, setLog] = useState<Log[]>([])
  const [flash, setFlash] = useState<{ keys: Set<string>; n: number }>({ keys: new Set(), n: 0 })
  const nextId = useRef(0)

  const apply = (changes: Entry[], reset = false) => {
    setRows((prev) => {
      const m = reset ? new Map<string, Entry>() : new Map(prev)
      for (const c of changes) {
        const k = JSON.stringify(c.row)
        const w = (m.get(k)?.w ?? 0) + c.w
        if (w <= 0) m.delete(k)
        else m.set(k, { row: c.row, w })
      }
      return m
    })
    setFlash((f) => ({ keys: new Set(changes.filter((c) => c.w > 0).map((c) => JSON.stringify(c.row))), n: f.n + 1 }))
  }
  const toEntries = (cs: Change[]): Entry[] => cs.map((c) => ({ row: c.row as Row, w: c.weight }))

  useEffect(() => {
    setLog([])
    setCount(0)
    if (e.mode === 'live' && view) {
      return subscribe(view, {
        snapshot: (l, _n, cols, cs) => { setColumns(cols); setLsn(l); apply(toEntries(cs), true) },
        delta: (l, _n, cs) => {
          setLsn(l)
          const es = toEntries(cs)
          apply(es)
          setCount((c) => c + 1)
          setLog((g) => [{ id: nextId.current++, lsn: l, head: 'event: delta', changes: es }, ...g].slice(0, 7))
        },
      })
    }
    // Simulation: the in-browser port of the operators, random committed writes.
    const sim = new Sim(42)
    setColumns([{ name: 'city', type: 'text' }, { name: 'orders', type: 'bigint' }, { name: 'revenue', type: 'numeric' }])
    apply(sim.ivm.view.entries(), true)
    setLsn(fmtLSN(sim.lsn))
    const t = setInterval(() => {
      const r = sim.tick()
      setLsn(fmtLSN(r.lsn))
      if (r.trace.view.length === 0) return
      apply(r.trace.view)
      setCount((c) => c + 1)
      setLog((g) => [{ id: nextId.current++, lsn: fmtLSN(r.lsn), head: r.sql, changes: r.trace.view }, ...g].slice(0, 7))
    }, 1100)
    return () => clearInterval(t)
  }, [e.mode, view])

  return { columns, rows, lsn, count, log, flash }
}

export function ViewTable({ columns, rows, flash, max = 14 }: {
  columns: Column[]; rows: Map<string, Entry>; flash: { keys: Set<string>; n: number }; max?: number
}) {
  const barCol = [...columns.keys()].reverse().find((i) => numeric.has(columns[i].type))
  const list = [...rows.entries()].sort((a, b) =>
    barCol !== undefined ? Number(b[1].row[barCol] ?? 0) - Number(a[1].row[barCol] ?? 0) : a[0] < b[0] ? -1 : 1)
  const top = Math.max(1, ...list.map(([, e]) => (barCol !== undefined ? Math.abs(Number(e.row[barCol] ?? 0)) : 0)))
  const shown = list.slice(0, max)
  // A stable identity per row, so a group whose numbers change moves and
  // updates in place instead of disappearing and reappearing: the first
  // column when it is unique (city, id, status), otherwise the whole row.
  const firsts = shown.map(([, e]) => JSON.stringify(e.row[0]))
  const byFirst = new Set(firsts).size === firsts.length
  const grid = { gridTemplateColumns: `repeat(${columns.length}, minmax(5.5rem, 1fr)) 2.5rem` }
  return (
    <div className="overflow-x-auto">
      <div className="min-w-[480px] font-mono text-[13px]">
        <div className="grid border-b hairline pb-2 text-fg-3" style={grid}>
          {columns.map((c, i) => (
            <div key={i} className={`truncate px-2 ${numeric.has(c.type) ? 'text-right' : ''}`}>
              {c.name}<span className="ml-1.5 text-[10px] opacity-60">{c.type}</span>
            </div>
          ))}
          <div className="text-right">×</div>
        </div>
        <AnimatePresence initial={false} mode="popLayout">
          {shown.map(([k, e], idx) => (
            <motion.div key={byFirst ? firsts[idx] : k} layout="position"
              initial={{ opacity: 0, x: -10 }} animate={{ opacity: 1, x: 0 }} exit={{ opacity: 0, transition: { duration: 0.2 } }}
              transition={{ layout: { duration: 0.5, ease: [0.22, 1, 0.36, 1] } }}
              className="relative grid items-center border-b hairline" style={grid}>
              {flash.keys.has(k) && (
                <motion.span key={flash.n} initial={{ opacity: 1 }} animate={{ opacity: 0 }} transition={{ duration: 1.8 }}
                  className="pointer-events-none absolute inset-0 bg-gradient-to-r from-lime/20 to-transparent" />
              )}
              {e.row.map((v, i) => (
                <div key={i} className={`relative truncate px-2 py-2.5 ${numeric.has(columns[i]?.type) ? 'text-right tabular' : ''}`}>
                  {i === barCol && (
                    <motion.span className="absolute inset-y-2 right-0 rounded-l-sm bg-plus/25"
                      animate={{ width: `${(Math.abs(Number(v ?? 0)) / top) * 100}%` }} transition={{ duration: 0.6 }} />
                  )}
                  <span className="relative">{v === null ? <span className="text-fg-3">NULL</span> : typeof v === 'number' && !Number.isInteger(v) ? fmtNum(v) : String(v)}</span>
                </div>
              ))}
              <div className="py-2.5 text-right text-fg-3">{e.w}</div>
            </motion.div>
          ))}
        </AnimatePresence>
      </div>
      {list.length === 0 && <p className="py-6 font-mono text-[12px] text-fg-3">no rows yet</p>}
      {list.length > max && <p className="mt-2 font-mono text-[11px] text-fg-3">+ {list.length - max} more rows</p>}
    </div>
  )
}

export function DeltaLog({ log, className = 'h-[440px]' }: { log: Log[]; className?: string }) {
  return (
    <div className={`overflow-hidden rounded-xl border hairline bg-[#080908] p-4 font-mono text-[12px] leading-6 ${className}`}>
      {log.length === 0 && <p className="text-fg-3">waiting for the next committed change…</p>}
      <AnimatePresence initial={false} mode="popLayout">
        {log.map((l) => (
          <motion.div key={l.id} layout="position" initial={{ opacity: 0, y: -14 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }}
            transition={{ duration: 0.4, ease: [0.22, 1, 0.36, 1] }}
            className="border-b border-line py-2 last:border-0">
            <div className="flex justify-between gap-3 text-fg-3"><span className="truncate">{l.head}</span><span>{l.lsn}</span></div>
            {l.changes.slice(0, 6).map((c, k) => (
              <div key={k} className="flex items-center gap-2 text-fg"><W w={c.w} /> <span className="truncate">{fmtRow(c.row)}</span></div>
            ))}
          </motion.div>
        ))}
      </AnimatePresence>
    </div>
  )
}

export function Live() {
  const e = useEngine()
  const [view, setView] = useState<string | null>(null)
  useEffect(() => {
    if (e.mode === 'live' && e.views.length && !e.views.some((v) => v.name === view)) {
      setView(e.views.find((v) => v.name === 'revenue_by_city')?.name ?? e.views[0].name)
    }
  }, [e.mode, e.views, view])
  const live = e.mode === 'live'
  const s = useViewStream(live ? view : null)

  return (
    <section id="live" className="mx-auto max-w-[1400px] px-5 pt-36 md:px-10">
      <SectionHead
        n="04"
        label="Live"
        title={<>A view you can <Hi>subscribe</Hi> to.</>}
        kicker="A snapshot at one LSN, then every delta after it, each tagged with its LSN. Connect your running engine and this section shows your own views, changing as your database changes."
      />
      <div className="mt-12 flex flex-col gap-3 rounded-xl border hairline bg-bg-2 px-4 py-3 md:flex-row md:items-center md:justify-between">
        <span className="font-mono text-[12px] text-fg-2">
          {live ? <>connected to <span className="text-fg">{e.base}</span> · {e.views.length} views</> : 'simulating revenue_by_city in your browser · connect an engine to see yours'}
        </span>
        <div className="flex items-center gap-3">
          <a href="console.html" className="text-[13px] text-lime hover:underline">full console →</a>
          <EnginePill compact />
        </div>
      </div>
      {live && (
        <div className="mt-4 flex flex-wrap gap-2">
          {e.views.map((v) => (
            <button key={v.name} onClick={() => setView(v.name)}
              className={`rounded-full border px-3 py-1 font-mono text-[12px] transition-colors ${v.name === view ? 'border-lime bg-lime text-bg' : 'hairline text-fg-2 hover:text-fg'}`}>
              {v.name}
            </button>
          ))}
        </div>
      )}
      <div className="mt-8 grid grid-cols-12 gap-x-6 gap-y-10">
        <div className="col-span-12 lg:col-span-7">
          <div className="mb-4 flex items-end justify-between">
            <p className="eyebrow">{live ? view : 'revenue_by_city'}</p>
            <p className="font-mono text-[12px] text-fg-3">valid at <span className="text-fg tabular">{s.lsn}</span> · {s.count} deltas</p>
          </div>
          <ViewTable columns={s.columns} rows={s.rows} flash={s.flash} />
        </div>
        <div className="col-span-12 lg:col-span-5">
          <p className="eyebrow mb-4">Delta stream</p>
          <DeltaLog log={s.log} />
        </div>
      </div>
    </section>
  )
}
