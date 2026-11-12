# Local agent-browser CLI verification (#22)

Source: the `agent_browser` backend in `457e03f`, measured locally on
2026-11-12 with Go 1.27.3, macOS arm64, Google Chrome for Testing
157.0.8103.52 and the pinned **agent-browser 0.42.0** CLI. This is synthetic
local evidence: no Threads account, Telegram, paid provider, VPS or real
credentials were used. The owner selected
[`vercel-labs/agent-browser`](https://github.com/vercel-labs/agent-browser); this
issue implements that choice locally and does not qualify live Threads.

## Pinned release and installation

`npm install agent-browser@0.42.0` installs platform-native binaries in the
package (`bin/agent-browser-darwin-arm64`, `-linux-x64`, and so on). The adapter
invokes the native binary directly; the Node wrapper is not part of the
transport. `agentbrowser.Version` is `0.42.0`, and the adapter fails closed with
`ErrVersion` when `agent-browser --version` prints anything else. A future
upgrade must re-verify the flags below and change the pin in one place.

Measured:

```sh
/path/to/agent-browser-darwin-arm64 --version
# agent-browser 0.42.0
```

## Verified CLI surface

The harness builds every argv itself. There is no shell, no `eval`, no `batch`,
no `connect`/`--cdp`, no `auth`, `cookies`, `storage`, `mcp`, extension, plugin
or credential command, and no model-supplied flags.

| Purpose | Verified invocation |
|---|---|
| Version gate | `--version` |
| Global flags | `--json --session <name> --namespace <ns> --profile <dir> --executable-path <chrome> --config <harness-file> --no-auto-connect --no-webmcp --idle-timeout 30m` |
| Launch only | `open` (about:blank) |
| Navigate | `open <absolute-url>` |
| Bounded rendered text | `read` |
| Interaction refs | `snapshot -c` |
| Fill | `fill <ref> -- <text>` |
| Submit | `click <ref>` |
| Owned daemon identity | `session info` |
| Session verification | `session list` |
| Close | `close` |

Flags verified from `agent-browser --help` and by execution. Notes that changed
the implementation:

- `snapshot -i` omits noninteractive `StaticText`. On the feed fixture it
  returned only the two links and dropped the post body. The adapter therefore
  never uses `-i` for content; it reads rendered text with `read` and uses
  `snapshot -c` only for refs.
- `--max-output` did not truncate active-tab `read` or `snapshot` in this
  release (`--max-output 20` still returned the full body). The adapter enforces
  its own 1 MiB stdout bound and kills a process that exceeds it.
- `--json` must appear before a literal `--`; everything after `--` is the fill
  text. `fill @e1 -- --leading text` therefore needs global flags first.
- `session info --json` exposes the owned daemon `pid`. `session list --json`
  returns `{"sessions":[...]}`.
- The daemon socket path is limited to 103 bytes. A long session name failed at
  launch with an explicit error, so sessions and namespaces are short
  (`tw-<8-char-prefix>-<10-hex>` and `tw-<10-hex>`), derived from the agent ID.
- `--allowed-domains` rejects persistent profiles. Origin isolation is therefore
  enforced by the harness: it navigates only to the configured origin, rejects
  cross-origin final URLs, and rejects non-same-origin form actions.
- The daemon auto-starts and outlives one CLI process. A CLI killed during a
  pending page read can leave the daemon unresponsive to `close`; in that probe
  `close` and even `session info` hung for more than 20 seconds. The adapter
  stores the daemon PID from startup `session info`, tries a five-second
  graceful close, and on failure kills only that owned daemon. The namespace is
  per agent, so no other session shares it. Chrome exited with the daemon in the
  measured cleanup.

## Page document decision

Live Threads DOM is **not** parsed by this adapter. The transport accepts only a
versioned, invented rendered-text document, `agent-browser-v1`, served by the
canonical fixtures in
[`testdata/agent-browser`](testdata/agent-browser/manifest.json). The leading
block consists of marker lines:

```text
twab1 ["contract","agent-browser-v1"]
twab1 ["page","feed"]
twab1 ["post","fixture-1","synthetic_author","/post","Synthetic feed text...",false,false]
twab1 ["metric","views","123"]
twab1 ["form","/submit?kind=post","post"]
twab1 ["thread","/post"]
twab1 ["auth","logged_out"]
```

Rules enforced by the parser:

- The first nonblank line must be the contract marker; the contract value must
  be `agent-browser-v1`. Anything else is `unknown_page`.
- Every marker line is a bounded JSON array. Unknown markers, wrong arity,
  duplicate contract/page/post IDs/thread/form, non-integer or negative metrics,
  missing required fields, non-same-origin URLs and oversized text are
  `malformed_page`.
- A page without the contract fails closed as `unknown_page`. Pointing
  `base_url` at Threads today does not produce a feed; it produces a safe
  operation error.
- Text after the leading marker block (for example a rendered "Post locally"
  button label) is ignored. A post body is a JSON string inside one marker, so
  newlines and marker-shaped text remain literal data and cannot add markers.
- Bounds: 1 MiB rendered text, 50 posts, 8 KiB per post, 8 fields per marker,
  exactly four metrics, exact same-origin thread URL for `action_metrics`.

This is deliberately text-first: it demonstrates that feed reads return the
noninteractive post body, not just accessibility refs. A live Threads profile
still needs measured selectors or a measured accessibility-tree mapping, and
belongs to #2/#18. No compatibility is claimed.

## Lifecycle

`NewAgentBrowser` canonicalizes and exclusively leases the operator profile,
derives the session and daemon namespace from the agent ID, writes a harness
`--config` file (`{}`) so user/project discovery cannot add plugins or state
replay, launches one session on about:blank, and closes it on every exit. The
client kills the CLI process group on cancellation, bounds stdout/stderr and
every call with a deadline, and removes nothing from the operator profile.

`fill` passes post text as one positional argv value, so the same OS user (or
root) can see it in the process list while the call runs. It is never logged or
returned in an error. If that exposure is unacceptable, a future release needs a
stdin or file-descriptor fill path; do not add `eval` for it.

## Measured integration

`docs/research/testdata/agent-browser-runtime/integration.txt` is the actual
`-v` output. The opt-in test serves the canonical fixtures from a loopback
`httptest` server and runs the real binary with local Chrome:

```sh
AGENT_BROWSER_BIN=/path/to/agent-browser CHROME_BIN=/path/to/chrome \
  go test -tags agent_browser_integration -run TestAgentBrowserLocalIntegration \
  -count=1 -v ./internal/threads/
# --- PASS: TestAgentBrowserLocalIntegration (27.96s)
# PASS
```

| Check | Result |
|---|---|
| Six operations | `read_feed`, `search`, `read_post`, `read_own_metrics`, `post`, `reply` passed through the public `threads.Backend` |
| Bounded feed text | Returned the literal instruction-shaped post body, not only refs |
| Action metrics | Bound to the exact `/post?reply=1` URL; metrics 9/1/0/0 |
| Launch/close cycles | Three cycles; profile reopened afterwards |
| Local submissions | Six (`post`/`reply` × three cycles), all confirmed at `/submitted` |
| Logged out / captcha | `ErrLoggedOut` / `ErrCaptcha`; no submit |
| Unknown / malformed | `ErrUnknownPage` / `ErrMalformedPage`; no guessed selectors |
| Profile isolation | A second session on the active profile failed; another agent on the same profile failed; reopen after close succeeded |
| Uncertain submit | Server accepted exactly once; transport returned `ErrUnknownOutcome`; no retry; owned daemon cleanup succeeded |
| Cancellation | A 500 ms deadline during a slow page returned `context.DeadlineExceeded`; close succeeded and submitted nothing |

Offline checks: `go vet ./...` (no output) and `go test ./...` passed; the
uncached `go test -count=1 ./...` run is in `tests.txt`, with `vet.txt`. The
shared eval suite passed 30/30 offline at revision `457e03f`, seed 8
(`default-eval.json`, `eval.txt`). `go test -race ./internal/agentbrowser
./internal/threads` also passed. The pre-existing guard concurrency flake that
blocked the first full run was fixed separately in #23 (commit `833af26`).

## Not measured

Live Threads DOM/selectors, login/session persistence, account survival,
challenge avoidance, headed operation, sandbox-enabled Linux behavior,
production memory (Chrome + daemon + harness PSS/RSS), launch peaks and elapsed
soak. The earlier chromedp memory samples do not predict this stack and do not
prove a 3,659 MiB host can run live Threads reliably. Those remain #2, #18, #21
and #9. This change does not post, reply, access Threads or Telegram, or change
the VPS.
