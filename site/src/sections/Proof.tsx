import { AnimatePresence, motion } from 'motion/react'
import { useState } from 'react'
import SpotlightCard from '@/components/rb/SpotlightCard'
import CountUp from '@/components/rb/CountUp'
import { Terminal, AnimatedSpan, TypingAnimation } from '@/components/magicui/terminal'
import { proof } from '@/lib/data'
import { Reveal, SectionHead, Serif } from './ui'

const loop = [
  'for each transaction from the slot:',
  '    if batch.EndLSN <= applied_lsn: skip',
  '    tx = store.Begin()',
  '    run every circuit with tx',
  '    tx.Set(m/applied_lsn, batch.EndLSN)',
  '    tx.Commit(sync = true)',
  '    acknowledge batch.EndLSN to Postgres',
  '    publish deltas to subscribers',
]

const crashes = [
  { at: 4, name: 'before the commit', disk: 'old state, old LSN', after: 'Postgres re-sends the transaction. It is applied exactly once.' },
  { at: 5, name: 'after the commit, before the ack', disk: 'new state, new LSN', after: 'Postgres re-sends it. Its LSN is not newer than the stored one, so it is skipped.' },
  { at: 6, name: 'after the ack', disk: 'new state, new LSN', after: 'Nothing is re-sent. Nothing to do.' },
]

export function Guarantees() {
  const [c, setC] = useState(1)
  return (
    <div id="guarantees" className="mx-auto max-w-[1400px] px-5 pt-28 md:px-10">
      <div className="grid grid-cols-12 gap-x-6 gap-y-10">
        <Reveal className="col-span-12 lg:col-span-5">
          <p className="eyebrow">Exactly once</p>
          <h3 className="mt-4 text-[clamp(1.8rem,3vw,2.6rem)] leading-tight font-medium tracking-[-0.035em]">
            At-least-once delivery, plus state and position saved <Serif>together</Serif>, equals exactly-once.
          </h3>
          <p className="mt-5 text-ink-2">Pick a moment to pull the plug. Each one is a fail point in the engine, exercised 30 times in a row by the crash test.</p>
          <div className="mt-8 flex flex-col gap-2">
            {crashes.map((x, k) => (
              <button key={x.name} onClick={() => setC(k)}
                className={`flex items-center justify-between border px-4 py-3 text-left text-[14px] transition-colors ${k === c ? 'border-ink bg-ink text-paper' : 'hairline hover:border-ink'}`}>
                <span>Crash {x.name}</span><span className="font-mono text-[11px] opacity-60">kill -9</span>
              </button>
            ))}
          </div>
        </Reveal>
        <Reveal delay={0.1} className="col-span-12 lg:col-span-7">
          <div className="border border-ink bg-paper">
            <div className="border-b border-ink px-4 py-2 font-mono text-[11px] text-ink-2">internal/engine/apply.go</div>
            <div className="py-3 font-mono text-[13px] leading-7">
              {loop.map((l, k) => (
                <div key={k}>
                  <div className={`flex gap-4 px-4 transition-colors ${k <= crashes[c].at ? 'text-ink' : 'text-ink-3'}`}>
                    <span className="w-5 shrink-0 text-right text-ink-3">{k + 1}</span>
                    <span className="whitespace-pre">{l}</span>
                  </div>
                  {k === crashes[c].at && (
                    <motion.div layoutId="crash" className="mx-4 my-1 flex items-center gap-3 border-y border-dashed border-minus py-1 text-minus">
                      <span>✕</span><span className="text-[12px]">process killed here</span>
                    </motion.div>
                  )}
                </div>
              ))}
            </div>
            <AnimatePresence mode="wait">
              <motion.div key={c} initial={{ opacity: 0, y: 8 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }}
                className="grid grid-cols-1 border-t border-ink md:grid-cols-2">
                <div className="border-b border-ink p-4 md:border-r md:border-b-0">
                  <p className="eyebrow">On disk</p><p className="mt-2 text-[15px]">{crashes[c].disk}</p>
                </div>
                <div className="p-4"><p className="eyebrow">After restart</p><p className="mt-2 text-[15px]">{crashes[c].after}</p></div>
              </motion.div>
            </AnimatePresence>
          </div>
        </Reveal>
      </div>
    </div>
  )
}

export function Proof() {
  return (
    <section id="proof" className="mx-auto max-w-[1400px] px-5 pt-36 md:px-10">
      <SectionHead
        n="05"
        label="Proof"
        title={<>Postgres is the <Serif>judge</Serif>.</>}
        kicker="A differential tester writes random transactions, waits on a heartbeat row until the engine has caught up, then compares every view with the same SELECT run on Postgres, as multisets. Planted bugs prove the tester can see them."
      />
      <div className="mt-14 grid grid-cols-1 gap-px border border-ink bg-ink sm:grid-cols-2 lg:grid-cols-3">
        {proof.map((p, k) => (
          <Reveal key={p.label} delay={k * 0.06} className="bg-paper">
            <SpotlightCard theme="light" spotlightColor="rgba(240,196,25,0.28)" className="!h-full !rounded-none !border-0 !bg-paper !p-7 !shadow-none">
              <div className="text-[clamp(2.6rem,4.6vw,4.2rem)] leading-none font-medium tracking-[-0.05em] tabular">
                <CountUp to={p.value} separator="," duration={1.8} />
              </div>
              <p className="mt-2 text-[15px] font-medium">{p.label}</p>
              <p className="mt-3 max-w-[34ch] text-[13.5px] leading-relaxed text-ink-2">{p.detail}</p>
            </SpotlightCard>
          </Reveal>
        ))}
      </div>
      <div className="mt-10 grid grid-cols-12 gap-6">
        <Reveal className="col-span-12 lg:col-span-5">
          <p className="eyebrow">Planted bugs, all caught</p>
          <ul className="mt-4 divide-y divide-[var(--color-rule)] border-y hairline text-[14px]">
            {[
              ['Filter ignores deletes', 'after 20 txns'],
              ['MIN ignores deletes', 'after 20 txns'],
              ['Join drops dA ⋈ dB', 'first check'],
              ['Join counts dA ⋈ dB twice', 'first check'],
              ['Integer division floors', '−7 / 2'],
              ['Ack before commit, then crash', 'engine refuses'],
            ].map(([a, b]) => (
              <li key={a} className="flex justify-between py-2.5"><span>{a}</span><span className="font-mono text-[12px] text-minus">{b}</span></li>
            ))}
          </ul>
        </Reveal>
        <Reveal delay={0.1} className="col-span-12 lg:col-span-7">
          <Terminal title="make difftest50" className="max-w-none">
            <TypingAnimation className="text-paper/60" duration={30}>$ scripts/difftest.sh "1 … 50" 1000 10</TypingAnimation>
            <AnimatedSpan className="text-paper">seed 1: 1000 transactions, 101 checks of 14 views, all equal</AnimatedSpan>
            <AnimatedSpan className="text-paper">seed 2: 1000 transactions, 101 checks of 14 views, all equal</AnimatedSpan>
            <AnimatedSpan className="text-paper/40">…</AnimatedSpan>
            <AnimatedSpan className="text-paper">seed 50: 1000 transactions, 101 checks of 14 views, all equal</AnimatedSpan>
            <AnimatedSpan className="text-[#7fc89a]">difftest: 50 seed(s) passed</AnimatedSpan>
            <TypingAnimation className="text-paper/60" duration={30}>$ scripts/crashtest.sh kill 200</TypingAnimation>
            <AnimatedSpan className="text-paper">crashtest: 200 cycles (kill) then a clean differential check</AnimatedSpan>
            <AnimatedSpan className="text-[#7fc89a]">check passed: 14 views equal to postgres</AnimatedSpan>
          </Terminal>
        </Reveal>
      </div>
    </section>
  )
}
