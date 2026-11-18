import { motion, useScroll, useTransform } from 'motion/react'
import { useRef, useState } from 'react'
import { Marquee } from '@/components/magicui/marquee'
import Magnet from '@/components/rb/Magnet'
import { GITHUB, stack } from '@/lib/data'
import { Reveal, Hi } from './ui'

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
      <div className="border-y border-line-2 py-5">
        <Marquee className="[--duration:38s] [--gap:3.5rem]" pauseOnHover>
          {stack.map((s) => (
            <span key={s} className="flex items-center gap-14 font-mono text-[15px] text-fg-2">
              {s}<span className="text-fg-3">/</span>
            </span>
          ))}
        </Marquee>
      </div>

      <div className="mx-auto grid max-w-[1400px] grid-cols-12 gap-x-6 gap-y-12 px-5 pt-28 md:px-10">
        <Reveal className="col-span-12 md:col-span-6">
          <p className="eyebrow">What it supports</p>
          <ul className="mt-5 border-t border-line-2">
            {supported.map((s) => <li key={s} className="flex gap-3 border-b hairline py-3 text-[15px]"><span className="font-mono text-plus-hi">+</span>{s}</li>)}
          </ul>
          <p className="mt-4 text-[13px] text-fg-3">Everything else is rejected at startup with an error that names the feature. A wrong answer that looks right is worse than an error.</p>
        </Reveal>
        <Reveal delay={0.1} className="col-span-12 md:col-span-6">
          <p className="eyebrow">What it does not do</p>
          <ul className="mt-5 border-t border-line-2">
            {limits.map((s) => <li key={s} className="flex gap-3 border-b hairline py-3 text-[15px]"><span className="font-mono text-minus-hi">−</span>{s}</li>)}
          </ul>
        </Reveal>
      </div>

      <div className="mx-auto max-w-[1400px] px-5 pt-32 md:px-10">
        <Reveal>
          <h2 className="max-w-4xl text-[clamp(2.4rem,5.4vw,5rem)] leading-[1] display">
            Run it on your laptop in <Hi>one command</Hi>.
          </h2>
        </Reveal>
        <Reveal delay={0.1} className="mt-10">
          <div className="flex flex-col gap-3 rounded-xl border hairline bg-[#080908] p-2 pl-5 text-fg md:flex-row md:items-center">
            <code className="flex-1 overflow-x-auto py-2 font-mono text-[13.5px] whitespace-nowrap"><span className="text-fg-3">$ </span>{cmd}</code>
            <button onClick={() => { navigator.clipboard?.writeText(cmd); setCopied(true); setTimeout(() => setCopied(false), 1600) }}
              className="shrink-0 rounded-lg bg-lime px-4 py-2.5 font-mono text-[12px] font-semibold text-bg">
              {copied ? 'copied' : 'copy'}
            </button>
          </div>
          <p className="mt-4 text-[14px] text-fg-2">Postgres, the engine and a load generator start together. Open localhost:8080 and watch revenue_by_city move.</p>
        </Reveal>
        <div className="mt-14 flex flex-wrap items-center gap-6">
          <Magnet padding={50} magnetStrength={3}>
            <a href={GITHUB} target="_blank" rel="noreferrer" className="inline-flex items-center gap-3 rounded-full bg-lime px-7 py-4 text-[16px] font-semibold text-bg">
              Read the code on GitHub ↗
            </a>
          </Magnet>
          <a href={`${GITHUB}/blob/main/docs/concepts.md`} target="_blank" rel="noreferrer" className="border-b border-fg/30 pb-0.5 text-[15px] hover:border-line-2">
            The delta rules, with worked examples
          </a>
          <a href={`${GITHUB}/blob/main/docs/notes.md`} target="_blank" rel="noreferrer" className="border-b border-fg/30 pb-0.5 text-[15px] hover:border-line-2">
            Bug diary
          </a>
        </div>
      </div>

      <footer className="mt-32 overflow-hidden border-t border-line-2">
        <div className="mx-auto flex max-w-[1400px] flex-wrap justify-between gap-4 px-5 py-6 font-mono text-[12px] text-fg-2 md:px-10">
          <span>Built by Pranshu Jain · Go, Postgres logical replication, Z-sets</span>
          <span>Theory: DBSP (VLDB 2023) · Same idea as Materialize, Feldera, pg_ivm</span>
        </div>
        <motion.div style={{ x }} className="display outline-text px-5 pb-4 text-[clamp(4rem,16.5vw,17rem)] leading-[0.82] whitespace-nowrap md:px-10">
          DeltaView<span className="text-lime [-webkit-text-stroke:0]">.</span>
        </motion.div>
      </footer>
    </section>
  )
}
