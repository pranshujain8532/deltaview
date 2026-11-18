import { AnimatePresence, motion, useScroll, useSpring, useTransform } from 'motion/react'
import { useEffect, useRef, useState } from 'react'
import Threads from '@/components/rb/Threads'
import BlurText from '@/components/rb/BlurText'
import CountUp from '@/components/rb/CountUp'
import Magnet from '@/components/rb/Magnet'
import { DeltaFlow, type DeltaFlowHandle } from '@/components/DeltaFlow'
import { Sim, fmtLSN } from '@/lib/ivm'
import { GITHUB } from '@/lib/data'
import { connect, disconnect, subscribe, useEngine } from '@/lib/engine'
import { wavesFromDelta, wavesFromTrace } from './ui'

const links = [
  ['01', 'Problem', '#problem'],
  ['02', 'Mechanism', '#mechanism'],
  ['03', 'Playground', '#playground'],
  ['04', 'Live', '#live'],
  ['05', 'Proof', '#proof'],
  ['06', 'Numbers', '#numbers'],
]

export function EnginePill({ compact = false }: { compact?: boolean }) {
  const e = useEngine()
  const [open, setOpen] = useState(false)
  const [url, setUrl] = useState(e.base)
  useEffect(() => setUrl(e.base), [e.base])
  const dot = e.mode === 'live' ? 'bg-plus-hi' : e.mode === 'error' ? 'bg-minus-hi' : e.mode === 'connecting' ? 'bg-lime animate-pulse' : 'bg-fg-3'
  return (
    <div className="relative">
      <button onClick={() => setOpen((o) => !o)}
        className="flex items-center gap-2 rounded-full border hairline bg-bg-2 px-3.5 py-1.5 font-mono text-[11.5px] text-fg-2 transition-colors hover:border-line-2 hover:text-fg">
        <span className={`h-1.5 w-1.5 rounded-full ${dot}`} />
        {e.mode === 'live' ? (compact ? 'live' : `live · ${e.status?.applied_lsn ?? ''}`) : e.mode === 'connecting' ? 'connecting' : e.mode === 'error' ? 'no engine' : 'simulated'}
      </button>
      <AnimatePresence>
        {open && (
          <motion.div initial={{ opacity: 0, y: -6, scale: 0.98 }} animate={{ opacity: 1, y: 0, scale: 1 }} exit={{ opacity: 0, y: -6 }}
            className="panel absolute right-0 top-11 z-50 w-[330px] p-4 shadow-2xl shadow-black/60">
            <p className="text-[13px] font-medium">Connect a running engine</p>
            <p className="mt-1 text-[12px] leading-relaxed text-fg-3">
              Start DeltaView (<span className="font-mono">docker compose up</span>), then connect. Everything on this page switches from the simulation to your engine's real views.
            </p>
            <form className="mt-3 flex gap-2" onSubmit={(ev) => { ev.preventDefault(); void connect(url) }}>
              <input value={url} onChange={(ev) => setUrl(ev.target.value)} aria-label="Engine URL"
                className="min-w-0 flex-1 rounded-md border hairline bg-bg px-2.5 py-1.5 font-mono text-[12px] outline-none focus:border-lime" />
              <button className="rounded-md bg-lime px-3 py-1.5 text-[12px] font-semibold text-bg">Connect</button>
            </form>
            {e.mode === 'error' && <p className="mt-2 font-mono text-[11px] text-minus-hi">{e.error}. Is it running, and reachable from this browser?</p>}
            {e.mode === 'live' && (
              <div className="mt-3 flex items-center justify-between font-mono text-[11px] text-fg-3">
                <span>{e.views.length} views · heartbeat {e.status?.heartbeat}</span>
                {!e.sameOrigin && <button onClick={disconnect} className="text-fg-2 hover:text-fg">disconnect</button>}
              </div>
            )}
            <a href="console.html" className="mt-3 block text-[12.5px] text-lime hover:underline">Open the console →</a>
          </motion.div>
        )}
      </AnimatePresence>
    </div>
  )
}

export function Nav() {
  const { scrollYProgress } = useScroll()
  const x = useSpring(scrollYProgress, { stiffness: 120, damping: 30 })
  return (
    <header className="fixed inset-x-0 top-0 z-50 border-b hairline bg-bg/75 backdrop-blur-xl">
      <div className="mx-auto flex h-14 max-w-[1400px] items-center justify-between gap-4 px-5 md:px-10">
        <a href="#top" className="flex items-center gap-2.5">
          <svg width="20" height="20" viewBox="0 0 32 32" aria-hidden>
            <path d="M16 5 28 27H4Z" fill="none" stroke="var(--color-lime)" strokeWidth="2.6" strokeLinejoin="round" />
          </svg>
          <span className="display text-[16px] !tracking-[-0.02em]">DeltaView</span>
        </a>
        <nav className="hidden items-center gap-6 lg:flex">
          {links.map(([n, l, h]) => (
            <a key={h} href={h} className="group flex items-baseline gap-1.5 text-[13px] text-fg-2 transition-colors hover:text-fg">
              <span className="font-mono text-[10px] text-fg-3 group-hover:text-lime">{n}</span>{l}
            </a>
          ))}
          <a href="console.html" className="text-[13px] text-fg-2 hover:text-fg">Console</a>
        </nav>
        <div className="flex items-center gap-2">
          <EnginePill />
          <a href={GITHUB} target="_blank" rel="noreferrer" className="hidden rounded-full bg-fg px-4 py-1.5 text-[13px] font-medium text-bg sm:block">GitHub ↗</a>
        </div>
      </div>
      <motion.div style={{ scaleX: x }} className="h-px origin-left bg-lime" />
    </header>
  )
}

/** The hero animation: fed by the in-browser engine, or by a real engine when connected. */
function Flow() {
  const flow = useRef<DeltaFlowHandle>(null)
  const e = useEngine()
  const [lsn, setLsn] = useState('')
  const [source, setSource] = useState('in-browser engine')

  useEffect(() => {
    if (e.mode === 'live' && e.views.length) {
      const view = e.views.find((v) => v.name === 'revenue_by_city')?.name ?? e.views[0].name
      setSource(`your engine · ${view}`)
      return subscribe(view, {
        snapshot: (l) => setLsn(l),
        delta: (l, _n, changes) => {
          setLsn(l)
          flow.current?.push(wavesFromDelta(changes.map((c) => c.weight)), `delta at ${l}: ${changes.length} changed rows`)
        },
      })
    }
    setSource('in-browser engine')
    const sim = new Sim(11)
    const tick = () => {
      let r = sim.tick()
      for (let i = 0; i < 6 && r.trace.view.length === 0; i++) r = sim.tick()
      setLsn(fmtLSN(r.lsn))
      flow.current?.push(wavesFromTrace(r.trace), r.sql)
    }
    const first = setTimeout(tick, 900)
    const t = setInterval(tick, 4200)
    return () => { clearTimeout(first); clearInterval(t) }
  }, [e.mode, e.views])

  return (
    <div className="panel relative overflow-hidden">
      <div className="flex items-center justify-between border-b hairline px-4 py-2.5">
        <span className="eyebrow">revenue_by_city · one transaction at a time</span>
        <span className="flex items-center gap-2 font-mono text-[11px] text-fg-3">
          <span className="relative flex h-1.5 w-1.5">
            <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-lime opacity-60" />
            <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-lime" />
          </span>
          {source} · {lsn}
        </span>
      </div>
      <DeltaFlow ref={flow} height={340} />
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
  const yTitle = useTransform(scrollYProgress, [0, 1], [0, -140])
  const fade = useTransform(scrollYProgress, [0, 0.6], [1, 0])
  const scale = useTransform(scrollYProgress, [0, 1], [1, 0.94])

  return (
    <section id="top" ref={ref} className="relative overflow-hidden pt-14">
      <div className="pointer-events-none absolute inset-x-0 top-0 h-[760px] opacity-80 [mask-image:linear-gradient(to_bottom,black_40%,transparent)]">
        <Threads color="#3a3f37" accentColor="#c8f04b" amplitude={1.1} distance={0.2} lineCount={36} enableMouseInteraction />
      </div>
      <div className="pointer-events-none absolute -top-40 left-1/2 h-[520px] w-[900px] -translate-x-1/2 rounded-full bg-lime/10 blur-[140px]" />
      <div className="relative mx-auto max-w-[1400px] px-5 pt-24 pb-14 md:px-10 md:pt-32">
        <motion.div style={{ y: yTitle, opacity: fade }}>
          <motion.div initial={{ opacity: 0, y: 10 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.8 }}
            className="mb-8 inline-flex items-center gap-2 rounded-full border hairline bg-bg-2/80 px-3 py-1 font-mono text-[11px] text-fg-2 backdrop-blur">
            <span className="h-1.5 w-1.5 rounded-full bg-lime" />
            Open source · incremental view maintenance for Postgres, in Go
          </motion.div>
          <h1 className="display text-[clamp(3.2rem,9vw,9.6rem)] leading-[0.88]">
            <BlurText text="Views that update" animateBy="words" direction="top" delay={90} className="!m-0" />
            <span className="block text-lime">
              <BlurText text="themselves." animateBy="letters" direction="bottom" delay={40} />
            </span>
          </h1>
          <div className="mt-10 grid grid-cols-12 gap-6">
            <motion.p initial={{ opacity: 0, y: 16 }} animate={{ opacity: 1, y: 0 }} transition={{ delay: 0.9, duration: 0.9 }}
              className="col-span-12 max-w-xl text-lg leading-relaxed text-fg-2 md:col-span-6 md:text-xl">
              DeltaView reads Postgres's write-ahead log and pushes each committed transaction through your SQL view as a
              delta. The work grows with the change, <span className="text-fg">not with the table</span>.
            </motion.p>
            <motion.div initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ delay: 1.15 }}
              className="col-span-12 flex flex-wrap items-center gap-3 md:col-span-6 md:justify-end md:self-end">
              <Magnet padding={40} magnetStrength={4}>
                <a href="#mechanism" className="inline-flex items-center gap-3 rounded-full bg-lime px-6 py-3.5 text-[15px] font-semibold text-bg shadow-[0_0_40px_-8px_var(--color-lime)]">
                  Follow one change <span aria-hidden>↓</span>
                </a>
              </Magnet>
              <a href="console.html" className="inline-flex items-center gap-2 rounded-full border hairline px-6 py-3.5 text-[15px] font-medium hover:border-line-2">
                Open the live console
              </a>
            </motion.div>
          </div>
        </motion.div>

        <motion.div style={{ scale }} initial={{ opacity: 0, y: 40 }} animate={{ opacity: 1, y: 0 }}
          transition={{ delay: 0.6, duration: 1.1, ease: [0.22, 1, 0.36, 1] }} className="mt-16 origin-top">
          <Flow />
        </motion.div>

        <div className="mt-14 grid grid-cols-2 border-t hairline md:grid-cols-4">
          {figures.map((f, i) => (
            <motion.div key={f.label} initial={{ opacity: 0, y: 20 }} animate={{ opacity: 1, y: 0 }} transition={{ delay: 1.3 + i * 0.1, duration: 0.8 }}
              className="border-b hairline py-6 pr-6 md:border-b-0 md:[&:not(:first-child)]:border-l md:[&:not(:first-child)]:pl-6">
              <div className="display text-[clamp(2.2rem,4vw,3.4rem)] leading-none tabular">
                <CountUp to={f.to} decimals={f.decimals ?? 0} duration={2} separator="," />
                <span className="text-lime">{f.suffix}</span>
              </div>
              <p className="mt-3 max-w-[22ch] text-sm leading-snug text-fg-2">{f.label}</p>
            </motion.div>
          ))}
        </div>
      </div>
    </section>
  )
}
