import { motion, type HTMLMotionProps } from 'motion/react'
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

const ease = [0.22, 1, 0.36, 1] as const

/** Fades and lifts its children the first time they scroll into view. */
export function Reveal({ children, delay = 0, y = 28, className, ...rest }: HTMLMotionProps<'div'> & { delay?: number; y?: number }) {
  return (
    <motion.div
      initial={{ opacity: 0, y }}
      whileInView={{ opacity: 1, y: 0 }}
      viewport={{ once: true, margin: '-12% 0px' }}
      transition={{ duration: 0.9, ease, delay }}
      className={className}
      {...rest}
    >
      {children as ReactNode}
    </motion.div>
  )
}

/** Numbered section header: "02 — How it works" then a large headline. */
export function SectionHead({ n, label, title, kicker }: { n: string; label: string; title: ReactNode; kicker?: ReactNode }) {
  return (
    <div className="grid grid-cols-12 gap-x-6 gap-y-6 border-t hairline pt-6">
      <Reveal className="col-span-12 flex items-baseline gap-4 md:col-span-3">
        <span className="font-mono text-sm text-ink">{n}</span>
        <span className="eyebrow">{label}</span>
      </Reveal>
      <div className="col-span-12 md:col-span-9">
        <Reveal delay={0.05}>
          <h2 className="max-w-4xl text-[clamp(2.1rem,4.6vw,4.2rem)] leading-[1.02] font-medium tracking-[-0.035em] text-balance">
            {title}
          </h2>
        </Reveal>
        {kicker && (
          <Reveal delay={0.12}>
            <p className="mt-6 max-w-2xl text-lg leading-relaxed text-ink-2 text-pretty">{kicker}</p>
          </Reveal>
        )}
      </div>
    </div>
  )
}

/** The Z-set weight chip: the one place color carries meaning, always with its sign. */
export function W({ w, className, dark }: { w: number; className?: string; dark?: boolean }) {
  return (
    <span
      className={cn(
        'inline-flex min-w-[2.6ch] justify-center rounded-[3px] px-1 font-mono text-[0.78em] font-medium tabular',
        dark
          ? w > 0 ? 'bg-[#7fc89a]/15 text-[#8fd3a8]' : 'bg-[#ff8a65]/15 text-[#ff9b7a]'
          : w > 0 ? 'bg-plus/12 text-plus' : 'bg-minus/12 text-minus',
        className,
      )}
    >
      {w > 0 ? `+${w}` : `−${Math.abs(w)}`}
    </span>
  )
}

export function Serif({ children }: { children: ReactNode }) {
  return <span className="font-serif font-normal italic tracking-[-0.01em]">{children}</span>
}

export function fmtRow(row: (string | number | null)[]) {
  return '(' + row.map((v) => (v === null ? 'NULL' : typeof v === 'number' ? fmtNum(v) : v)).join(', ') + ')'
}

export function fmtNum(n: number) {
  return Number.isInteger(n) ? n.toLocaleString('en-US') : n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })
}
