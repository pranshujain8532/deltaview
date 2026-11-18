import { AnimatePresence, motion, useInView } from 'motion/react'
import { useEffect, useRef, useState } from 'react'
import { RevenueByCity, ZSet, type Customer, type Entry, type Order, type StepTrace } from '@/lib/ivm'
import { SectionHead, Hi, W, fmtNum, fmtRow } from './ui'

type World = { orders: Map<number, Order>; customers: Map<number, Customer>; ivm: RevenueByCity; nextId: number }

function seed(): World {
  const customers = new Map<number, Customer>([[7, [7, 'Asha', 'Delhi']], [8, [8, 'Ravi', 'Mumbai']], [9, [9, 'Meera', 'Pune']]])
  const orders = new Map<number, Order>([[1, [1, 7, 'paid', 100]], [2, [2, 7, 'paid', 50]], [3, [3, 8, 'paid', 120]], [5, [5, 9, 'pending', 40]]])
  const ivm = new RevenueByCity()
  const dc = new ZSet(), dO = new ZSet()
  customers.forEach((c) => dc.add(c, 1))
  orders.forEach((o) => dO.add(o, 1))
  ivm.step(dO, dc)
  return { orders, customers, ivm, nextId: 10 }
}

type Action = { label: string; run: (w: World) => { sql: string; dO: ZSet; dC: ZSet } | null }

const actions: Action[] = [
  {
    label: 'Meera places a paid order',
    run: (w) => {
      const o: Order = [w.nextId++, 9, 'paid', 75]
      w.orders.set(o[0], o)
      return { sql: `INSERT INTO orders VALUES (${o[0]}, 9, 'paid', 75)`, dO: new ZSet().add(o, 1), dC: new ZSet() }
    },
  },
  {
    label: 'Asha moves city',
    run: (w) => {
      const c = w.customers.get(7)
      if (!c) return null
      const n: Customer = [7, 'Asha', c[2] === 'Delhi' ? 'Mumbai' : 'Delhi']
      w.customers.set(7, n)
      return { sql: `UPDATE customers SET city = '${n[2]}' WHERE id = 7`, dO: new ZSet(), dC: new ZSet().add(c, -1).add(n, 1) }
    },
  },
  {
    label: 'Refund a paid order',
    run: (w) => {
      const o = [...w.orders.values()].find((x) => x[2] === 'paid')
      if (!o) return null
      const n: Order = [o[0], o[1], 'refunded', o[3]]
      w.orders.set(o[0], n)
      return { sql: `UPDATE orders SET status = 'refunded' WHERE id = ${o[0]}`, dO: new ZSet().add(o, -1).add(n, 1), dC: new ZSet() }
    },
  },
  {
    label: 'Ravi is deleted, orders stay',
    run: (w) => {
      const c = w.customers.get(8)
      if (!c) return null
      w.customers.delete(8)
      return { sql: 'DELETE FROM customers WHERE id = 8', dO: new ZSet(), dC: new ZSet().add(c, -1) }
    },
  },
]

function Col({ title, note, entries, id }: { title: string; note: string; entries: Entry[]; id: number }) {
  return (
    <div className="min-w-0 overflow-x-auto border-t border-line-2 pt-3">
      <p className="eyebrow !text-fg">{title}</p>
      <p className="mt-1 h-9 text-[12px] leading-snug text-fg-3">{note}</p>
      <div className="mt-3 min-h-[48px] font-mono text-[12px] leading-6 md:min-h-[120px]">
        <AnimatePresence mode="popLayout">
          {entries.length === 0 ? (
            <motion.p key={`e${id}`} initial={{ opacity: 0 }} animate={{ opacity: 1 }} className="text-fg-3">∅ empty</motion.p>
          ) : (
            entries.map((e, k) => (
              <motion.div key={`${id}-${k}`} initial={{ opacity: 0, x: -10 }} animate={{ opacity: 1, x: 0 }}
                transition={{ delay: k * 0.06, duration: 0.35 }} className="flex items-start gap-2">
                <W w={e.w} className="mt-1 shrink-0" /> <span className="whitespace-nowrap">{fmtRow(e.row)}</span>
              </motion.div>
            ))
          )}
        </AnimatePresence>
      </div>
    </div>
  )
}

export function Playground() {
  const world = useRef<World>(seed())
  const [trace, setTrace] = useState<(StepTrace & { sql: string; id: number }) | null>(null)
  const [view, setView] = useState<Entry[]>(world.current.ivm.view.entries())
  const [changed, setChanged] = useState<Set<string>>(new Set())

  const fire = (a: Action) => {
    const r = a.run(world.current)
    if (!r) return
    const t = world.current.ivm.step(r.dO, r.dC)
    setTrace({ ...t, sql: r.sql, id: Date.now() })
    setView(world.current.ivm.view.entries())
    setChanged(new Set(t.view.filter((e) => e.w > 0).map((e) => String(e.row[0]))))
  }
  const reset = () => {
    world.current = seed()
    setTrace(null)
    setView(world.current.ivm.view.entries())
    setChanged(new Set())
  }

  // Play the signature example once, the first time the section is seen.
  const box = useRef<HTMLDivElement>(null)
  const seen = useInView(box, { once: true, amount: 0.4 })
  useEffect(() => {
    if (!seen) return
    const t = setTimeout(() => fire(actions[1]), 600)
    return () => clearTimeout(t)
  }, [seen])

  const input = trace ? [...trace.dOrders, ...trace.dCustomers] : []
  return (
    <section id="playground" className="mx-auto max-w-[1400px] px-5 pt-36 md:px-10">
      <SectionHead
        n="03"
        label="Playground"
        title={<>Inserts, deletes and updates are <Hi>one thing</Hi>: a delta.</>}
        kicker="Press a change. The engine below is a line-for-line port of DeltaView's operators, running in your browser. Nobody wrote special code for a customer moving city; it falls out of the algebra."
      />
      <div ref={box} className="mt-14 grid grid-cols-12 gap-6">
        <div className="col-span-12 flex flex-wrap gap-2 lg:col-span-12">
          {actions.map((a) => (
            <button key={a.label} onClick={() => fire(a)}
              className="rounded-full border hairline bg-bg-2 px-4 py-2 text-[14px] transition-colors hover:border-lime hover:text-lime active:scale-[0.98]">
              {a.label}
            </button>
          ))}
          <button onClick={reset} className="rounded-full px-4 py-2 text-[14px] text-fg-3 underline-offset-4 hover:text-fg hover:underline">
            Reset
          </button>
        </div>
        <div className="col-span-12 min-h-7 font-mono text-[13px] text-fg-2">
          <AnimatePresence mode="wait">
            <motion.p key={trace?.id ?? 'none'} initial={{ opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }} exit={{ opacity: 0 }}>
              <span className="text-fg-3">=# </span>{trace ? trace.sql + ';' : 'waiting for a change…'}
            </motion.p>
          </AnimatePresence>
        </div>
        <div className="col-span-12 grid grid-cols-1 gap-6 sm:grid-cols-2 lg:col-span-9 lg:grid-cols-[1fr_0.9fr_1.7fr_1fr]">
          <Col id={trace?.id ?? 0} title="Δ input" note="the transaction as Z-sets" entries={input} />
          <Col id={trace?.id ?? 0} title="Filter" note="status = 'paid', no state" entries={trace?.filtered ?? []} />
          <Col id={trace?.id ?? 0} title="Join" note="weights multiply: wa × wb" entries={trace?.joined ?? []} />
          <Col id={trace?.id ?? 0} title="Aggregate" note="−old, +new per city" entries={trace?.view ?? []} />
        </div>
        <div className="col-span-12 lg:col-span-3">
          <div className="rounded-xl border hairline bg-[#080908] p-5 text-fg">
            <p className="font-mono text-[11px] tracking-[0.14em] text-fg-3 uppercase">revenue_by_city</p>
            <table className="mt-4 w-full font-mono text-[13px]">
              <thead className="text-fg-3">
                <tr><th className="pb-2 text-left font-normal">city</th><th className="pb-2 text-right font-normal">orders</th><th className="pb-2 text-right font-normal">revenue</th></tr>
              </thead>
              <tbody>
                <AnimatePresence>
                  {view.map((e) => (
                    <motion.tr key={String(e.row[0])} layout initial={{ opacity: 0 }}
                      animate={{ opacity: 1, backgroundColor: changed.has(String(e.row[0])) ? ['rgba(200,240,75,0.30)', 'rgba(200,240,75,0)'] : 'rgba(0,0,0,0)' }}
                      exit={{ opacity: 0 }} transition={{ duration: 1.2 }} className="border-t border-line">
                      <td className="py-2">{e.row[0]}</td>
                      <td className="py-2 text-right tabular">{e.row[1]}</td>
                      <td className="py-2 text-right tabular">{fmtNum(Number(e.row[2]))}</td>
                    </motion.tr>
                  ))}
                </AnimatePresence>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </section>
  )
}
