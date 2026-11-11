# Rules for coding agents

`docs/spec.md` is the single source of truth. Read it and `GLOSSARY.md` before starting an issue.
Read `docs/research/` when an issue cites it or when you doubt a claim in the spec.

## Taking an issue

1. Pick an open issue with zero open blockers:
   `gh api repos/canhta/threadwalker/issues/<n> --jq .issue_dependencies_summary.blocked_by` prints 0.
2. Claim it: `gh issue edit <n> --add-assignee @me`. Skip issues that already have an assignee.
3. Work directly on `main` (this is a greenfield solo project — do not create feature branches or PRs unless an issue explicitly asks for a throwaway spike branch).
4. Build the "Done when" list, one item at a time, test first.
5. Commit with `Closes #<n>`, push directly to `main`, and comment on the issue with the evidence
   under each "Done when" item (test name, command output, or file path), then close the issue.

The issue is done when every "Done when" box is ticked with evidence and
`go vet ./... && go test ./...` passes.

## Scope

- A change touches only what its issue's Scope lists. Anything else you notice becomes a new issue.
- If the issue and the spec disagree, or the spec is silent, ask in the issue before writing code.
- A changed decision is an edit to `docs/spec.md` in the same commit. The repo holds one statement of
  each decision, and one file per topic: extend the existing file. Never keep dual copies of
  templates, schemas or code in `docs/spec.md` once the canonical file exists in the repo; keep the
  contract in `docs/spec.md` and point to the file.
- Where the spec says "untested" or lists an unknown, measure it and record the result in
  `docs/research/`.
- `AGENTS.md`, the guards and the invariants below change only when the owner asks for it.

## Code

- Go, single binary, standard library first. Add a dependency only when the issue names it.
- Every network call takes a `context.Context` with a timeout.
- Errors are returned and wrapped with what was being attempted; only `main` exits the process.
- Time and randomness come from an injected clock and source, so tests are deterministic.
- SQL uses parameters. Schema changes are new migration files.
- Logging is `log/slog`; log ids, counts and reasons.
- Every branch, loop and parser has a test. Tests use `httptest` servers and saved fixtures, and
  run offline.

## Live systems

- Work in dry run. Real posts and replies happen only in an issue whose Scope says so.
- Use the VPS, the Threads account, Telegram or a paid API only when the issue's Scope names it.
- Issues labelled `needs-owner` wait for the owner to supply an account, key or login. Ask in the
  issue and stop there.

## When to stop and report

Comment on the issue with what you tried and what you found, then stop, when:

- the same approach has failed twice;
- a "Done when" item cannot be met as written;
- finishing would break an invariant or exceed the issue's Scope.

## Invariants

Each commit keeps all five true:

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
