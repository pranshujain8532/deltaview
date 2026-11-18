import { AnimatePresence, motion } from 'motion/react'
import { useEffect, useRef, useState } from 'react'
import { Sim, fmtLSN, type Entry, type Row } from '@/lib/ivm'
import { SectionHead, Serif, W, fmtNum, fmtRow } from './ui'

type Change = { row: Row; weight: number }
type Log = { id: number; lsn: string; head: string; changes: Entry[] }

export function Live() {
  const [url, setUrl] = useState('http://localhost:8080')
  const [mode, setMode] = useState<'sim' | 'connecting' | 'live' | 'failed'>('sim')
  const [rows, setRows] = useState<Map<string, Entry>>(new Map())
  const [flash, setFlash] = useState<Set<string>>(new Set())
  const [lsn, setLsn] = useState('—')
  const [count, setCount] = useState(0)
  const [log, setLog] = useState<Log[]>([])
  const last = useRef(0)
  const nextId = useRef(0)

  const apply = (changes: Entry[], reset = false) => {
    setRows((prev) => {
      const m = reset ? new Map<string, Entry>() : new Map(prev)
      for (const c of changes) {
        const k = JSON.stringify(c.row)
        const e = m.get(k)
        const w = (e?.w ?? 0) + c.w
        if (w <= 0) m.delete(k)
        else m.set(k, { row: c.row, w })
      }
      return m
    })
    setFlash(new Set(changes.filter((c) => c.w > 0).map((c) => String(c.row[0]))))
  }

  // In-browser simulation: the same delta rules, random committed writes.
  useEffect(() => {
    if (mode !== 'sim' && mode !== 'failed') return
    const sim = new Sim(42)
    apply(sim.ivm.view.entries(), true)
    setLsn(fmtLSN(sim.lsn))
    const t = setInterval(() => {
      const r = sim.tick()
      setLsn(fmtLSN(r.lsn))
      if (r.trace.view.length === 0) return
      apply(r.trace.view)
      setCount((c) => c + 1)
      setLog((l) => [{ id: nextId.current++, lsn: fmtLSN(r.lsn), head: r.sql, changes: r.trace.view }, ...l].slice(0, 6))
    }, 900)
    return () => clearInterval(t)
  }, [mode])

  // A real engine: GET /views/revenue_by_city/subscribe (server-sent events).
  useEffect(() => {
    if (mode !== 'connecting' && mode !== 'live') return
    const es = new EventSource(`${url.replace(/\/$/, '')}/views/revenue_by_city/subscribe`)
    const toEntries = (cs: Change[]) => cs.map((c) => ({ row: c.row, w: c.weight }))
    es.addEventListener('snapshot', (ev) => {
      const d = JSON.parse((ev as MessageEvent).data)
      last.current = d.lsn_int
      setLsn(d.lsn)
      apply(toEntries(d.changes), true)
      setLog([])
      setCount(0)
      setMode('live')
    })
    es.addEventListener('delta', (ev) => {
      const d = JSON.parse((ev as MessageEvent).data)
      if (d.lsn_int <= last.current) return // at-least-once: drop what we already have
      last.current = d.lsn_int
      setLsn(d.lsn)
      apply(toEntries(d.changes))
      setCount((c) => c + 1)
      setLog((l) => [{ id: nextId.current++, lsn: d.lsn, head: 'event: delta', changes: toEntries(d.changes) }, ...l].slice(0, 6))
    })
    es.addEventListener('resync', () => { es.close(); setMode('connecting') })
    es.onerror = () => { es.close(); setMode((m) => (m === 'connecting' ? 'failed' : 'sim')) }
    return () => es.close()
  }, [mode, url])

  const sorted = [...rows.values()].sort((a, b) => Number(b.row[2]) - Number(a.row[2]))
  const max = Math.max(1, ...sorted.map((e) => Number(e.row[2])))

  return (
    <section id="live" className="mx-auto max-w-[1400px] px-5 pt-36 md:px-10">
      <SectionHead
        n="04"
        label="Live"
        title={<>A view you can <Serif>subscribe</Serif> to.</>}
        kicker="A snapshot at one LSN, then every delta after it, each tagged with its LSN so a reconnecting client drops what it already has. Point this page at your own engine, or watch the in-browser one."
      />
      <div className="mt-12 flex flex-col gap-3 border-y border-ink py-4 md:flex-row md:items-center">
        <div className="flex items-center gap-2.5">
          <span className={`h-2 w-2 rounded-full ${mode === 'live' ? 'bg-plus' : mode === 'failed' ? 'bg-minus' : 'bg-marker'}`} />
          <span className="font-mono text-[12px] text-ink-2">
            {mode === 'live' ? 'connected to your engine' : mode === 'connecting' ? 'connecting…' : mode === 'failed' ? 'no engine there, simulating in the browser' : 'simulating in the browser'}
          </span>
        </div>
        <form className="flex flex-1 gap-2 md:justify-end" onSubmit={(e) => { e.preventDefault(); setMode('connecting') }}>
          <input value={url} onChange={(e) => setUrl(e.target.value)} aria-label="Engine URL"
            className="w-full max-w-[280px] border-b border-ink bg-transparent px-1 py-1.5 font-mono text-[13px] outline-none focus:border-plus" />
          <button className="rounded-full bg-ink px-4 py-1.5 text-[13px] text-paper">Connect</button>
          {mode === 'live' && <button type="button" onClick={() => setMode('sim')} className="px-2 text-[13px] text-ink-3 hover:text-ink">Disconnect</button>}
        </form>
      </div>

      <div className="mt-10 grid grid-cols-12 gap-x-6 gap-y-10">
        <div className="col-span-12 lg:col-span-7">
          <div className="flex items-end justify-between">
            <p className="eyebrow">revenue_by_city</p>
            <p className="font-mono text-[12px] text-ink-3">valid at <span className="text-ink tabular">{lsn}</span> · {count} deltas</p>
          </div>
          <div className="mt-4">
            <AnimatePresence initial={false}>
              {sorted.map((e) => {
                const city = String(e.row[0])
                return (
                  <motion.div key={city} layout transition={{ layout: { duration: 0.6, ease: [0.22, 1, 0.36, 1] } }}
                    initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }}
                    className="grid grid-cols-[8rem_1fr_6rem] items-center gap-4 border-b hairline py-3">
                    <span className="text-[15px] font-medium">{city}</span>
                    <div className="relative h-6">
                      <motion.div className="absolute inset-y-0 left-0 overflow-hidden rounded-r-[4px] bg-ink"
                        animate={{ width: `${(Number(e.row[2]) / max) * 100}%` }} transition={{ duration: 0.7, ease: [0.22, 1, 0.36, 1] }}>
                        <motion.div key={flash.has(city) ? `${city}-${count}` : city}
                          initial={{ opacity: flash.has(city) ? 1 : 0 }} animate={{ opacity: 0 }} transition={{ duration: 1.4 }}
                          className="absolute inset-0 bg-marker" />
                      </motion.div>
                    </div>
                    <span className="text-right font-mono text-[13px] tabular">
                      {fmtNum(Number(e.row[2]))}<span className="ml-1 text-ink-3">/{String(e.row[1])}</span>
                    </span>
                  </motion.div>
                )
              })}
            </AnimatePresence>
          </div>
          <p className="mt-3 font-mono text-[11px] text-ink-3">bar = revenue · number after the slash = paid orders</p>
        </div>
        <div className="col-span-12 lg:col-span-5">
          <p className="eyebrow">Delta stream</p>
          <div className="mt-4 h-[420px] overflow-hidden border border-ink bg-ink p-4 font-mono text-[12px] leading-6 text-paper">
            <AnimatePresence initial={false}>
              {log.map((l) => (
                <motion.div key={l.id} layout initial={{ opacity: 0, y: -10 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }}
                  className="border-b border-paper/10 py-2">
                  <div className="flex justify-between gap-3 text-paper/45"><span className="truncate">{l.head}</span><span>{l.lsn}</span></div>
                  {l.changes.map((c, k) => (
                    <div key={k} className="flex items-center gap-2"><W w={c.w} dark /> {fmtRow(c.row)}</div>
                  ))}
                </motion.div>
              ))}
            </AnimatePresence>
          </div>
        </div>
      </div>
    </section>
  )
}
