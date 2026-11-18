# How to run this project with Claude Code

## First-time setup
1. Run the four setup scripts in an empty folder. Run git init and make a first commit.
2. Start claude in that folder. Run /context and check that CLAUDE.md is listed under Memory files.
3. Ask: "List the subagents you can see." You should get test-runner, correctness-reviewer, doc-drafter, Explore.
   If not, restart Claude Code once (a new agents folder needs a restart).

## The session loop
One session = one milestone, or one part of a milestone. Never one session for the whole project.

Start (new milestone):
```text
Start milestone M4. Follow the session steps in CLAUDE.md.
```

Start (continuing):
```text
Continue the active milestone. Pick up at the first unchecked item in docs/PROGRESS.md.
```

End (always, before you leave):
```text
Update docs/PROGRESS.md: done, next step, open problems, suggested decisions. Keep it under 60 lines. Commit.
```

Then, to move to the next milestone, edit "Active milestone" in docs/PROGRESS.md, run /clear, and start again.

## Context rules
| Rule | Why |
|---|---|
| Never paste docs into the chat. Name the file path | The file is read once; pasted text is carried in every later message |
| /clear between milestones and between unrelated tasks | Old conversation crowds out the files needed next |
| Anything that must survive goes in a file, not the chat | After compaction only files are reloaded; chat-only instructions are summarised away |
| Test runs and searches go to subagents | Their output stays in the subagent; only a summary comes back |
| Before a long task in a long session: /compact focus on <the current task> | You choose what the summary keeps |
| Run /context when a session feels slow or forgetful | Shows what is filling the window |
| Commit after every green step | Git is the durable memory; a fresh session can read git log |
| If Claude repeats a mistake twice, add one line to CLAUDE.md | That is what CLAUDE.md is for |
| Keep CLAUDE.md under 200 lines | Longer files cost context and are followed less reliably |

Signs a session should end: Claude re-reads files it already read, forgets a rule from
CLAUDE.md, or proposes something you rejected earlier. Run the End prompt, then /clear.

## Which model does which job
| Tier | Alias | Use for | Never for |
|---|---|---|---|
| Deep reasoning | opus (effort high) | Delta rules, exactly-once, snapshot, the tester, reviews, explaining concepts | Boilerplate |
| Daily building | sonnet | Implementing an approved plan, handlers, metrics, benchmark harness | Deciding how a delta rule works |
| Mechanical | haiku | Running tests, searching, formatting docs | Anything in internal/ops, internal/engine, internal/cdc, cmd/dvtest |
| Escalation | fable | A failing seed Opus could not root-cause in two attempts; a last review of M8 | Routine work. It may bill usage credits; check the picker |

- Default: /model opusplan (Opus plans, Sonnet codes).
- Milestones marked "opus, whole session": /model opus then /effort high.
- One hard question: put ultrathink in the prompt instead of switching.
- Each milestone file states its model on the second line.

| Job | Model |
|---|---|
| Root-cause a failing differential seed | opus, high |
| Shrink a failing seed | sonnet |
| Quiz me, explain concepts | opus |
| docs/notes.md and bug diary | haiku drafts, you rewrite |
| docs/decisions.md | you |

## Reusable prompts

Quiz (after every milestone):
```text
Quiz me on the active milestone using its "Explain it back" section. One question at a time,
like a senior interviewer. After each answer say what was wrong or missing, then go harder.
```

Review (before approving a milestone):
```text
@"correctness-reviewer (agent)" review the diff for the active milestone.
```

Failing seed:
```text
make difftest SEED=<n> fails. Do not change the tester or expected values. Reproduce it,
shrink it to the smallest failing transaction sequence, explain the root cause using
docs/concepts.md, fix the operator, add the shrunk case as a regression test.
```

## Escalation
1. Sonnet's code fails the tester: switch to opus and use the failing-seed prompt.
2. Opus fails twice on one seed: stop. Shrink by hand, write what you think is happening, then try fable once with your notes.
3. Any model proposes changing the tester or an expected value: reject it.

## Timeline (about 2 hours a day, DSA continues daily)
| Weeks | Milestones | You can now say |
|---|---|---|
| 1 | M0, M1 | I read Postgres's replication stream |
| 2 to 3 | M2, M3 | I have an end-to-end view and a tester that proves it |
| 3 to 5 | M4 | Aggregates update incrementally, including MIN and MAX |
| 5 to 6 | M5 | Views are written in SQL |
| 6 to 8 | M6 | Joins work when both sides change. Resume-ready |
| 8 to 9 | M7 | It starts on a live database with no gap |
| 9 to 11 | M8 | It survives kill -9 with exactly-once semantics |
| 11 to 12 | M9 | Clients subscribe to live changes |
| 12 to 14 | M10 | Here are the numbers and the demo |
