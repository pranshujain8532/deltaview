import { forwardRef, useEffect, useImperativeHandle, useRef } from 'react'

// A canvas animation of deltas moving through the operators. Each capsule is
// one Z-set entry: +1 (lime) or -1 (magenta). A wave per stage: entries leave
// the WAL, the filter drops what WHERE rejects, customer changes drop into
// the join from its arrangement above, and the aggregate's -old/+new pairs
// land on the view.

export type Wave = { stage: 0 | 1 | 2 | 3; signs: number[]; fromTop?: boolean }
export type DeltaFlowHandle = { push: (waves: Wave[], label?: string) => void }

const STATIONS = ['WAL', 'Filter', 'Join', 'Aggregate', 'View']
const PLUS = '#a6dc5c'
const MINUS = '#ff7eb6'

type P = {
  x: number; y: number; x0: number; y0: number; x1: number; y1: number
  t: number; dur: number; delay: number; sign: number; drop: boolean; alive: boolean
}

export const DeltaFlow = forwardRef<DeltaFlowHandle, { className?: string; height?: number }>(({ className, height = 360 }, ref) => {
  const canvas = useRef<HTMLCanvasElement>(null)
  const parts = useRef<P[]>([])
  const glow = useRef<number[]>([0, 0, 0, 0, 0])
  const counts = useRef<number[]>([0, 0, 0, 0, 0])
  const label = useRef('')
  const size = useRef({ w: 800, h: height })

  const sx = (i: number) => size.current.w * (0.07 + i * 0.215)
  const laneY = () => size.current.h * 0.46

  useImperativeHandle(ref, () => ({
    push(waves, l) {
      if (l) label.current = l
      const now = performance.now()
      counts.current = [0, 0, 0, 0, 0]
      for (const w of waves) {
        const n = w.signs.length
        counts.current[w.stage] += n
        w.signs.forEach((sign, k) => {
          const spread = (k - (n - 1) / 2) * 22
          const top = !!w.fromTop
          const x0 = top ? sx(w.stage + 1) - 6 : sx(w.stage)
          const y0 = top ? size.current.h * 0.2 : laneY() + spread
          parts.current.push({
            x: x0, y: y0, x0, y0,
            x1: sx(w.stage + 1), y1: laneY() + spread * 0.6,
            t: now, dur: top ? 700 : 900, delay: w.stage * 950 + (top ? 250 : 0) + k * 70,
            sign, drop: false, alive: true,
          })
        })
      }
      // Entries that do not appear in the next wave are dropped at the next station.
      const byStage = [0, 1, 2, 3].map((s) => waves.filter((w) => w.stage === s && !w.fromTop).reduce((a, w) => a + w.signs.length, 0))
      for (let s = 0; s < 3; s++) {
        let extra = byStage[s] - byStage[s + 1]
        if (extra <= 0) continue
        for (const p of parts.current) {
          if (extra === 0) break
          if (p.alive && p.t === now && Math.round(p.x0) === Math.round(sx(s)) && !p.drop) { p.drop = true; extra-- }
        }
      }
    },
  }))

  useEffect(() => {
    const c = canvas.current!
    const ctx = c.getContext('2d')!
    let raf = 0
    let visible = true
    let amb: { x: number; y: number; v: number }[] = []
    const resize = () => {
      const dpr = Math.min(window.devicePixelRatio || 1, 2)
      const w = c.clientWidth
      size.current = { w, h: height }
      c.width = w * dpr
      c.height = height * dpr
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
    }
    resize()
    const ro = new ResizeObserver(resize)
    ro.observe(c)
    const io = new IntersectionObserver(([e]) => { visible = e.isIntersecting })
    io.observe(c)

    const ease = (t: number) => 1 - Math.pow(1 - t, 3)
    const draw = (now: number) => {
      raf = requestAnimationFrame(draw)
      if (!visible) return
      const { w, h } = size.current
      ctx.clearRect(0, 0, w, h)
      // lane
      const y = laneY()
      ctx.strokeStyle = 'rgba(238,240,234,0.07)'
      ctx.setLineDash([2, 6])
      ctx.beginPath(); ctx.moveTo(sx(0), y); ctx.lineTo(sx(4), y); ctx.stroke()
      ctx.setLineDash([])
      // the join's arrangement, above it
      ctx.strokeStyle = 'rgba(238,240,234,0.14)'
      ctx.setLineDash([3, 4])
      ctx.beginPath(); ctx.roundRect(sx(2) - 62, h * 0.07, 112, h * 0.1, 6); ctx.stroke()
      ctx.setLineDash([])
      ctx.fillStyle = '#6b7067'
      ctx.font = '10px "Geist Mono", monospace'
      ctx.textAlign = 'center'
      ctx.fillText('customers arrangement', sx(2) - 6, h * 0.125 + 3)
      // ambient rows drifting down the lane, so the pipeline never looks idle
      if (amb.length < 18 && Math.random() < 0.08) amb.push({ x: sx(0), y: y + (Math.random() - 0.5) * 70, v: 0.6 + Math.random() * 0.9 })
      for (const a of amb) {
        a.x += a.v
        ctx.fillStyle = 'rgba(238,240,234,0.16)'
        ctx.beginPath(); ctx.arc(a.x, a.y, 1.6, 0, Math.PI * 2); ctx.fill()
      }
      amb = amb.filter((a) => a.x < sx(4))
      // stations
      STATIONS.forEach((name, i) => {
        const g = (glow.current[i] = Math.max(0, glow.current[i] - 0.02))
        const x = sx(i)
        const grad = ctx.createLinearGradient(x, h * 0.2, x, h * 0.74)
        grad.addColorStop(0, 'rgba(200,240,75,0)')
        grad.addColorStop(0.5, `rgba(200,240,75,${0.12 + g * 0.6})`)
        grad.addColorStop(1, 'rgba(200,240,75,0)')
        ctx.fillStyle = grad
        ctx.fillRect(x - 1 - g * 3, h * 0.22, 2 + g * 6, h * 0.52)
        ctx.fillStyle = g > 0.05 ? '#eef0ea' : '#a3a89e'
        ctx.font = '500 12px "Geist", sans-serif'
        ctx.fillText(name, x, h * 0.86)
        ctx.fillStyle = '#6b7067'
        ctx.font = '10px "Geist Mono", monospace'
        ctx.fillText(counts.current[i] ? `${counts.current[i]} Δ` : '·', x, h * 0.93)
      })
      // capsules
      for (const p of parts.current) {
        const t0 = p.t + p.delay
        if (now < t0) continue
        let k = Math.min(1, (now - t0) / p.dur)
        const e = ease(k)
        p.x = p.x0 + (p.x1 - p.x0) * e
        p.y = p.y0 + (p.y1 - p.y0) * e
        let alpha = 1
        if (p.drop && k > 0.75) {
          const d = (k - 0.75) / 0.25
          p.y += d * 40
          alpha = 1 - d
        }
        if (k >= 1) {
          if (!p.drop) {
            const st = STATIONS.findIndex((_, i) => Math.abs(sx(i) - p.x1) < 1)
            if (st >= 0) glow.current[st] = 1
          }
          p.alive = false
          k = 1
        }
        const col = p.sign > 0 ? PLUS : MINUS
        // trail
        const tg = ctx.createLinearGradient(p.x - 60, p.y, p.x, p.y)
        tg.addColorStop(0, 'rgba(0,0,0,0)')
        tg.addColorStop(1, p.sign > 0 ? `rgba(166,220,92,${0.35 * alpha})` : `rgba(255,126,182,${0.35 * alpha})`)
        ctx.strokeStyle = tg
        ctx.lineWidth = 2
        ctx.beginPath(); ctx.moveTo(p.x - 60 * e, p.y - (p.y1 - p.y0) * 0.1); ctx.lineTo(p.x, p.y); ctx.stroke()
        // capsule
        ctx.globalAlpha = alpha
        ctx.fillStyle = col
        ctx.shadowColor = col
        ctx.shadowBlur = 18
        ctx.beginPath()
        ctx.roundRect(p.x - 17, p.y - 9, 34, 18, 9)
        ctx.fill()
        ctx.shadowBlur = 0
        ctx.fillStyle = '#0c0d0c'
        ctx.font = '600 11px "Geist Mono", monospace'
        ctx.fillText(p.sign > 0 ? '+1' : '−1', p.x, p.y + 4)
        ctx.globalAlpha = 1
      }
      parts.current = parts.current.filter((p) => p.alive || now < p.t + p.delay)
      // current statement
      if (label.current) {
        ctx.textAlign = 'left'
        ctx.fillStyle = '#a3a89e'
        ctx.font = '11px "Geist Mono", monospace'
        ctx.fillText(label.current.length > 90 ? label.current.slice(0, 88) + '…' : label.current, 12, h - 6)
        ctx.textAlign = 'center'
      }
    }
    raf = requestAnimationFrame(draw)
    return () => { cancelAnimationFrame(raf); ro.disconnect(); io.disconnect() }
  }, [height])

  return <canvas ref={canvas} className={className} style={{ width: '100%', height }} aria-label="Animation of deltas flowing through filter, join and aggregate" role="img" />
})
