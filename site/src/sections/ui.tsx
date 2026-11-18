import { motion, type HTMLMotionProps } from 'motion/react'
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import type { StepTrace } from '@/lib/ivm'
import type { Wave } from '@/components/DeltaFlow'

export const ease = [0.22, 1, 0.36, 1] as const

/** Fades and lifts its children the first time they scroll into view. */
export function Reveal({ children, delay = 0, y = 28, className, ...rest }: HTMLMotionProps<'div'> & { delay?: number; y?: number }) {
  return (
    <motion.div
      initial={{ opacity: 0, y, filter: 'blur(6px)' }}
      whileInView={{ opacity: 1, y: 0, filter: 'blur(0px)' }}
      viewport={{ once: true, margin: '-12% 0px' }}
      transition={{ duration: 0.9, ease, delay }}
      className={className}
      {...rest}
    >
      {children as ReactNode}
    </motion.div>
  )
}

/** Numbered section header: "02 / Mechanism", then a large display headline. */
export function SectionHead({ n, label, title, kicker }: { n: string; label: string; title: ReactNode; kicker?: ReactNode }) {
  return (
    <div className="grid grid-cols-12 gap-x-6 gap-y-6">
      <Reveal className="col-span-12 md:col-span-3">
        <div className="flex items-center gap-3">
          <span className="grid h-7 min-w-7 place-items-center rounded-full border hairline px-2 font-mono text-[11px] text-lime">{n}</span>
          <span className="eyebrow">{label}</span>
        </div>
      </Reveal>
      <div className="col-span-12 md:col-span-9">
        <Reveal delay={0.05}>
          <h2 className="display max-w-4xl text-[clamp(2.2rem,5vw,4.6rem)] leading-[0.98] text-balance">{title}</h2>
        </Reveal>
        {kicker && (
          <Reveal delay={0.12}>
            <p className="mt-6 max-w-2xl text-lg leading-relaxed text-fg-2 text-pretty">{kicker}</p>
          </Reveal>
        )}
      </div>
    </div>
  )
}

/** An accent word in the signal lime. */
export function Hi({ children }: { children: ReactNode }) {
  return <span className="text-lime">{children}</span>
}

/** The Z-set weight chip: the one place color carries meaning, always with its sign. */
export function W({ w, className }: { w: number; className?: string }) {
  return (
    <span
      className={cn(
        'inline-flex min-w-[2.7ch] justify-center rounded-[4px] px-1 font-mono text-[0.78em] font-medium tabular',
        w > 0 ? 'bg-plus/20 text-plus-hi' : 'bg-minus/20 text-minus-hi',
        className,
      )}
    >
      {w > 0 ? `+${w}` : `−${Math.abs(w)}`}
    </span>
  )
}

export function fmtRow(row: readonly (string | number | boolean | null)[]) {
  return '(' + row.map((v) => (v === null ? 'NULL' : typeof v === 'number' ? fmtNum(v) : String(v))).join(', ') + ')'
}

export function fmtNum(n: number) {
  return Number.isInteger(n) ? n.toLocaleString('en-US') : n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}

const signs = (es: { w: number }[], cap = 7) => es.slice(0, cap).map((e) => Math.sign(e.w))

/** Turns a step of the in-browser engine into animation waves, one per operator. */
export function wavesFromTrace(t: StepTrace): Wave[] {
  return [
    { stage: 0, signs: signs(t.dOrders) },
    { stage: 1, signs: signs(t.filtered) },
    { stage: 1, signs: signs(t.dCustomers), fromTop: true },
    { stage: 2, signs: signs(t.joined) },
    { stage: 3, signs: signs(t.view) },
  ]
}

/** A real engine only exposes a view's output delta: animate it end to end. */
export function wavesFromDelta(ws: number[]): Wave[] {
  const s = ws.slice(0, 7).map(Math.sign)
  return [0, 1, 2, 3].map((stage) => ({ stage: stage as Wave['stage'], signs: s }))
}
