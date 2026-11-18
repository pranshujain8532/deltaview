import { forwardRef, useRef, type ReactNode } from 'react'
import { AnimatedBeam } from '@/components/magicui/animated-beam'
import { Reveal } from './ui'

const Node = forwardRef<HTMLDivElement, { n: string; title: string; children: ReactNode; dark?: boolean }>(
  ({ n, title, children, dark }, ref) => (
    <div ref={ref}
      className={`relative z-10 w-full border px-4 py-3.5 ${dark ? 'border-ink bg-ink text-paper' : 'border-ink bg-paper'}`}>
      <div className="flex items-baseline justify-between gap-3">
        <span className="text-[15px] font-medium tracking-[-0.01em]">{title}</span>
        <span className={`font-mono text-[10px] ${dark ? 'text-paper/50' : 'text-ink-3'}`}>{n}</span>
      </div>
      <p className={`mt-1 text-[12.5px] leading-snug ${dark ? 'text-paper/65' : 'text-ink-2'}`}>{children}</p>
    </div>
  ),
)

export function Architecture() {
  const box = useRef<HTMLDivElement>(null)
  const pg = useRef<HTMLDivElement>(null)
  const cdc = useRef<HTMLDivElement>(null)
  const loop = useRef<HTMLDivElement>(null)
  const circ = useRef<HTMLDivElement>(null)
  const store = useRef<HTMLDivElement>(null)
  const http = useRef<HTMLDivElement>(null)
  const sql = useRef<HTMLDivElement>(null)

  const beam = { containerRef: box, pathColor: '#151412', pathOpacity: 0.28, pathWidth: 2, gradientStartColor: '#151412', gradientStopColor: '#2f7d4f', duration: 4 }

  return (
    <div id="architecture" className="mx-auto max-w-[1400px] px-5 pt-24 md:px-10">
      <Reveal className="grid grid-cols-12 gap-6">
        <div className="col-span-12 md:col-span-3">
          <p className="eyebrow">One Go process</p>
        </div>
        <div className="col-span-12 md:col-span-9">
          <h3 className="max-w-2xl text-[clamp(1.6rem,2.6vw,2.3rem)] leading-tight font-medium tracking-[-0.03em]">
            One goroutine owns all state. No locks, a deterministic order, and a recovery story you can explain on a whiteboard.
          </h3>
        </div>
      </Reveal>
      <Reveal delay={0.1}>
        <div ref={box} className="relative mt-14 grid grid-cols-1 gap-6 border border-dashed hairline p-6 md:grid-cols-4 md:gap-10 md:p-10">
          <div className="flex flex-col justify-center gap-6">
            <Node ref={pg} n="pg" title="Postgres" dark>WAL → logical decoding (pgoutput). The replication slot remembers the confirmed LSN.</Node>
            <Node ref={sql} n="0" title="SQL front end">views.sql parsed by the real Postgres parser, bound, planned. Unsupported SQL is refused by name.</Node>
          </div>
          <div className="flex flex-col justify-center gap-6">
            <Node ref={cdc} n="1·2" title="CDC reader + assembler">Decodes messages, answers keepalives, builds one Z-set per table per transaction.</Node>
            <Node ref={loop} n="3" title="Apply loop">Single writer. Skips duplicates, runs every circuit, commits, then acks.</Node>
          </div>
          <div className="flex flex-col justify-center gap-6">
            <Node ref={circ} n="4" title="Circuits">Filter · Project · Join · Aggregate · Distinct, one graph per view.</Node>
            <Node ref={store} n="5" title="Pebble state">Operator state, view rows and applied LSN in one fsynced batch.</Node>
          </div>
          <div className="flex flex-col justify-center gap-6">
            <Node ref={http} n="7" title="HTTP · SSE" dark>Read a view at a consistent LSN, or subscribe to its deltas. Prometheus at /metrics.</Node>
          </div>

          <AnimatedBeam {...beam} fromRef={pg} toRef={cdc} curvature={-20} />
          <AnimatedBeam {...beam} fromRef={cdc} toRef={loop} delay={0.6} />
          <AnimatedBeam {...beam} fromRef={sql} toRef={circ} curvature={40} delay={0.2} />
          <AnimatedBeam {...beam} fromRef={loop} toRef={circ} delay={1.1} curvature={-10} />
          <AnimatedBeam {...beam} fromRef={loop} toRef={store} delay={1.6} />
          <AnimatedBeam {...beam} fromRef={store} toRef={http} delay={2.1} curvature={-30} />
          <AnimatedBeam {...beam} fromRef={loop} toRef={pg} reverse curvature={70} delay={2.4} gradientStopColor="#d4471f" />
        </div>
        <p className="mt-3 text-right font-mono text-[11px] text-ink-3">
          <span className="text-minus">vermilion beam</span> = acknowledgement back to Postgres, sent only after the fsync
        </p>
      </Reveal>
    </div>
  )
}
