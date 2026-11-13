## What and why

<!-- One or two sentences. Link the issue this closes. -->

## Tests run

- [ ] `go vet ./...`
- [ ] `go test -count=1 ./...`
- [ ] `go run ./cmd/threadz eval --suite internal/eval/testdata/default.json`

<!-- Paste the commands and results, and say what is not covered. -->

## Checks

- [ ] The change stays inside its issue's scope.
- [ ] The design doc that owns the fact (see `docs/README.md`) is updated when a decision changed.
- [ ] No secrets, `.env` file or real account credentials.
- [ ] No live Threads account was used, unless the issue's scope names it.
- [ ] New behaviour has an offline test.
