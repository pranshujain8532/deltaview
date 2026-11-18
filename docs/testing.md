# Testing

| Layer | What it checks | Bugs it catches |
|---|---|---|
| Property tests | Z-set laws, encoding round trip and order | Broken foundations |
| Operator unit tests | Hand-worked deltas from docs/concepts.md | Wrong delta rules |
| Expression tests vs Postgres | Our evaluator equals Postgres on the same inputs | NULL logic, division, casts |
| Differential tester | View equals the real query after random workloads | Almost everything |
| Mutation checks | Planted bugs must make the tester fail | A tester that is too weak |
| Crash tests | Kill at random and at fail points, then differential check | Lost or double-applied changes |
| Snapshot test | Snapshot while writes continue | Gaps and double counts at the handoff |

## Z-set laws to prove

```text
a + (-a) = empty        a + b = b + a        (a + b) + c = a + (b + c)
no entry with weight 0 is stored
decode(encode(row)) = row        encode order = value order
```

## Mutation checks to run by hand

| Milestone | Planted bug | The tester must fail |
|---|---|---|
| M3 | Filter ignores deletes | yes |
| M4 | MIN ignores deletes | yes |
| M6 | Drop the dA join dB contribution | yes |
| M6 | Count dA join dB twice | yes |
| M8 | Ack before commit, then crash | yes, in the crash test |
