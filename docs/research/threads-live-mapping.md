# Measured Threads accessibility mapping (#24, #25)

Measured on 2026-11-12 from the owner-authorized **authenticated Orca Browser**
tab (`https://www.threads.com/`, tab title `(4) Home • Threads`) plus a fresh
agent-browser profile for the logged-out case. #24 measured reads; #25 measured
live **posts and replies**, including success acknowledgement, on the owner's
explicitly authorized throwaway account. This is one account on one host on one
day. It does not qualify account survival, captcha handling, rate-limit
behavior or session portability.

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
# live write measurement (#25, multi-line follow-up)
orca click --page <threads-tab> --element <ref> --json
orca fill --page <threads-tab> --element <ref> --value "<neutral text>" --json
orca type --page <threads-tab> --input "<line>" --json
orca exec --page <threads-tab> --command "press Shift+Enter" --json
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
(only the root and the author's thread parts). A reply permalink page measured in
#25 did render the parent chain plus the reply; ordinary post pages still did not
render unrelated reply blocks.

The Insights overview exposed `Views`, `Viewers`, `Net followers` and
`Interactions`. It did not expose likes/replies/reposts, and the test account had
no posts. Own per-post metrics are therefore unavailable, not zero.

`New thread` and a post's `Reply N` open a `dialog` with a heading
(`New thread` / `Reply`), a textbox and a `Post` button. No form action URL is
exposed.

### Measured write path (#25)

- **Post compose.** `button "New thread"` opens the dialog. After a fill, the
  same dialog exposes `button "Post"`. Clicking `Post` closes the dialog and
  leaves an accessibility `status` toast: `status > generic > image + generic >
  link "View"` whose same-origin URL is the new `/@<user>/post/<id>`.
- **Reply compose.** A post page has an inline composer: a textbox inside the
  post block, and after typing, a `button "Reply"` in the same block. The reply
  appears as a sibling post block with its own `/post/<id>` permalink nested
  under the parent. `Reply N` also opens the dialog form with `Post`.
- **Acknowledgement.** The new own permalink is observed either in the status
  toast or in a post block that did not exist before the click. For a reply the
  permalink page shows the parent chain plus the reply.
- **Failure shape.** An empty `Post` click is a no-op (`page unchanged`). No
  in-page error toast was observed. Missing acknowledgement within the deadline
  is `ErrUnknownOutcome` and the click is never repeated.
- **Multi-line.** `fill` collapses `\n` into one line. Typing line by line with
  real keystrokes and `press Shift+Enter` between lines preserves breaks in both
  the `New thread` dialog and the inline reply composer. Plain Enter in the
  inline reply composer submits instead of inserting a break. Measured posts
  with 3 lines (emoji, Vietnamese diacritics) and a multi-line reply re-read with
  their `\n` breaks intact; a deliberately blank line collapsed on re-read. Lines
  beginning with `--` are refused before the click because the pinned CLI would
  parse them as options instead of text.
- A URL in post text renders as a separate `link` plus a link card; the read
  mapping keeps the surrounding `StaticText`, not the embedded URL.

Logged-out pages show a login dialog and `Continue with Instagram` instead of the
authenticated navigation.

## Mapping decisions

`threads.page_profile: threads` selects the live profile. It requires
`backend: agent_browser` and an HTTPS `threads.com`/`threads.net` origin. It now
supports measured reads and writes; `dry_run` remains the template-level switch
the guard uses to decide whether a reserved action may click.

| Tool | Mapping |
|---|---|
| `read_feed` | Navigate `/`; parse post blocks from `Column body`; body from direct `StaticText`, with measured `N / M` counters removed. Media-only posts with no direct text are omitted. |
| `search` | Navigate `/search?q=...`; same post extraction. |
| `read_post` | Navigate the supplied same-origin post URL; return the root post plus same-author thread continuation posts. A reply permalink returns the measured parent chain plus the reply. |
| `read_own_metrics` | Navigate `/insights/`; return `ErrMetricsUnavailable` because the measured page lacks all four required counters. |
| `read_own_metrics` with `action_id` | Same; `ErrMetricsUnavailable`. |
| `post` | Navigate `/`; open `New thread`; fill the dialog textbox (single line) or focus it and type line by line with `Shift+Enter` (multi-line); click the one `Post` control; poll for the status toast `View` URL or a new own post block. Return the acknowledged `/post/<id>` URL. |
| `reply` | Navigate the target post; fill the inline composer textbox (single line) or type line by line with `Shift+Enter` (multi-line); click its one `Reply` control (or the dialog `Post`); poll for the toast/new own reply block. Return the acknowledged `/post/<id>` URL. |
| Missing acknowledgement | Deadline after the single click is `ErrUnknownOutcome`; the harness retains the intent and stops the wake. No retry. |
| Compose not found | No dialog and no unambiguous `New thread` button, or no single textbox/submit in the composer: `ErrUnknownPage` / `ErrMalformedPage` before any fill/click. |
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
same-origin rejection. Write tests cover post success via the toast, reply
success via the toast, missing acknowledgement after one click, compose not
found, logged-out and blank text failing before any fill/click, and the composer
control that skips the post's metric `Reply` button.

Live write evidence (#25): 5 posts and 3 replies were published on the
owner-authorized throwaway account; every text-only permalink was re-read and
matched. Post click commands took 0.48-1.48s and the `View` acknowledgement was
observed about 3s after the click. Reply clicks took 1.07-1.59s; the reply
permalink or `View` toast appeared about 4-5s after the click. The 470-character
post, emoji/Vietnamese text, duplicate text and a URL post all acknowledged.
Empty text was a no-op. No captcha or rate-limit banner appeared. Two further
multi-line posts and one multi-line reply were published with line-by-line
keystrokes plus `Shift+Enter`; their permalinks re-read with the `\n` breaks
intact. A deliberately blank line collapsed in the rendered post. Details are in
[`measurement.txt`](testdata/threads-live/measurement.txt).

The opt-in real-browser check loads public Threads with a fresh profile and
asserts that the live profile fails closed as logged out and refuses a write:

```sh
AGENT_BROWSER_BIN=/path/to/agent-browser CHROME_BIN=/path/to/chrome \
  go test -tags agent_browser_integration \
  -run TestAgentBrowserThreadsLiveLoggedOutFailsClosed -count=1 -v ./internal/threads/
# --- PASS (7.44s)
```

The existing fixture integration test remains green after adding `-u` to the
snapshot command. `go vet ./...` (no output) and `go test -count=1 ./...` pass;
outputs are in
[`testdata/threads-live-runtime`](testdata/threads-live-runtime/tests.txt). The
shared eval suite passed 30/30 offline at revision `8c92054`, seed 8
([report](testdata/threads-live-runtime/default-eval.json)); `go test -race
./internal/threads ./internal/agentbrowser` passed.

## Not measured

Captcha/challenge flows, explicit in-page error/rate-limit banners, login
persistence, session portability to another host or VPS, account survival,
datacenter-IP behavior, headed operation, production memory and elapsed soak. Embedded URLs are present in post text but
the read mapping omits the link node's text. Duplicate text was accepted once;
no long-run duplicate policy is claimed. The Orca tab is an authorized
measurement surface, not a portable browser profile for a VPS or unattended
runtime. Those remain #2/#18/#21/#9.
