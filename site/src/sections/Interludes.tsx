import ScrollReveal from '@/components/rb/ScrollReveal'
import ScrollVelocity from '@/components/rb/ScrollVelocity'

/** A paragraph that sharpens word by word as it scrolls past. */
export function Statement() {
  return (
    <section className="mx-auto max-w-[1400px] px-5 pt-36 md:px-10">
      <div className="grid grid-cols-12 gap-6">
        <p className="eyebrow col-span-12 md:col-span-3">The idea</p>
        <div className="col-span-12 md:col-span-9">
          <ScrollReveal effect="rise" mode="scrub" splitBy="words" baseOpacity={0.08} enableBlur blurStrength={6}
            textClassName="display !text-[clamp(1.9rem,3.8vw,3.4rem)] !leading-[1.08] !font-bold">
            Every insert, update and delete is the same thing: a row with a weight. Plus one adds it, minus one takes it
            away. Each operator knows a single rule, delta in and delta out, so the work is the size of the change.
          </ScrollReveal>
        </div>
      </div>
    </section>
  )
}

/** Operation names that speed up and skew with scroll velocity. */
export function Velocity() {
  return (
    <section className="mt-28 border-y hairline py-8" aria-hidden>
      <ScrollVelocity
        texts={['+1 INSERT  ·  −1 DELETE  ·  −1 +1 UPDATE  ·', 'FILTER  ·  JOIN  ·  AGGREGATE  ·  VIEW  ·']}
        velocity={60}
        numCopies={5}
        skewWithVelocity={8}
        outlineAlternate
        fadeEdges
        className="display !text-[clamp(3rem,8vw,7.5rem)] !leading-none text-fg"
        scrollerClassName="scroller py-2"
      />
    </section>
  )
}
