import { motion, useMotionValueEvent, useScroll, useTransform } from 'motion/react'
import { useRef, useState } from 'react'
import { SectionHead, Hi, W } from './ui'

const COLS = 40
const ROWS = 22
const CHANGED = 9 * COLS + 27 // the one row that changed

function Table({ mode, progress }: { mode: 'refresh' | 'delta'; progress: number }) {
  const read = Math.floor(progress * COLS * ROWS)
  return (
    <div className="grid gap-[3px]" style={{ gridTemplateColumns: `repeat(${COLS}, minmax(0, 1fr))` }}>
      {Array.from({ length: COLS * ROWS }, (_, i) => {
        let cls = 'bg-bg-3'
        if (mode === 'refresh' && i < read) cls = 'bg-fg-2/55'
        if (mode === 'delta' && i === CHANGED && progress > 0.35) cls = 'bg-plus'
        if (mode === 'delta' && i === CHANGED - COLS && progress > 0.35) cls = 'bg-minus'
        return <div key={i} className={`aspect-square rounded-[1px] transition-colors duration-150 ${cls}`} />
      })}
    </div>
  )
}

export function Problem() {
  const ref = useRef<HTMLDivElement>(null)
  const { scrollYProgress } = useScroll({ target: ref, offset: ['start start', 'end end'] })
  const scan = useTransform(scrollYProgress, [0.05, 0.85], [0, 1], { clamp: true })
  const [p, setP] = useState(0)
  // Quantized so the 1,760 cells re-render a few hundred times, not every frame.
  useMotionValueEvent(scan, 'change', (v) => setP(Math.round(v * 220) / 220))

  return (
    <section id="problem" className="mx-auto max-w-[1400px] px-5 pt-28 md:px-10">
      <SectionHead
        n="01"
        label="The problem"
        title={<>One order changed. <Hi>REFRESH</Hi> re-reads all two million.</>}
        kicker="A materialized view in Postgres is refreshed by running its whole query again. Teams either serve stale numbers or hand-write trigger logic that breaks on deletes, on rows that move between groups, and on joins where both sides change."
      />
      <div ref={ref} className="relative h-[220vh]">
        <div className="sticky top-14 flex min-h-[calc(100vh-3.5rem)] items-center py-10">
          <div className="grid w-full grid-cols-12 gap-x-6 gap-y-10">
            {(['refresh', 'delta'] as const).map((mode) => (
              <div key={mode} className="col-span-12 md:col-span-6">
                <div className="mb-4 flex items-end justify-between border-b border-line-2 pb-3">
                  <div>
                    <p className="eyebrow">{mode === 'refresh' ? 'REFRESH MATERIALIZED VIEW' : 'DeltaView'}</p>
                    <p className="mt-1 text-sm text-fg-2">
                      {mode === 'refresh' ? 'Scans the table, recomputes every group' : 'Reads one transaction from the WAL'}
                    </p>
                  </div>
                  <div className="text-right">
                    <div className="display text-3xl tabular">
                      {mode === 'refresh' ? Math.round(p * 2_000_000).toLocaleString('en-US') : p > 0.35 ? '2' : '0'}
                    </div>
                    <p className="eyebrow">rows read</p>
                  </div>
                </div>
                <Table mode={mode} progress={p} />
                {mode === 'delta' && (
                  <motion.p animate={{ opacity: p > 0.35 ? 1 : 0, y: p > 0.35 ? 0 : 8 }}
                    className="mt-4 flex flex-wrap items-center gap-2 font-mono text-[12.5px] text-fg-2">
                    <W w={-1} /> (42, 7, pending, 70.00) <span className="text-fg-3">then</span> <W w={1} /> (42, 7, paid, 70.00)
                  </motion.p>
                )}
                {mode === 'refresh' && (
                  <p className="mt-4 font-mono text-[12.5px] text-fg-3">
                    {p < 1 ? 'Seq Scan on orders …' : 'done. 1,999,999 of those rows had not changed.'}
                  </p>
                )}
              </div>
            ))}
          </div>
        </div>
      </div>
    </section>
  )
}
