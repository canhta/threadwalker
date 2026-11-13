# Contributing

Read [`docs/README.md`](docs/README.md) (the index of design docs) and [`GLOSSARY.md`](GLOSSARY.md) first. Each
fact lives in one doc: a change that alters a decision edits that doc in the same commit. AI coding agents also
follow [`AGENTS.md`](AGENTS.md).

## Build and test

Use the Go version in [`go.mod`](go.mod). Every change must pass:

```sh
go vet ./...
go test -count=1 ./...
go run ./cmd/threadz eval --suite internal/eval/testdata/default.json
```

Tests run offline against `httptest` servers and saved fixtures. A change that needs a network
call, a model key or a Threads account also needs an offline test with a fake transport; live calls
run only behind an opt-in environment variable. To try the scheduler without credentials:

```sh
go run ./cmd/threadz run --once --offline
```

## Issues

- One problem or feature per issue: what you expected and what happened.
- For a bug, give the command, the version or commit, and the smallest input that shows it. Remove
  keys, usernames and private text from logs.
- For a feature, say what problem it solves. If the design docs are silent or disagree, say so before
  writing code.
- Report security problems through [`SECURITY.md`](SECURITY.md), not a public issue.

## Changes

- Stay inside the issue's scope; other problems become new issues.
- Write the test first. Every branch, loop and parser gets one.
- Keep the invariants in `AGENTS.md` true.
- Edit the doc that owns the fact (find it in `docs/README.md`) in the same commit when a decision changes.

Fork the repository, branch, and open a pull request against `main`. List the commands you ran.

## Commit messages

One imperative sentence under about 72 characters, in the style of the history:

```
Fix reply spacing for a thread answered twice
Add a bound to the trend fetch
```

Put the reason in the body when it is not obvious, and end with `Closes #<number>` when the commit
completes an issue.
