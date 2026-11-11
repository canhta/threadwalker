# Local read-only dashboard (#11)

The activity page is implemented and checked against temporary SQLite databases and
synthetic account identifiers. It requires no VPS, proxy, service login or provider key.
These results establish local behavior only; real account/service qualification remains
in #2/#18/#21 and elapsed experiment evidence in #9.

## Checks

- `TestPageProjectsOnlySelectedSafeMetadata` seeds private notes, third-party handles,
  secret-looking text, prompt text and raw tool payloads, then verifies the public DTO.
- `TestHTTPResponsesNeverReflectPrivateStateOrRequests` checks HTML/CSS, GET/HEAD,
  rejected methods, query parameters, unknown paths and static database errors.
- `TestDashboardCasesExercisePublicProjectionAndReadOnlySQLite` and the public suite
  exercise the actual handler with sensitive strings in DB text/URL/JSON slots.
- `TestDashboardOpenReadOnlyNeverCreatesOrMigratesAndCannotWrite` verifies concurrent
  reads, refused writes/schema changes, absent-file behavior and cancellation identity.
- History, numeric metadata, URL ownership, escaping and configuration validation have
  dedicated public-boundary tests in `internal/dashboard/dashboard_test.go`.
- `TestDashboardCLIStartsFromPublicConfigWithoutCredentials` starts and cancels the
  actual CLI using a template with Telegram enabled and an environment getter that fails
  the test if called. No provider or Telegram key is resolved.

```sh
go vet ./... && go test ./...
go test -race ./internal/dashboard ./internal/store ./cmd/threadwalker ./internal/eval \
  -run 'Test(Page|HTTP|Publication|Server|Dashboard|ToolMetadata|CapturedDashboard)' -count=1
go run ./cmd/threadwalker eval -suite internal/eval/testdata/dashboard-v1.json \
  -seed 11 -repeat 2 -out /tmp/dashboard-eval.json
```

The public CLI suite passed **12/12** rows (three cases, two synthetic profile labels,
repeat two), with zero model calls, tokens and provider spend. The profile labels run the
same local code; this is not a model comparison. The captured operational trace is
[`dashboard-v1-events.jsonl`](../../internal/eval/testdata/dashboard-v1-events.jsonl),
verified by `TestCapturedDashboardEvents`; only the nondeterministic slog timestamp is
removed. To regenerate after reviewing a contract change:
`THREADWALKER_UPDATE_DASHBOARD_TRACE=1 go test ./internal/eval -run '^TestCapturedDashboardEvents$'`.

## Render reproduction

The canonical synthetic display data is
[`preview.sql`](../../internal/dashboard/testdata/preview.sql). This recipe derives
throwaway templates from the existing canonical example and applies existing migrations;
it does not read environment credentials. Python's bundled SQLite needs FTS5.

```sh
python3 - <<'PY'
import glob, pathlib, re, sqlite3, tempfile
root = pathlib.Path(tempfile.mkdtemp(prefix='threadwalker-dashboard-'))
(root / 'agents').mkdir()
source = pathlib.Path('agents/solo-builder.example.yaml').read_text()
for i, agent in enumerate(['synthetic-builder', 'synthetic-second']):
    text = re.sub(r'^id:.*', 'id: ' + agent, source, flags=re.M)
    text = re.sub(r'  profile_dir:.*', '  profile_dir: ' + str(root / agent), text)
    text = re.sub(r'  username:.*\n', '', text)
    text = text.replace('threads:\n', 'threads:\n  username: synthetic_' + str(i) + '\n')
    (root / 'agents' / (agent + '.yaml')).write_text(text)
db = sqlite3.connect(root / 'preview.db')
for path in sorted(glob.glob('internal/store/migrations/*.sql')):
    db.executescript(pathlib.Path(path).read_text())
db.executescript(pathlib.Path('internal/dashboard/testdata/preview.sql').read_text())
db.commit()
db.close()
print('Preview directory:', root)
PY
# Substitute the printed directory, then open the loopback URL:
go run ./cmd/threadwalker dashboard -agents /tmp/<preview>/agents \
  -db /tmp/<preview>/preview.db -listen 127.0.0.1:18081
```

Spend is selected for today's UTC date; the fixed fixture ledger date is 2026-11-11.
Historical fixture spend will consequently show zero on other dates. The running page
shows the real render time, not a claimed live experiment measurement. Publication links
are synthetic and were not followed.

## Browser evidence

Orca's browser loaded the actual compiled HTTP server at `127.0.0.1:18081` with the
fixture above. The final captured widths were **1280, 390 and 320 CSS pixels**.
At each width, `scrollWidth == clientWidth`; the recorded visible-element and table
overflow counts were zero. Phone tables reflow into labeled rows, and timestamp values
remain readable. Both agents, safe action links, playbook IDs/status/counts and tool
metadata were inspected. No published link was followed.

The checked screenshots and per-width layout JSON are in
[`internal/dashboard/testdata/ui`](../../internal/dashboard/testdata/ui/README.md).
The captures use a viewport tall enough for the page: the browser's stitched full-page
capture duplicated a frame, so the helper recaptured each complete rendered page without
stitching. These are synthetic UI render evidence, not live Threads activity.
