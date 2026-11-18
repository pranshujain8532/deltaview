import { AnimatePresence, motion, useMotionValueEvent, useScroll } from 'motion/react'
import { useRef, useState, type ReactNode } from 'react'
import { SectionHead, Serif, W } from './ui'

type Step = { t: string; caption: string; code: string; body: ReactNode }

const Row = ({ w, children, struck }: { w: number; children: ReactNode; struck?: boolean }) => (
  <div className={`flex items-center gap-3 py-1.5 ${struck ? 'text-ink-3 line-through decoration-minus/60' : ''}`}>
    <W w={w} /> <span>{children}</span>
  </div>
)

const steps: Step[] = [
  {
    t: 'An app commits',
    caption: 'Nothing special happens in the user\'s transaction. No trigger runs in the write path.',
    code: 'psql',
    body: <p><span className="text-ink-3">=#</span> UPDATE orders SET status = <span className="text-plus">'paid'</span> WHERE id = 42;<br /><span className="text-ink-3">UPDATE 1</span></p>,
  },
  {
    t: 'Postgres decodes the WAL',
    caption: 'Logical decoding with the built-in pgoutput plugin. Whole transactions, in commit order; rolled-back work never appears.',
    code: 'pgoutput · slot deltaview_slot',
    body: (
      <div className="space-y-1">
        <p><span className="text-ink-3">Begin</span>    xid 95989</p>
        <p><span className="text-ink-3">Update</span>   orders  old=(42, 7, pending, 70.00)</p>
        <p className="pl-[8.4ch]">new=(42, 7, paid, 70.00)</p>
        <p><span className="text-ink-3">Commit</span>   end_lsn 0/1B39470</p>
      </div>
    ),
  },
  {
    t: 'The transaction becomes a delta',
    caption: 'Insert is +new, delete is −old, update is both. One Z-set per table: a map from row to an integer weight.',
    code: 'TxnBatch{EndLSN: 0/1B39470}',
    body: (<><p className="mb-2 text-ink-3">orders</p><Row w={-1}>(42, 7, pending, 70.00)</Row><Row w={1}>(42, 7, paid, 70.00)</Row></>),
  },
  {
    t: 'Filter drops what WHERE rejects',
    caption: 'Filter is linear: filter(A + B) = filter(A) + filter(B). It needs no state at all.',
    code: "Filter  status = 'paid'",
    body: (<><Row w={-1} struck>(42, 7, pending, 70.00)</Row><Row w={1}>(42, 7, paid, 70.00)</Row></>),
  },
  {
    t: 'Join looks up the other side',
    caption: 'The join keeps an index of each input by key. d(A⋈B) = dA⋈B + A⋈dB + dA⋈dB, computed with two lookups.',
    code: 'Join  o.customer_id = c.id',
    body: (<><p className="mb-2 text-ink-3">customers[7] → (7, Asha, Delhi)</p><Row w={1}>(42, 7, paid, 70.00, Asha, Delhi)</Row></>),
  },
  {
    t: 'Aggregate emits −old, +new',
    caption: 'Per touched group: read the old output row, apply the delta, write the new one. Delhi gains one order.',
    code: 'Aggregate  GROUP BY city',
    body: (<><Row w={-1}>(Delhi, 2, 150.00)</Row><Row w={1}>(Delhi, 3, 220.00)</Row></>),
  },
  {
    t: 'One atomic, fsynced batch',
    caption: 'Operator state, view rows and the applied LSN commit together in Pebble. Both saved, or neither.',
    code: 'pebble.Batch.Commit(Sync)',
    body: (
      <div className="space-y-1">
        <p><span className="text-ink-3">set</span> a/revenue_by_city/Delhi   → (3, 220.00)</p>
        <p><span className="text-ink-3">set</span> v/revenue_by_city/(Delhi,3,220.00) → +1</p>
        <p><span className="text-ink-3">del</span> v/revenue_by_city/(Delhi,2,150.00)</p>
        <p><span className="text-ink-3">set</span> m/applied_lsn → 0/1B39470</p>
        <p className="pt-2 text-plus">fsync ✓</p>
      </div>
    ),
  },
  {
    t: 'Only now, acknowledge',
    caption: 'Postgres may now recycle the WAL. Acknowledge before the fsync and a crash loses the change forever.',
    code: 'StandbyStatusUpdate',
    body: <p>flush_lsn = <span className="text-plus">0/1B39470</span><br /><span className="text-ink-3">slot advances · WAL behind it can be recycled</span></p>,
  },
  {
    t: 'Subscribers get the delta',
    caption: 'Server-sent events carry the change and its LSN. A slow client is dropped with a resync, never waited for.',
    code: 'GET /views/revenue_by_city/subscribe',
    body: (
      <div>
        <p className="text-ink-3">event: delta</p>
        <p>data: {'{'}"lsn":"0/1B39470","changes":[</p>
        <p className="pl-4">{'{'}"row":["Delhi",2,150],"weight":<span className="text-minus">-1</span>{'}'},</p>
        <p className="pl-4">{'{'}"row":["Delhi",3,220],"weight":<span className="text-plus">1</span>{'}'}]{'}'}</p>
      </div>
    ),
  },
]

export function Mechanism() {
  const ref = useRef<HTMLDivElement>(null)
  const { scrollYProgress } = useScroll({ target: ref, offset: ['start start', 'end end'] })
  const [i, setI] = useState(0)
  useMotionValueEvent(scrollYProgress, 'change', (v) => setI(Math.min(steps.length - 1, Math.floor(v * steps.length))))

  return (
    <section id="mechanism" className="mx-auto max-w-[1400px] px-5 pt-28 md:px-10">
      <SectionHead
        n="02"
        label="The mechanism"
        title={<>Follow one change from <Serif>commit</Serif> to screen.</>}
        kicker="Scroll. This is the real path through the code, with the real message shapes and storage keys."
      />
      <div ref={ref} className="relative" style={{ height: `${steps.length * 70}vh` }}>
        <div className="sticky top-14 grid min-h-[calc(100vh-3.5rem)] grid-cols-12 items-center gap-x-6 gap-y-8 py-12">
          <ol className="col-span-12 hidden md:col-span-4 md:block">
            {steps.map((s, k) => (
              <li key={s.t} className="relative flex gap-4 py-2.5">
                <span className={`font-mono text-[11px] transition-colors ${k === i ? 'text-ink' : 'text-ink-3'}`}>{String(k + 1).padStart(2, '0')}</span>
                <span className={`text-[15px] transition-all duration-500 ${k === i ? 'translate-x-1 font-medium text-ink' : k < i ? 'text-ink-2' : 'text-ink-3'}`}>{s.t}</span>
                {k === i && <motion.span layoutId="step-bar" className="absolute top-2 -left-4 h-[calc(100%-1rem)] w-[2px] bg-ink" />}
              </li>
            ))}
          </ol>
          <div className="col-span-12 md:col-span-8">
            <div className="mb-5 h-px w-full bg-rule">
              <motion.div className="h-px origin-left bg-ink" style={{ scaleX: scrollYProgress }} />
            </div>
            <AnimatePresence mode="wait">
              <motion.div key={i}
                initial={{ opacity: 0, y: 24, filter: 'blur(6px)' }}
                animate={{ opacity: 1, y: 0, filter: 'blur(0px)' }}
                exit={{ opacity: 0, y: -16, filter: 'blur(4px)' }}
                transition={{ duration: 0.45, ease: [0.22, 1, 0.36, 1] }}>
                <p className="eyebrow mb-3">Step {i + 1} of {steps.length}</p>
                <h3 className="text-[clamp(1.8rem,3.2vw,2.8rem)] leading-tight font-medium tracking-[-0.035em]">{steps[i].t}</h3>
                <p className="mt-3 max-w-xl text-ink-2">{steps[i].caption}</p>
                <div className="mt-8 border border-ink bg-paper shadow-[6px_6px_0_0_var(--color-ink)]">
                  <div className="border-b border-ink px-4 py-2 font-mono text-[11px] tracking-wide text-ink-2">{steps[i].code}</div>
                  <div className="min-h-[170px] overflow-x-auto px-5 py-5 font-mono text-[13.5px] leading-6">{steps[i].body}</div>
                </div>
              </motion.div>
            </AnimatePresence>
          </div>
        </div>
      </div>
    </section>
  )
}
