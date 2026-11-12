# Local Chromium transport spike (#20)

Measured 2026-11-11 on Linux 7.0.0-1014-aws, amd64, Ubuntu 24.04.5, Go
1.27.3. This is **synthetic local transport evidence only**. No Threads account,
Telegram, paid provider or remote deployment was used. The local machine's kernel
name does not establish a VPS or datacenter-IP test.

## Choice for #8

Select **`github.com/chromedp/chromedp v0.23.0`** with locally installed Chromium.
It completed the six fixture operations, bounded cancellation and three clean
launch/close cycles. One permitted candidate was sufficient for this transport
check; **go-rod was not run**, so this is not a performance comparison or evidence
that chromedp is better than go-rod. #8 owns production integration, guards,
audit-before-submit, pause/notification behavior and durable browser tests.

The candidate's [v0.23.0 API documentation](https://github.com/chromedp/chromedp/blob/v0.23.0/docs/API.md)
uses generic `Action[T]`, `Run` for a value, `Do` for ordered void actions, and
`chromedp.CSS` selectors. Old pointer-result/`ByQuery` examples do not compile
against this version; the temporary runner was adapted before measurements.

The temporary module resolved only this candidate and its required modules:

| Module | Version |
|---|---|
| `github.com/chromedp/chromedp` | `v0.23.0` |
| `github.com/chromedp/cdproto` | `v0.159.0` |
| `github.com/go-json-experiment/json` | `v0.0.0-20260213210345-44df1a37e875` |

No browser dependency was added to the repository's `go.mod`/`go.sum`. The
candidate runner and its tests remain in a throwaway module at
`/tmp/threadwalker-browser-spike`, as the issue requires; they are not production
tool calls and do not expose an alternative agent dispatcher.

## Browser and commands

Browser: **Google Chrome for Testing 157.0.8103.50**, linux64, from the Stable
entry returned by the [official Chrome for Testing manifest](https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json).
The manifest can change; the measured archive is pinned here:

```sh
curl -fsSL --max-time 180 \
  https://storage.googleapis.com/chrome-for-testing-public/157.0.8103.50/linux64/chrome-linux64.zip \
  -o /tmp/threadwalker-browser-spike/chrome.zip
unzip -q /tmp/threadwalker-browser-spike/chrome.zip -d /tmp/threadwalker-browser-spike
/tmp/threadwalker-browser-spike/chrome-linux64/chrome --version
# Google Chrome for Testing 157.0.8103.50

# Candidate module setup (outside the repository):
cd /tmp/threadwalker-browser-spike
/tmp/threadwalker-go/go/bin/go mod init threadwalker.local/browser-spike
/tmp/threadwalker-go/go/bin/go get github.com/chromedp/chromedp@v0.23.0

# Commands used after creating the temporary runner and TestLocalTransport:
FIXTURE_DIR=/home/ubuntu/orca/workspaces/threadwalker/local-browser-spike/docs/research/testdata/browser \
CHROME_BIN=/tmp/threadwalker-browser-spike/chrome-linux64/chrome \
/tmp/threadwalker-go/go/bin/go test -race -run TestLocalTransport -v
# PASS; 3 cycles / 33 checks; package wall-clock 5.306s
FIXTURE_DIR=/home/ubuntu/orca/workspaces/threadwalker/local-browser-spike/docs/research/testdata/browser \
CHROME_BIN=/tmp/threadwalker-browser-spike/chrome-linux64/chrome \
/tmp/threadwalker-go/go/bin/go test -count=1 -run TestLocalTransport -v
# PASS; 3 cycles / 33 checks; package wall-clock 3.385s
```

These last commands need the temporary runner while it exists; they are a record
of the experiment, not a new repository command or CI requirement. The durable
inputs and measured outputs are saved below so #8 can implement its own tests.
Installing Chromium and fetching the candidate required downloads; the measured
operations themselves used only a loopback `httptest` server, no credentials.

Each cycle allocated a new temporary profile with the headless defaults from
[`DefaultExecAllocatorOptions`](https://github.com/chromedp/chromedp/blob/v0.23.0/allocate.go).
Additional flags: `--no-sandbox`, `--disable-dev-shm-usage`,
`--disable-crash-reporter`, `--disable-breakpad`,
`--disable-background-networking`,
`--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1`,
`--proxy-server=http://127.0.0.1:9`, and
`--proxy-bypass-list=127.0.0.1;localhost`. Non-loopback browser traffic was sent to
an unused loopback proxy; fixture CSP limited content/forms/connections to self.
This was not a packet-level egress audit. Sandbox-enabled deployment was not
qualified by this run; `--no-sandbox` is a recorded test condition, not a
production recommendation.

The whole temporary test had a 45-second context; ordinary browser operations
had five-second contexts. Deadline checks used 150 ms for a missing element and
300 ms for a missing submit acknowledgement. Close used `chromedp.Cancel`, then
both browser and allocator cancellation, waited for allocator cleanup, inspected
observed descendant PIDs, and removed the temporary profile. A final `/proc`
command-line scan found no process using the downloaded browser path.

## Saved synthetic contract and outcomes

[`testdata/browser/manifest.json`](testdata/browser/manifest.json) records the
invented `synthetic-v1` DOM contract, fixture hashes and server expectations.
[`testdata/browser/measurements.json`](testdata/browser/measurements.json) contains
all 33 operation outcomes, timings, memory samples and submission counts from the
normal run. These files contain only invented data. They are inputs/evidence for
#8, not an extension claiming current shared eval browser coverage.

| Probe | Fixture / action | Observed outcome, all 3 cycles |
|---|---|---|
| `read_feed` | `feed.html`; `[data-tw-text]` | Exact text, including the instruction-shaped string, returned literally |
| `search` | `search.html`, navigated as `/search?q=synthetic` | Exact synthetic result text |
| `read_post` | `post.html` | Exact synthetic post body |
| `read_own_metrics` | `metrics.html`; `[data-tw-metric]` | Views 123, likes 7, replies 3, reposts 1 |
| `post` | `compose.html`, fill textarea and click | One local POST, acknowledgement and `/post` result |
| `reply` | `post.html`, fill textarea and click | One local POST, acknowledgement and `/post` result |
| Logged out | `logged-out.html` | `logged_out` marker recognised |
| Captcha | `captcha.html` | `captcha` marker recognised; no challenge attempted |
| Unknown page | `unknown.html` | Classified `unknown_page`; no guessed selectors |
| Missing element | `feed.html`, wait for absent marker | `context.DeadlineExceeded`, no retry |
| Uncertain submit | `uncertain.html`, local server accepts but never acknowledges | Exactly one acceptance, deadline, no retry |

The server accepted exactly **3 post + 3 reply + 3 uncertain** submissions across
three cycles. It bounded form bodies to 4096 bytes and accepted only the known
synthetic text; no remote destination was involved. For the uncertain case it
incremented acceptance before waiting for request cancellation without a response;
the browser waited for the success marker and timed out. This demonstrates the
transport ambiguity, **not** durable `actions` accounting or crash recovery.

Fixture convention: recognised pages have `data-tw-contract="synthetic-v1"` and
`data-tw-page`; posts have `data-tw-post`, `data-tw-author`, `data-tw-text` and
`data-tw-permalink`; metrics use `data-tw-metric`; forms use `data-tw-compose` and
`data-tw-submit`; acknowledgements use `data-tw-thread-url`. The probe read marker
values, not a complete production parser. #8 must validate the contract, reject
malformed/ambiguous pages, wrap read content as untrusted, pause/notify on unsafe
states, and exercise all six operations through `internal/guard`. Search tested
query navigation and result extraction, not live ranking or dynamic pagination.

## Memory and lifecycle measurements

Memory below is from the **non-race** run, after the race run had warmed the
browser/filesystem. Baseline harness RSS/PSS before starting a browser was
9,424/7,817 KiB. The harness is the Go test process **including** the local server,
test runtime and sampler; it is not the final ThreadWalker binary.

A goroutine sampled `/proc/<pid>/smaps_rollup` on a 25 ms ticker, beginning **after
launch** and ending before close. It discovered the browser's descendants from
`/proc/*/stat`, summed RSS and PSS across live descendants including the browser,
and tracked their PIDs for cleanup. Values are independent sampled maxima;
reading proc files takes time, snapshots are not atomic, and peaks during launch,
close, between samples or in unobserved/reparented children can be missed.

| Cycle | Launch / close ms | Samples / peak live processes | Browser peak RSS KiB | Browser peak PSS KiB | Harness peak RSS / PSS KiB | Remaining observed live / zombies |
|---|---|---|---|---|---|---|
| 1 | 166 / 67 | 34 / 11 | 1,368,796 | 433,907 | 18,228 / 16,618 | 0 / 0 |
| 2 | 160 / 66 | 37 / 11 | 1,366,008 | 434,958 | 17,900 / 16,292 | 0 / 0 |
| 3 | 154 / 62 | 37 / 11 | 1,369,132 | 436,724 | 18,156 / 16,549 | 0 / 0 |

RSS sums count shared pages repeatedly; PSS apportions them. Browser peak PSS was
approximately **424–427 MiB**, with harness peak RSS under **18 MiB**. Independent
maxima must not be treated as a simultaneous combined peak. These short, small
pages do not establish a production memory budget, two-agent fit, leak rate or
elapsed soak stability. No heap-only figure is substituted for process memory.

## Repository checks and pending qualification

On the spike checkout, the following completed successfully without a real
service or account:

```sh
/tmp/threadwalker-go/go/bin/go vet ./... && /tmp/threadwalker-go/go/bin/go test ./...
/tmp/threadwalker-go/go/bin/go test -race ./...
/tmp/threadwalker-go/go/bin/go run ./cmd/threadwalker eval \
  -suite internal/eval/testdata/default.json -revision 81be092 \
  -out /tmp/threadwalker-browser-spike/eval.json
# Existing shared suite: 30/30 passed, offline; no browser eval claim.
```

**Still unverified:** current Threads selectors/DOM, real session persistence,
login/logout/captcha variants, real posting, account survival, anti-automation
behavior, datacenter/VPS IP challenges, sandbox-enabled host behavior, production
memory and elapsed soak. Real account/IP observation remains in #2, operational
checks in #18, two-agent observation in #21 and the live experiment in #9. None is
established by synthetic markup or simulated time. #8 is now free to implement
against this local contract while preserving those qualification limits.

## Agent-friendly browser interfaces (documentation review, 2026-11-12)

**Recommendation for a new spike: `vercel-labs/agent-browser`, using Chrome.** It
provides the agent-facing layer the question asks for: compact accessibility
snapshots with element references, followed by commands such as `click @e1` and
`fill @e2 ...`. Headless describes how Chrome displays its window; it does not
describe the interface an agent gets. These tools improve that interface while
still using a browser underneath. This is a proposal, **not a replacement of the
chromedp decision or evidence of Threads compatibility**.

This review used Context7 documentation retrieved by the coordinating researcher
and the current primary sources linked below. Nothing was installed or executed,
and no SSH connection, account session or paid API was used. The supplied target
is SSH alias `vps`, Ubuntu 24.04 x64, 3,659 MiB RAM; these are input facts, not
host measurements from this review. Documentation can change; pin the selected
release before any implementation and verify its own help/source.

| Candidate | Documented agent interface and session support | Runtime / deployment implications | Fit for ThreadWalker (assessment) |
|---|---|---|---|
| **agent-browser** | Accessibility refs, compact snapshots, JSON output; named live sessions, persistent profile paths, headless and headed operation, CDP attachment. | Current docs describe native Rust CLI **and daemon**, direct CDP, Linux x64 binaries, Chrome for Testing and Linux browser libraries (`install --with-deps`). npm is an installation route; the documented current architecture has no Node daemon. | Best first interface spike: a Go adapter can invoke a fixed executable with validated arguments, without a second LLM loop. Its daemon lifecycle still needs explicit cleanup per wake. [Introduction](https://agent-browser.dev/), [installation](https://agent-browser.dev/installation), [sessions](https://agent-browser.dev/sessions), [commands](https://agent-browser.dev/commands). |
| **Playwright CLI** | Snapshot refs, commands, named sessions; headless default, `--headed`, `--persistent` or `--profile` for disk persistence. | Node.js 20+, Playwright/browser installation and browser system libraries. | Strong comparison candidate with similar Go subprocess integration. Microsoft's CLI recommendation targets coding agents; it does not establish better fit for this production loop. [Official README](https://github.com/microsoft/playwright-cli#readme). |
| **Playwright MCP** | Structured accessibility tools, no vision requirement; persistent or isolated profiles; headed default, `--headless` for a server. | Node.js 18+, browser dependencies and an MCP client/transport. | Can sit behind a Go adapter, but adds protocol integration. Directly exposing generic click/evaluate tools to the LLM would bypass the meaning of ThreadWalker's post/reply guards. Upstream explicitly says MCP is not a security boundary. [Official README](https://github.com/microsoft/playwright-mcp#readme). |
| **browser-use** | Python agent library **and** independently usable CLI/browser controls. Current 1.0 CLI executes supplied Python through Browser Harness/CDP; an inner `Agent` loop is optional. Local library browser supports persistent `user_data_dir`, headed/headless and CDP. | Python 3.11+; Chrome and Linux browser libraries for local mode. Current library `Browser()` defaults to cloud; local must be selected explicitly. Current CLI defaults to attaching existing Chrome. | Feasible as a lower-level adapter; more Python execution surface to constrain. Do not nest its autonomous `Agent` under the Go loop or silently select cloud defaults. [Quickstart](https://docs.browser-use.com/open-source/quickstart), [CLI](https://docs.browser-use.com/open-source/browser-use-cli), [browser parameters](https://docs.browser-use.com/open-source/all-parameters). |
| **Stagehand** | Natural-language `act`, structured `extract`, action discovery `observe`, plus deterministic page APIs. Current v4 docs include Go, Python and TypeScript. Local browser supports profile persistence and headed/headless. | Current Go SDK requires Go 1.26+; local Chrome; AI primitives add model requests. Browserbase cloud is an alternative, not required for local browsing. | Worth reconsidering if semantic extraction is the bottleneck. Model calls would need integration with the existing ledger, budget, redaction and guard boundaries. Not the first spike for a compact browser command interface. [Introduction](https://docs.stagehand.dev/v4/first-steps/introduction), [installation](https://docs.stagehand.dev/v4/first-steps/installation), [browser configuration](https://docs.stagehand.dev/v4/configuration/browser). |

### Integration conditions and what the spike must prove

The recommended boundary remains `LLM -> internal/guard -> existing Threads
tool -> adapter -> agent-browser -> Chrome`. Keep the six domain tools from the
spec. Do not give the model a shell, arbitrary JavaScript, unrestricted CLI
arguments, CDP connection or direct generic browser tools. A compact snapshot
helps only if a bounded, untrusted representation is actually needed in model
context; changing the backend alone does not make the existing fixed parser
understand live Threads. A richer model-controlled navigation interface would
require an explicit design/spec decision first.

In particular, `snapshot -i` selects interactive elements: it can omit post body
text. Feed reading needs a bounded full/compact or appropriately scoped snapshot,
not an assumption that interactive-only output contains the feed. See the
[snapshot reference](https://agent-browser.dev/snapshots). The current backend
also disables page scripts and accepts the invented `synthetic-v1` markers;
profile configuration alone cannot turn it into a live Threads implementation
([browser backend](../../internal/threads/browser.go)).

The harness must own executable/config/profile/session selection, origin limits,
timeouts, process shutdown and output limits. Profiles remain separate per agent;
browser state, cookies and storage must never enter tool results, logs or model
context. Do not add plaintext auth export files or password vault files as part of
this proposal. Page text and refs remain untrusted data, never instructions.

Before a submit, preserve the existing durable guarded intent and accounting.
After an ambiguous acknowledgement, crash or timeout, record unknown/assumed sent
and stop: **never retry the post/reply**, including through a batch, automatic
recovery or a new browser session. A library's actionability wait is not evidence
of durable publication semantics.

A scoped offline spike should compare agent-browser with the existing backend on
the same fixtures: six operations, malformed/unknown pages, logged-out/captcha
pause, cancellation, profile isolation, three launch/close cycles, and uncertain
submit accepted exactly once. Confirm the actual release's sandbox/launch flags,
daemon cleanup on cancellation and bounded output before considering integration.
Measure browser plus daemon plus Go process PSS/RSS, launch peaks and elapsed
soak on the intended host. The earlier chromedp samples do not predict this
stack's memory or prove a 3,659 MiB host can run live Threads reliably.

For the supplied VPS, headless Chrome is a sensible initial mode; headed use
requires a display arrangement and is mainly useful for operator diagnosis.
Neither headed mode, a compact agent interface, nor vendor stealth claims prove
Threads login persistence, account survival, challenge avoidance or selectors.
Those remain separately scoped live qualification. This review was the input to
[#22](https://github.com/canhta/threadwalker/issues/22), which implements the
local adapter, the `agent_browser` backend and the matching spec decision. The
pinned version, verified flags and measured local integration are in
[agent-browser.md](agent-browser.md); live Threads compatibility, host memory and
elapsed soak remain unverified.
