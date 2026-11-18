// A small, faithful port of DeltaView's delta rules, used by the playground
// and the in-browser simulation. Same algebra as internal/ops in the Go code:
// Z-sets, a linear filter, a join with two arrangements, and a group
// aggregate that emits -old / +new per touched group.

export type Value = string | number | null
export type Row = Value[]
export type Entry = { row: Row; w: number }

const key = (r: Row) => JSON.stringify(r)

export class ZSet {
  m = new Map<string, Entry>()
  add(row: Row, w: number) {
    if (w === 0) return this
    const k = key(row)
    const e = this.m.get(k)
    if (!e) this.m.set(k, { row, w })
    else if ((e.w += w) === 0) this.m.delete(k)
    return this
  }
  merge(o: ZSet) {
    o.m.forEach((e) => this.add(e.row, e.w))
    return this
  }
  entries(): Entry[] {
    return [...this.m.values()].sort((a, b) => (key(a.row) < key(b.row) ? -1 : 1))
  }
  get size() {
    return this.m.size
  }
}

// orders: [id, customer_id, status, amount]   customers: [id, name, city]
export type Order = [number, number | null, string, number]
export type Customer = [number, string, string]

export type StepTrace = {
  dOrders: Entry[]
  dCustomers: Entry[]
  filtered: Entry[]
  joined: Entry[]
  view: Entry[]
}

// revenue_by_city:
//   SELECT c.city, COUNT(*), SUM(o.amount) FROM orders o JOIN customers c
//   ON o.customer_id = c.id WHERE o.status = 'paid' GROUP BY c.city
export class RevenueByCity {
  private left = new Map<string, ZSet>() // arrangement of paid orders by customer_id
  private right = new Map<string, ZSet>() // arrangement of customers by id
  private groups = new Map<string, { count: number; sum: number }>()
  view = new ZSet()

  private arr(side: Map<string, ZSet>, k: string) {
    let z = side.get(k)
    if (!z) side.set(k, (z = new ZSet()))
    return z
  }

  step(dOrders: ZSet, dCustomers: ZSet): StepTrace {
    // Filter is linear: d_out = filter(d_in).
    const filtered = new ZSet()
    dOrders.m.forEach((e) => e.row[2] === 'paid' && filtered.add(e.row, e.w))

    // Join: out = dA ⋈ B_old; A += dA; out += A_new ⋈ dB; B += dB.
    const joined = new ZSet()
    filtered.m.forEach((a) => {
      if (a.row[1] === null) return // NULL keys never match
      this.right.get(String(a.row[1]))?.m.forEach((b) => joined.add([...a.row, ...b.row], a.w * b.w))
    })
    filtered.m.forEach((a) => a.row[1] !== null && this.arr(this.left, String(a.row[1])).add(a.row, a.w))
    dCustomers.m.forEach((b) => {
      this.left.get(String(b.row[0]))?.m.forEach((a) => joined.add([...a.row, ...b.row], a.w * b.w))
    })
    dCustomers.m.forEach((b) => this.arr(this.right, String(b.row[0])).add(b.row, b.w))

    // Aggregate: per touched group, emit -old and +new.
    const out = new ZSet()
    const touched = new Map<string, Entry[]>()
    joined.m.forEach((e) => {
      const city = String(e.row[6])
      touched.set(city, [...(touched.get(city) ?? []), e])
    })
    touched.forEach((rows, city) => {
      const st = this.groups.get(city) ?? { count: 0, sum: 0 }
      const old = st.count > 0 ? ([city, st.count, round(st.sum)] as Row) : null
      for (const e of rows) {
        st.count += e.w
        st.sum += e.w * Number(e.row[3])
      }
      const now = st.count > 0 ? ([city, st.count, round(st.sum)] as Row) : null
      if (st.count === 0) this.groups.delete(city)
      else this.groups.set(city, st)
      if (old && now && key(old) === key(now)) return
      if (old) out.add(old, -1)
      if (now) out.add(now, 1)
    })
    this.view.merge(out)
    return {
      dOrders: dOrders.entries(),
      dCustomers: dCustomers.entries(),
      filtered: filtered.entries(),
      joined: joined.entries(),
      view: out.entries(),
    }
  }
}

const round = (n: number) => Math.round(n * 100) / 100

// A tiny "database" so the simulation produces real -old/+new pairs.
export class Sim {
  orders = new Map<number, Order>()
  customers = new Map<number, Customer>()
  ivm = new RevenueByCity()
  lsn = 0x1b39470
  private rnd: () => number
  static cities = ['Delhi', 'Mumbai', 'Pune', 'Goa', 'Jaipur']
  static names = ['Asha', 'Ravi', 'Meera', 'Kabir', 'Zoya', 'Arjun', 'Ira', 'Dev']

  constructor(seed = 7) {
    let s = seed
    this.rnd = () => ((s = (s * 1103515245 + 12345) % 2147483648) / 2147483648)
    const dc = new ZSet()
    for (let i = 1; i <= 12; i++) {
      const c: Customer = [i, Sim.names[i % 8], Sim.cities[i % 5]]
      this.customers.set(i, c)
      dc.add(c, 1)
    }
    const dO = new ZSet()
    for (let i = 1; i <= 40; i++) {
      const o: Order = [i, 1 + (i % 12), this.rnd() < 0.7 ? 'paid' : 'pending', this.amount()]
      this.orders.set(i, o)
      dO.add(o, 1)
    }
    this.ivm.step(dO, dc)
  }

  private amount() {
    return Math.round((20 + this.rnd() * 480) * 100) / 100
  }
  private pick<T>(xs: T[]) {
    return xs[Math.floor(this.rnd() * xs.length)]
  }

  // One random committed transaction; returns its SQL and the view delta.
  tick(): { sql: string; trace: StepTrace; lsn: number } {
    const dO = new ZSet()
    const dC = new ZSet()
    const r = this.rnd()
    let sql: string
    if (r < 0.35) {
      const id = 41 + Math.floor(this.rnd() * 60)
      const old = this.orders.get(id)
      const o: Order = [id, 1 + Math.floor(this.rnd() * 12), 'paid', this.amount()]
      if (old) dO.add(old, -1)
      dO.add(o, 1)
      this.orders.set(id, o)
      sql = old
        ? `UPDATE orders SET customer_id = ${o[1]}, status = 'paid', amount = ${o[3]} WHERE id = ${id}`
        : `INSERT INTO orders VALUES (${id}, ${o[1]}, 'paid', ${o[3]})`
    } else if (r < 0.6) {
      const o = this.pick([...this.orders.values()])
      const n: Order = [o[0], o[1], o[2] === 'paid' ? 'refunded' : 'paid', o[3]]
      dO.add(o, -1).add(n, 1)
      this.orders.set(o[0], n)
      sql = `UPDATE orders SET status = '${n[2]}' WHERE id = ${o[0]}`
    } else if (r < 0.8) {
      const c = this.pick([...this.customers.values()])
      const n: Customer = [c[0], c[1], this.pick(Sim.cities.filter((x) => x !== c[2]))]
      dC.add(c, -1).add(n, 1)
      this.customers.set(c[0], n)
      sql = `UPDATE customers SET city = '${n[2]}' WHERE id = ${c[0]}`
    } else {
      const o = this.pick([...this.orders.values()])
      dO.add(o, -1)
      this.orders.delete(o[0])
      sql = `DELETE FROM orders WHERE id = ${o[0]}`
    }
    this.lsn += 0x58 + Math.floor(this.rnd() * 0x200)
    return { sql, trace: this.ivm.step(dO, dC), lsn: this.lsn }
  }
}

export const fmtLSN = (n: number) => `0/${n.toString(16).toUpperCase()}`
