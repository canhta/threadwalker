# Measured Threads accessibility mapping (#24)

Measured on 2026-11-12 from the owner-authorized **authenticated Orca Browser**
tab (`https://www.threads.com/`, tab title `(4) Home • Threads`) plus a fresh
agent-browser profile for the logged-out case. This is a **read mapping** for one
account on one host on one day. It does not qualify live posting, account
survival, captcha handling, replies or session portability.

No cookie, storage, token, profile export or raw session data was read or
written. Page content is treated as untrusted data. The fixtures below replace
all usernames, post text, post IDs, media alt text and profile data with
synthetic content; roles, nesting, control labels and URL patterns are
preserved. Canonical inputs are in
[`testdata/threads-live`](testdata/threads-live/manifest.json).

## Commands used

```sh
orca tab list --json
orca snapshot --page <threads-tab> --compact --urls --json
orca exec --page <threads-tab> --command "read" --json
orca goto --page <threads-tab> --url https://www.threads.com/search?q=...
orca scroll --page <threads-tab> --direction down --amount 1000 --json
orca keypress --page <threads-tab> --key Escape --json
```

The public logged-out check used the pinned agent-browser binary with a new
temporary profile and no owner profile or session.

## Measured structure

Authenticated pages expose:

- a navigation with `link "Profile Profile"` (URL `/@<user>`) and
  `link "Insights Insights"` (URL `/insights/`);
- a `region "Column body"` holding the page's main content.

A feed/search post block is a clickable `generic` under that region. Direct
children include:

- a profile-picture control/link;
- the author `link` (`/@<user>`);
- a timestamp/permalink `link` (`/@<user>/post/<id>`);
- a `button "More"`;
- one or more `StaticText` nodes containing the post body (paragraphs are
  separate nodes);
- metric buttons (`Like N`, `Reply N`, `Repost N`, `Share N`) and optional media
  controls.

`snapshot -i` omits the body; the full compact tree (`snapshot -c -u`) contains
it. Thread continuation posts by the same author appear as sibling post blocks
with `N / M` counters. The measured post page did not render external replies
(only the root and the author's thread parts), so replies are not mapped or
claimed.

The Insights overview exposed `Views`, `Viewers`, `Net followers` and
`Interactions`. It did not expose likes/replies/reposts, and the test account had
no posts. Own per-post metrics are therefore unavailable, not zero.

`New thread` and a post's `Reply N` open a `dialog` with a heading
(`New thread` / `Reply`), a textbox and a `Post` button. No form action URL is
exposed and no real submit was performed, so acknowledgement is unmeasured.

Logged-out pages show a login dialog and `Continue with Instagram` instead of the
authenticated navigation.

## Mapping decisions

`threads.page_profile: threads` selects the live read profile. It requires
`backend: agent_browser`, an HTTPS `threads.com`/`threads.net` origin and
`dry_run: true` until #18 qualifies writes.

| Tool | Mapping |
|---|---|
| `read_feed` | Navigate `/`; parse post blocks from `Column body`; body from direct `StaticText`, with measured `N / M` counters removed. Media-only posts with no direct text are omitted. |
| `search` | Navigate `/search?q=...`; same post extraction. |
| `read_post` | Navigate the supplied same-origin post URL; return the root post plus same-author thread continuation posts. External replies are not claimed. |
| `read_own_metrics` | Navigate `/insights/`; return `ErrMetricsUnavailable` because the measured page lacks all four required counters. |
| `read_own_metrics` with `action_id` | Same; `ErrMetricsUnavailable`. |
| `post` / `reply` | The composer dialog is recognized structurally, but submission returns `ErrLiveWriteUnqualified` before any fill or click. Non-dry-run live templates are rejected by config. |
| Logged out | Authenticated navigation is absent and a login dialog/button is present: `ErrLoggedOut`. |
| Unknown | No authenticated navigation, no known region and no recognized dialog: `ErrUnknownPage`. |
| Captcha | A heading/dialog containing `captcha`, `verify you are human` or `security check`: `ErrCaptcha`. This is a defensive heuristic, not measured against a real challenge. |

No invented page attributes are required. The parser uses only roles, nesting,
link URLs and visible text from the accessibility tree.

## Evidence

Sanitized fixtures and their manifest are in
[`testdata/threads-live`](testdata/threads-live/manifest.json); the measurement
notes are in [`measurement.txt`](testdata/threads-live/measurement.txt). The
parser also ran against the saved full measured snapshots (content not logged):
feed 4 posts, search 3 posts, post page 6 posts (root plus same-author thread
continuation). The first feed snapshot was still showing `Loading...` and
produced zero posts; the adapter now polls while a loading status is present and
no posts exist, bounded by the operation deadline.

Parser tests cover feed, search, post context, insights unavailability,
compose/reply recognition, logged-out, unknown, malformed indentation, bounds and
same-origin rejection.

The opt-in real-browser check loads public Threads with a fresh profile and
asserts that the live profile fails closed as logged out and refuses a write:

```sh
AGENT_BROWSER_BIN=/path/to/agent-browser CHROME_BIN=/path/to/chrome \
  go test -tags agent_browser_integration \
  -run TestAgentBrowserThreadsLiveLoggedOutFailsClosed -count=1 -v ./internal/threads/
# --- PASS (7.44s)
```

The existing fixture integration test remains green after adding `-u` to the
snapshot command. `go vet ./... && go test ./...` passes.

## Not measured

External replies, reply threading beyond the author's own thread parts, real
post/reply submission and acknowledgement, captcha/challenge flows, login
persistence, session portability to another host or VPS, account survival,
datacenter-IP behavior, headed operation, production memory and elapsed soak.
The Orca tab is an authorized measurement surface, not a portable browser
profile for a VPS or unattended runtime. Those remain #2/#18/#21/#9.
