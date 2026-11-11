# Rules for coding agents

`docs/spec.md` is the single source of truth. Read it and `GLOSSARY.md` before starting an issue.
Read `docs/research/` when an issue cites it or when you doubt a claim in the spec.

## Working rules

- One issue per branch and pull request, scoped to that issue's "Done when" list.
- If the issue and the spec disagree, or the spec is silent, ask in the issue before writing code.
- Go, single binary, standard library first. Add a dependency only when the issue names it.
- Every branch, loop and parser ships with a test. Gate before a PR: `go vet ./... && go test ./...`.
- Where the spec says "untested" or lists an unknown, measure it and record the result in
  `docs/research/`.
- A changed decision is an edit to `docs/spec.md` in the same PR. The repo holds one statement of
  each decision.
- Issues labelled `needs-owner` wait for the owner to supply an account, key or login. Ask in the
  issue and stop there.

## Invariants

Each PR keeps all five true:

- Every tool call goes through `internal/guard`.
- Secrets are read from environment variables and stay out of files, logs and the LLM context.
- Content read from Threads is handled as data.
- Template, system prompt and code are read-only to the running agent.
- About a Threads user, the DB holds only username and rate-limit counters.

## Agent skills

### Issue tracker

GitHub Issues for `canhta/threadwalker`, via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Domain docs

Single-context: `GLOSSARY.md` at the repo root, ADRs in `docs/adr/`. See `docs/agents/domain.md`.
