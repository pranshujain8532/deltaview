import { motion, useScroll, useTransform } from 'motion/react'
import { useRef, useState } from 'react'
import { Marquee } from '@/components/magicui/marquee'
import Magnet from '@/components/rb/Magnet'
import { GITHUB, stack } from '@/lib/data'
import { Reveal, Serif } from './ui'

const supported = [
  'Columns, arithmetic, CASE, COALESCE, NULLIF, casts, ||',
  'COUNT(*), COUNT(col), SUM, AVG, MIN, MAX',
  'Inner equi-joins, chained, self-joins',
  'WHERE with AND / OR / NOT, IN, BETWEEN, LIKE, IS NULL',
  'GROUP BY, HAVING, SELECT DISTINCT',
]

const limits = [
  'One node, one apply thread: one CPU core does the work',
  'No outer joins, subqueries, window functions or ORDER BY … LIMIT',
  'Tables need REPLICA IDENTITY FULL, which adds WAL volume',
  'A transaction is held in memory until its commit arrives',
  'No online schema changes; a changed schema means a fresh snapshot',
]

export function Closing() {
  const ref = useRef<HTMLElement>(null)
  const { scrollYProgress } = useScroll({ target: ref, offset: ['start end', 'end end'] })
  const x = useTransform(scrollYProgress, [0, 1], ['-8%', '0%'])
  const [copied, setCopied] = useState(false)
  const cmd = 'git clone https://github.com/pranshujain8532/deltaview && cd deltaview && docker compose up'

  return (
    <section ref={ref} className="pt-36">
      <div className="border-y border-ink py-5">
        <Marquee className="[--duration:38s] [--gap:3.5rem]" pauseOnHover>
          {stack.map((s) => (
            <span key={s} className="flex items-center gap-14 font-mono text-[15px] text-ink-2">
              {s}<span className="text-ink-3">/</span>
            </span>
          ))}
        </Marquee>
      </div>

      <div className="mx-auto grid max-w-[1400px] grid-cols-12 gap-x-6 gap-y-12 px-5 pt-28 md:px-10">
        <Reveal className="col-span-12 md:col-span-6">
          <p className="eyebrow">What it supports</p>
          <ul className="mt-5 border-t border-ink">
            {supported.map((s) => <li key={s} className="flex gap-3 border-b hairline py-3 text-[15px]"><span className="font-mono text-plus">+</span>{s}</li>)}
          </ul>
          <p className="mt-4 text-[13px] text-ink-3">Everything else is rejected at startup with an error that names the feature. A wrong answer that looks right is worse than an error.</p>
        </Reveal>
        <Reveal delay={0.1} className="col-span-12 md:col-span-6">
          <p className="eyebrow">What it does not do</p>
          <ul className="mt-5 border-t border-ink">
            {limits.map((s) => <li key={s} className="flex gap-3 border-b hairline py-3 text-[15px]"><span className="font-mono text-minus">−</span>{s}</li>)}
          </ul>
        </Reveal>
      </div>

      <div className="mx-auto max-w-[1400px] px-5 pt-32 md:px-10">
        <Reveal>
          <h2 className="max-w-4xl text-[clamp(2.4rem,5.4vw,5rem)] leading-[1] font-medium tracking-[-0.045em]">
            Run it on your laptop in <Serif>one command</Serif>.
          </h2>
        </Reveal>
        <Reveal delay={0.1} className="mt-10">
          <div className="flex flex-col gap-3 border border-ink bg-ink p-2 pl-5 text-paper md:flex-row md:items-center">
            <code className="flex-1 overflow-x-auto py-2 font-mono text-[13.5px] whitespace-nowrap"><span className="text-paper/40">$ </span>{cmd}</code>
            <button onClick={() => { navigator.clipboard?.writeText(cmd); setCopied(true); setTimeout(() => setCopied(false), 1600) }}
              className="shrink-0 bg-paper px-4 py-2.5 font-mono text-[12px] text-ink">
              {copied ? 'copied' : 'copy'}
            </button>
          </div>
          <p className="mt-4 text-[14px] text-ink-2">Postgres, the engine and a load generator start together. Open localhost:8080 and watch revenue_by_city move.</p>
        </Reveal>
        <div className="mt-14 flex flex-wrap items-center gap-6">
          <Magnet padding={50} magnetStrength={3}>
            <a href={GITHUB} target="_blank" rel="noreferrer" className="inline-flex items-center gap-3 rounded-full bg-ink px-7 py-4 text-[16px] font-medium text-paper">
              Read the code on GitHub ↗
            </a>
          </Magnet>
          <a href={`${GITHUB}/blob/main/docs/concepts.md`} target="_blank" rel="noreferrer" className="border-b border-ink/30 pb-0.5 text-[15px] hover:border-ink">
            The delta rules, with worked examples
          </a>
          <a href={`${GITHUB}/blob/main/docs/notes.md`} target="_blank" rel="noreferrer" className="border-b border-ink/30 pb-0.5 text-[15px] hover:border-ink">
            Bug diary
          </a>
        </div>
      </div>

      <footer className="mt-32 overflow-hidden border-t border-ink">
        <div className="mx-auto flex max-w-[1400px] flex-wrap justify-between gap-4 px-5 py-6 font-mono text-[12px] text-ink-2 md:px-10">
          <span>Built by Pranshu Jain · Go, Postgres logical replication, Z-sets</span>
          <span>Theory: DBSP (VLDB 2023) · Same idea as Materialize, Feldera, pg_ivm</span>
        </div>
        <motion.div style={{ x }} className="px-5 pb-2 font-serif text-[clamp(5rem,22vw,22rem)] leading-[0.8] tracking-[-0.04em] whitespace-nowrap italic md:px-10">
          DeltaView<span className="text-minus">.</span>
        </motion.div>
      </footer>
    </section>
  )
}
