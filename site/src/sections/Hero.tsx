import { AnimatePresence, motion, useScroll, useSpring, useTransform } from 'motion/react'
import { useEffect, useRef, useState } from 'react'
import DotGrid from '@/components/rb/DotGrid'
import BlurText from '@/components/rb/BlurText'
import CountUp from '@/components/rb/CountUp'
import Magnet from '@/components/rb/Magnet'
import { Sim, fmtLSN, type Entry } from '@/lib/ivm'
import { GITHUB } from '@/lib/data'
import { W, fmtRow } from './ui'

const links = [
  ['01', 'Problem', '#problem'],
  ['02', 'Mechanism', '#mechanism'],
  ['03', 'Playground', '#playground'],
  ['04', 'Live', '#live'],
  ['05', 'Proof', '#proof'],
  ['06', 'Numbers', '#numbers'],
]

export function Nav() {
  const { scrollYProgress } = useScroll()
  const x = useSpring(scrollYProgress, { stiffness: 120, damping: 30 })
  return (
    <header className="fixed inset-x-0 top-0 z-50 border-b hairline bg-paper/80 backdrop-blur-md">
      <div className="mx-auto flex h-14 max-w-[1400px] items-center justify-between px-5 md:px-10">
        <a href="#top" className="flex items-center gap-2.5">
          <svg width="18" height="18" viewBox="0 0 32 32" aria-hidden>
            <path d="M16 5 28 27H4Z" fill="none" stroke="currentColor" strokeWidth="2.6" strokeLinejoin="round" />
          </svg>
          <span className="text-[15px] font-semibold tracking-[-0.02em]">DeltaView</span>
        </a>
        <nav className="hidden items-center gap-7 lg:flex">
          {links.map(([n, l, h]) => (
            <a key={h} href={h} className="group flex items-baseline gap-1.5 text-[13px] text-ink-2 transition-colors hover:text-ink">
              <span className="font-mono text-[10px] text-ink-3 group-hover:text-ink">{n}</span>
              {l}
            </a>
          ))}
        </nav>
        <a href={GITHUB} target="_blank" rel="noreferrer"
          className="rounded-full border border-ink px-4 py-1.5 text-[13px] font-medium transition-colors hover:bg-ink hover:text-paper">
          Source ↗
        </a>
      </div>
      <motion.div style={{ scaleX: x }} className="h-px origin-left bg-ink" />
    </header>
  )
}

type Line = { id: number; sql: string; lsn: number; out: Entry[] }

function Ticker() {
  const [lines, setLines] = useState<Line[]>([])
  const sim = useRef<Sim | null>(null)
  useEffect(() => {
    sim.current = new Sim(11)
    let id = 0
    const next = () => {
      const s = sim.current!
      let r = s.tick()
      for (let i = 0; i < 6 && r.trace.view.length === 0; i++) r = s.tick()
      return { id: id++, sql: r.sql, lsn: r.lsn, out: r.trace.view }
    }
    // Start with a few lines so the card is never empty.
    setLines([next(), next(), next()].reverse())
    const t = setInterval(() => setLines((ls) => [next(), ...ls].slice(0, 5)), 1600)
    return () => clearInterval(t)
  }, [])
  return (
    <div className="border border-ink bg-paper shadow-[8px_8px_0_0_var(--color-ink)]">
      <div className="flex items-center justify-between border-b border-ink px-4 py-2.5">
        <span className="eyebrow !text-ink">revenue_by_city · deltas</span>
        <span className="flex items-center gap-2 font-mono text-[11px] text-ink-2">
          <span className="relative flex h-1.5 w-1.5">
            <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-plus opacity-60" />
            <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-plus" />
          </span>
          streaming
        </span>
      </div>
      <div className="h-[340px] overflow-hidden px-4 py-3 font-mono text-[12.5px] leading-6">
        <AnimatePresence initial={false}>
          {lines.map((l) => (
            <motion.div key={l.id} layout
              initial={{ opacity: 0, y: -14, filter: 'blur(4px)' }}
              animate={{ opacity: 1, y: 0, filter: 'blur(0px)' }}
              exit={{ opacity: 0 }}
              transition={{ duration: 0.55, ease: [0.22, 1, 0.36, 1] }}
              className="border-b border-dashed hairline py-2 last:border-0">
              <div className="flex justify-between gap-3 text-ink-3">
                <span className="truncate">{l.sql}</span>
                <span className="shrink-0">{fmtLSN(l.lsn)}</span>
              </div>
              {l.out.map((e, i) => (
                <div key={i} className="flex items-center gap-2 text-ink">
                  <W w={e.w} /> {fmtRow(e.row)}
                </div>
              ))}
            </motion.div>
          ))}
        </AnimatePresence>
      </div>
    </div>
  )
}

const figures = [
  { to: 0, suffix: '', label: 'mismatches across 50,000 random transactions' },
  { to: 200, suffix: '', label: 'kill -9 crashes survived, exactly once' },
  { to: 2.7, suffix: ' ms', label: 'p50, Postgres commit to view commit', decimals: 1 },
  { to: 2, suffix: '×', label: 'faster than REFRESH at 2M rows, and widening' },
]

export function Hero() {
  const ref = useRef<HTMLElement>(null)
  const { scrollYProgress } = useScroll({ target: ref, offset: ['start start', 'end start'] })
  const yTitle = useTransform(scrollYProgress, [0, 1], [0, -120])
  const yCard = useTransform(scrollYProgress, [0, 1], [0, 60])
  const fade = useTransform(scrollYProgress, [0, 0.8], [1, 0.2])

  return (
    <section id="top" ref={ref} className="relative overflow-hidden pt-14">
      <div className="absolute inset-0 opacity-70 [mask-image:radial-gradient(ellipse_at_30%_40%,black_30%,transparent_75%)]">
        <DotGrid dotSize={2} gap={22} baseColor="#cfc8b4" activeColor="#151412" proximity={110} shockRadius={180} shockStrength={4} />
      </div>
      <div className="relative mx-auto max-w-[1400px] px-5 pt-20 pb-16 md:px-10 md:pt-28">
        <div className="grid grid-cols-12 gap-x-6 gap-y-14">
          <motion.div style={{ y: yTitle, opacity: fade }} className="col-span-12 lg:col-span-7">
            <motion.p initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ duration: 1 }} className="eyebrow mb-8">
              Incremental view maintenance · Postgres · Go
            </motion.p>
            <h1 className="text-[clamp(3.2rem,8.4vw,8.4rem)] leading-[0.92] font-medium tracking-[-0.055em]">
              <BlurText text="Views that update" animateBy="words" direction="top" delay={90} className="!m-0" />
              <span className="block font-serif font-normal italic tracking-[-0.03em]">
                <BlurText text="themselves." animateBy="letters" direction="bottom" delay={45} />
              </span>
            </h1>
            <motion.p initial={{ opacity: 0, y: 16 }} animate={{ opacity: 1, y: 0 }} transition={{ delay: 0.9, duration: 0.9 }}
              className="mt-10 max-w-xl text-lg leading-relaxed text-ink-2 md:text-xl">
              DeltaView reads Postgres's write-ahead log and pushes each committed transaction through your
              view as a <em className="font-serif text-ink">delta</em>. The work grows with the change, not with the table.
            </motion.p>
            <motion.div initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ delay: 1.2 }} className="mt-10 flex flex-wrap items-center gap-4">
              <Magnet padding={40} magnetStrength={4}>
                <a href="#mechanism" className="inline-flex items-center gap-3 rounded-full bg-ink px-6 py-3.5 text-[15px] font-medium text-paper">
                  Follow one change <span aria-hidden>↓</span>
                </a>
              </Magnet>
              <a href={GITHUB} target="_blank" rel="noreferrer" className="group inline-flex items-center gap-2 px-2 py-3 text-[15px] font-medium">
                <span className="border-b border-ink/30 group-hover:border-ink">Read the source</span> ↗
              </a>
            </motion.div>
          </motion.div>
          <motion.div style={{ y: yCard }} initial={{ opacity: 0, x: 30 }} animate={{ opacity: 1, x: 0 }}
            transition={{ delay: 0.5, duration: 1, ease: [0.22, 1, 0.36, 1] }}
            className="col-span-12 self-end lg:col-span-5">
            <pre className="mb-5 overflow-x-auto font-mono text-[12px] leading-5 text-ink-2">
{`CREATE MATERIALIZED VIEW revenue_by_city AS
SELECT c.city, COUNT(*), SUM(o.amount)
FROM orders o JOIN customers c ON o.customer_id = c.id
WHERE o.status = 'paid' GROUP BY c.city;`}
            </pre>
            <Ticker />
          </motion.div>
        </div>

        <div className="mt-24 grid grid-cols-2 border-t border-ink md:grid-cols-4">
          {figures.map((f, i) => (
            <motion.div key={f.label} initial={{ opacity: 0, y: 20 }} animate={{ opacity: 1, y: 0 }}
              transition={{ delay: 1.3 + i * 0.1, duration: 0.8 }}
              className="border-b hairline py-6 pr-6 md:border-b-0 md:[&:not(:first-child)]:border-l md:[&:not(:first-child)]:pl-6">
              <div className="text-[clamp(2.2rem,4vw,3.4rem)] leading-none font-medium tracking-[-0.04em] tabular">
                <CountUp to={f.to} decimals={f.decimals ?? 0} duration={2} separator="," />
                <span>{f.suffix}</span>
              </div>
              <p className="mt-3 max-w-[22ch] text-sm leading-snug text-ink-2">{f.label}</p>
            </motion.div>
          ))}
        </div>
      </div>
    </section>
  )
}
