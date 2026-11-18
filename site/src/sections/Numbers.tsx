import { motion } from 'motion/react'
import { useState } from 'react'
import { refreshVsIncremental as data, throughput } from '@/lib/data'
import { Reveal, SectionHead, Serif } from './ui'

const W = 640, H = 340, L = 48, R = 120, T = 20, B = 44
const ymax = 140
const x = (i: number) => L + (i * (W - L - R)) / (data.length - 1)
const y = (v: number) => T + (1 - v / ymax) * (H - T - B)
const path = (k: 'refresh' | 'deltaview') => data.map((d, i) => `${i ? 'L' : 'M'}${x(i)},${y(d[k])}`).join(' ')
const fmtRows = (n: number) => (n >= 1e6 ? `${n / 1e6}M` : `${n / 1e3}k`)

function RefreshChart() {
  const [hover, setHover] = useState<number | null>(null)
  return (
    <figure>
      <div className="mb-4 flex flex-wrap items-center gap-5 text-[13px] text-ink-2">
        <span className="flex items-center gap-2"><svg width="22" height="6"><line x1="0" y1="3" x2="22" y2="3" stroke="var(--color-minus)" strokeWidth="2" strokeDasharray="4 3" /></svg>REFRESH MATERIALIZED VIEW</span>
        <span className="flex items-center gap-2"><svg width="22" height="6"><line x1="0" y1="3" x2="22" y2="3" stroke="var(--color-plus)" strokeWidth="2" /></svg>DeltaView, same 1,000 changes</span>
      </div>
      <svg viewBox={`0 0 ${W} ${H}`} className="w-full overflow-visible" role="img"
        aria-label="Time to bring revenue_by_city up to date after 1,000 changes: REFRESH 19.7, 43.5 and 127.8 ms at 100k, 500k and 2M rows; DeltaView 67.8, 59.1 and 62.5 ms.">
        {[0, 35, 70, 105, 140].map((v) => (
          <g key={v}>
            <line x1={L} x2={W - R + 10} y1={y(v)} y2={y(v)} stroke="var(--color-rule)" strokeWidth="1" />
            <text x={L - 10} y={y(v) + 4} textAnchor="end" className="fill-ink-3 font-mono text-[11px]">{v}</text>
          </g>
        ))}
        <text x={L - 10} y={T - 8} textAnchor="end" className="fill-ink-3 font-mono text-[11px]">ms</text>
        {data.map((d, i) => (
          <text key={d.rows} x={x(i)} y={H - B + 24} textAnchor="middle" className="fill-ink-2 font-mono text-[12px]">{fmtRows(d.rows)} rows</text>
        ))}
        {hover !== null && <line x1={x(hover)} x2={x(hover)} y1={T} y2={H - B} stroke="var(--color-ink)" strokeWidth="1" strokeDasharray="2 3" />}
        {(['refresh', 'deltaview'] as const).map((k) => (
          <g key={k}>
            <motion.path d={path(k)} fill="none" stroke={k === 'refresh' ? 'var(--color-minus)' : 'var(--color-plus)'} strokeWidth="2"
              strokeLinecap="round" strokeLinejoin="round" strokeDasharray={k === 'refresh' ? '6 5' : undefined}
              initial={{ pathLength: 0 }} whileInView={{ pathLength: 1 }} viewport={{ once: true, margin: '-20%' }}
              transition={{ duration: 1.6, ease: [0.22, 1, 0.36, 1], delay: k === 'refresh' ? 0 : 0.3 }} />
            {data.map((d, i) => (
              <motion.circle key={i} cx={x(i)} cy={y(d[k])} r={hover === i ? 6 : 4.5}
                fill="var(--color-paper)" stroke={k === 'refresh' ? 'var(--color-minus)' : 'var(--color-plus)'} strokeWidth="2"
                initial={{ opacity: 0 }} whileInView={{ opacity: 1 }} viewport={{ once: true }} transition={{ delay: 0.5 + i * 0.35 }} />
            ))}
            <text x={x(data.length - 1) + 14} y={y(data[data.length - 1][k]) + 4} className="fill-ink font-mono text-[12px]">
              {k === 'refresh' ? 'REFRESH' : 'DeltaView'} {data[data.length - 1][k]} ms
            </text>
          </g>
        ))}
        {data.map((_, i) => (
          <rect key={i} x={x(i) - 60} y={T} width={120} height={H - T - B} fill="transparent"
            onMouseEnter={() => setHover(i)} onMouseLeave={() => setHover(null)} />
        ))}
        {hover !== null && (
          <g transform={`translate(${Math.min(x(hover) + 12, W - R - 150)},${T + 8})`} pointerEvents="none">
            <rect width="150" height="64" fill="var(--color-ink)" />
            <text x="12" y="20" className="fill-paper font-mono text-[11px]">{fmtRows(data[hover].rows)} rows</text>
            <text x="12" y="38" className="fill-paper font-mono text-[11px]">REFRESH   {data[hover].refresh} ms</text>
            <text x="12" y="54" className="fill-paper font-mono text-[11px]">DeltaView {data[hover].deltaview} ms</text>
          </g>
        )}
      </svg>
      <figcaption className="mt-4 max-w-xl text-[13px] leading-relaxed text-ink-3">
        Refresh time grows with the table; DeltaView's is flat because it depends on the change. On small tables a full refresh
        is still cheaper: here the lines cross between 500k and 2M rows. Single runs, view revenue_by_city.
      </figcaption>
    </figure>
  )
}

function Throughput() {
  const max = Math.max(...throughput.map((t) => t.txns))
  return (
    <div>
      {throughput.map((t, i) => (
        <div key={t.shape} className="group grid grid-cols-[9rem_1fr] items-center gap-4 border-b hairline py-3.5">
          <div>
            <p className="text-[14px] font-medium">{t.shape}</p>
            <p className="font-mono text-[11px] text-ink-3">{t.sql}</p>
          </div>
          <div className="flex items-center gap-3">
            <motion.div className="h-5 rounded-r-[4px] bg-ink transition-colors group-hover:bg-plus"
              initial={{ width: 0 }} whileInView={{ width: `${(t.txns / max) * 78}%` }} viewport={{ once: true }}
              transition={{ duration: 1.1, delay: i * 0.12, ease: [0.22, 1, 0.36, 1] }} />
            <span className="font-mono text-[13px] tabular">{t.txns.toLocaleString('en-US')}</span>
          </div>
        </div>
      ))}
      <p className="mt-3 font-mono text-[11px] text-ink-3">transactions per second, one apply thread, 2 CPUs</p>
    </div>
  )
}

const tiles = [
  { v: '2.7', u: 'ms', l: 'p50 latency, Postgres commit to view commit', s: 'p99 4.3 ms · max 20.6 ms at 200 txn/s' },
  { v: '458', u: 'ms', l: 'to catch up after a restart with 5,000 pending', s: 'ready in 30 ms, then streams the backlog' },
  { v: '13.1k', u: 'txn/s', l: 'with group commit, one fsync per 64', s: 'vs 11.4k with one fsync per transaction' },
  { v: '0.29', u: '×', l: 'state size of a join, relative to its source tables', s: 'aggregate 0.04× · filter 0.12×, at 500k orders' },
]

export function Numbers() {
  return (
    <section id="numbers" className="mx-auto max-w-[1400px] px-5 pt-36 md:px-10">
      <SectionHead
        n="06"
        label="Numbers"
        title={<>Cost follows the <Serif>change</Serif>, not the table.</>}
        kicker="Measured on a laptop with the engine capped at two CPUs and Postgres 17 in Docker. Real runs, reported as measured, including where a full refresh still wins."
      />
      <div className="mt-14 grid grid-cols-12 gap-x-6 gap-y-14">
        <Reveal className="col-span-12 lg:col-span-7"><RefreshChart /></Reveal>
        <Reveal delay={0.1} className="col-span-12 lg:col-span-5"><p className="eyebrow mb-4">Throughput by view shape</p><Throughput /></Reveal>
      </div>
      <div className="mt-16 grid grid-cols-1 border-t border-ink sm:grid-cols-2 lg:grid-cols-4">
        {tiles.map((t, i) => (
          <Reveal key={t.l} delay={i * 0.07} className="border-b hairline py-7 pr-6 lg:border-b-0 lg:[&:not(:first-child)]:border-l lg:[&:not(:first-child)]:pl-6">
            <div className="flex items-baseline gap-1.5">
              <span className="text-[clamp(2.4rem,3.6vw,3.2rem)] leading-none font-medium tracking-[-0.045em] tabular">{t.v}</span>
              <span className="font-mono text-[13px] text-ink-2">{t.u}</span>
            </div>
            <p className="mt-3 text-[14px] leading-snug">{t.l}</p>
            <p className="mt-2 font-mono text-[11.5px] text-ink-3">{t.s}</p>
          </Reveal>
        ))}
      </div>
    </section>
  )
}
