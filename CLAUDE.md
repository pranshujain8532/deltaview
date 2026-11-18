# DeltaView

Incremental view maintenance engine. It reads Postgres logical replication and keeps SQL views
(filter, join, aggregate) up to date by processing only changed rows. Go, single process.

## Current state
@docs/PROGRESS.md

## Every session
1. The state above says which milestone is active and what is left.
2. Read docs/milestones/<ID>.md for the active milestone, then only the files it lists under "Read first".
3. Give a short plan: files to create or change, tests to write, Postgres behaviour you are unsure about. Wait for my OK.
4. Write the approved plan into docs/PROGRESS.md as a checklist. Tick items as they pass.
5. Work in small steps. Commit after each green step.
6. Before stopping: update docs/PROGRESS.md and tell me the next step.

## What not to load
- Do not read docs/human/. It is for the human.
- Do not read other milestone files, or all of docs/, for context.
- Use the Explore subagent for codebase searches and the test-runner subagent for test runs.

## Hard rules
- One milestone at a time. Do not build ahead.
- Tests first. Never delete, skip or loosen a test or an expected value to get green. If a test looks wrong, stop and tell me.
- All operator state goes through state.Tx. Only the apply loop writes state. No locks inside operators.
- Never acknowledge an LSN to Postgres before the batch containing it is committed with sync.
- numeric columns are decimals, never float64.
- Unsupported SQL is rejected with an error that names the feature. Never guess.
- When unsure how Postgres behaves, write a small test against the Docker Postgres. Do not guess.
- Standard library first. Ask before adding a dependency. Use go get MODULE@latest; do not pin versions from memory.
- Wrap errors with context. No panics outside main and tests.
- Exported types and functions get a comment saying why they exist.

## Finishing a task
- Run tests through the test-runner subagent and report its real output.
- Before saying a milestone is done, run the correctness-reviewer subagent on the diff and show me its findings unedited.
- List every assumption made and anything skipped or stubbed.
- Explain the main code path in plain words, as if I must defend it in an interview.

## Commands
make up | make down | make check | make difftest SEED=n | make crashtest | make bench

## Map
- docs/design.md: types, interfaces, state key layout, SQL subset, HTTP API, repo layout
- docs/concepts.md: the delta rules with worked examples
- docs/architecture.md: components and data flow
- docs/testing.md, docs/benchmarks.md
- docs/decisions.md: mine. Never write it for me. Suggest entries in docs/PROGRESS.md instead.
