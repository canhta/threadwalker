# ThreadWalker spec (v0)

This is the only spec. Work is split into GitHub issues; each issue cites the sections it implements.

Evidence lives in `docs/research/`. Statements here are tagged where it matters:
**(decision)** made by the owner, **(sourced)** backed by the research, **(proposed)** a design
choice not yet tested.

## 1. What the experiment asks

Given only a goal, one Threads account and a budget, how far can an agent run, learn and improve
by itself with nobody approving its output? This is an experiment in AI capability, not a product.
**(decision)**

Weekly measures on the agent's own account: followers, views, replies received, token cost.
**(proposed)**

## 2. Settled

| Topic | Choice | Kind |
|---|---|---|
| Autonomy | Fully automatic; nobody approves posts | decision |
| Owner contact | Telegram, only when needed | decision |
| Template | Declares goal, persona and limits; the agent plans its own tactics | decision |
| Self-editing | The agent edits its playbook and notes in the DB, never code or its template | decision |
| Account | A disposable side account whose bio states it is an AI experiment | decision (AI label: author's condition, owner has not objected) |
| Threads access | Browser automation; the official API needs Business Verification the owner does not have | decision |
| LLM | Configurable named providers and model profiles; provider IDs, model IDs and key environment-variable names are chosen by the operator | decision |
| Language | Go | proposed |
| Host | Local development and verification; a VPS is an optional deployment target | decision |
| Several agents | Yes, after one agent runs stably | proposed |

## 3. Shape

Two layers:

- **Harness (Go code):** scheduling, tools, guards, ledger, audit log, kill switch, gate. The agent
  cannot change it.
- **Agent (LLM):** on each wake it receives its goal, playbook, metrics and remaining budget, and
  decides what to do by calling tools.

Each agent uses one hand-written tool-calling loop with the LLM configuration resolved from
its template. The optional catalog supports multiple providers and model profiles, including
several profiles sharing one provider; selecting a profile resolves only that provider's key.
The runtime has internal adapters for OpenAI-compatible Chat Completions (`openai`) and native
Anthropic Messages (`anthropic`). Both normalize messages, tool calls/results and token usage into
the same bounded loop and ledger. Protocols name implemented wire contracts: a vendor's arbitrary
API does not become compatible merely by configuring its key. Native Messages pins API version
`2023-06-01`, uses `max_tokens` (1024 by default, or the caller's output limit), and supports text
and client-side function tools. Unknown content blocks fail closed after recording known usage;
extended thinking, multimodal input, streaming and server-side tools are outside this contract.
**(decision)**

Operational diagnostics use one injectable standard-library `log/slog` logger, JSON to stdout
by default (compatible with systemd/journald), with selectable `debug/info/warn/error` level and
`json/text` format. The CLI `check` and `run` expose `-log-level` and `-log-format`; library callers inject
`llm.Options.Logger`, with nil disabling client output. No hosted collector or raw-payload debug
mode is used. The canonical event names and metadata allowlist are in
[`internal/logging/logging.go`](../internal/logging/logging.go); emitters pass a typed record,
never arbitrary errors or payload fields. The SQLite audit DB remains the action record.
**(decision)**

No fixed triage/compose/critic pipeline and no same-model critic. **(sourced)**: first-party guidance says
to maximise a single agent first, and in Anthropic's Project Vend a same-model supervisor shared
the blind spots of the agent it supervised.

Reproducible evaluations are an operator-run harness capability at the existing client/caller
boundary, using versioned synthetic fixtures, scripted providers and injected time/randomness.
Offline is the default; arbitrary configured profiles can be compared without modifying an
agent's immutable template or prompt. Reports contain actual outcomes and safe reason codes,
tool names/steps, normalized usage and estimated cost, plus separately labelled wall-clock
timing. Suite/input identity, seed, repeats, code revision and secret-free input and configuration
fingerprints make runs reproducible. Correctness, safety and explicit deterministic quality
checks gate comparisons; token/cost/timing differences are reported separately. Human style
judgments and real engagement outcomes require separate review and measurement. Optional live
provider calls require explicit profiles/cases, environment credentials and positive USD,
step and output bounds; they never access Threads, Telegram or the VPS. Canonical fixtures and
report/runner code live in [`internal/eval`](../internal/eval); reproduce, comparison and failure
inspection commands live in [`docs/evals.md`](evals.md). Issues #5/#6/#7/#8/#9/#10/#12 must extend
the suite as their components land; these evaluations certify only implemented client,
protocol, configuration, accounting and local runtime behavior. **(decision)**

## 4. One wake

1. The harness wakes the agent at the time the agent chose last wake, within a frequency limit.
2. The harness assembles context: template (immutable), playbook, fresh metrics computed by code,
   remaining budget, and the note the agent left for itself.
3. The agent calls tools until it ends the wake or hits the step or token limit.
4. Every tool call passes through the guards, is written to the audit log, then executes.
5. The agent writes a note for next time and schedules its next wake.

The local runtime serializes wakes across configured agents. `schedule_next_wake` clamps to
`now + min_minutes_between_wakes` through `now + 24h`; the configured minimum is 1..1440 minutes.
A wake reserves a fallback schedule of `now + 60min` (or the configured minimum if longer) before
any tool runs. Startup closes open wakes as `interrupted`, retires unfinished tool audit outcomes
as unknown/assumed sent with retry disabled, and advances the affected agent's schedule to at
least that fallback interval after restart. It never reloads the interrupted conversation or
executes its pending tool/action intent. Paused and killed agents stay stopped. **(decision)**

`run -once -offline` uses an isolated temporary DB, a built-in synthetic template with dummy
credentials, an in-process scripted LLM transport and synthetic trend/Threads fixtures. It reads
no environment credentials and makes no network request. `-soak-hours` uses injected logical time
and an actual local SQLite close/reopen to simulate a restart; its exported evidence is synthetic,
not elapsed live qualification. The canonical implementation and fixtures are in
[`internal/agent`](../internal/agent) and [`internal/eval/testdata/runtime-v1.json`](../internal/eval/testdata/runtime-v1.json).
Normal `run` loads configured LLM settings, with custom endpoints supported by that configuration.
The default Threads stub requires `dry_run`; an explicitly configured browser backend permits
guarded submissions. `run -offline` always forces the in-process Threads stub and rejects
non-dry-run templates. Playbook and metrics context are empty until their implementing issues land.

Two rules keep a wake from doing damage **(sourced: patterns in `docs/research/reference-repos.md`)**:

- **Stall stop.** A wake ends when the agent issues the same tool call with the same arguments
  three times; the end reason is recorded.
- **No replay.** An interrupted wake is never re-run. A `post` or `reply` whose outcome is unknown
  is recorded as sent and never retried, so a crash cannot double-post.

Reflection is something the agent chooses to do when new metrics exist; there is no fixed nightly
job. **(proposed)**

The existing client emits run start/finish/stop, request start/finish/retry, usage-accounting and
client dispatcher-result events. Correlation uses available operator-configured agent/provider/
profile/model/protocol IDs, wake ID, a local request sequence, attempt and a locally derived tool
call ID (wake/step), never provider-supplied IDs. Durations use the injected client clock. Events
carry operation, outcome/reason, HTTP status when known, steps, tokens and locally computed USD;
unknown or malformed tool names are omitted. `check` emits validation start/configuration/result.
The runtime adds wake, scheduler and context events; guarded tool events cover the running-state
and audit boundary. Telegram emits owner operation outcomes; browser events cover launch/close
and each configured-backend operation with safe outcome and pause reasons.

Event ownership is an acceptance requirement in the implementing issues: #5 implements wake
lifecycle (`wake.started/finished/stopped`) and tool lifecycle (`tool.started/finished`) plus
scheduler/context/note/trend outcomes and initial `guard.checked`, `tool.blocked` and `audit.recorded` outcomes; #6 extends full rule-policy coverage; #7 adds
`telegram.operation.finished` and kill-switch outcomes; #8 adds `browser.operation.finished`
and real Threads-tool outcomes; #9 validates those events in the integrated live run. Each adds
captured-log and offline eval cases through the same logger/record contract before completion.
No future component is considered instrumented until its issue implements and tests it.
**(decision)**

## 5. Tools

| Group | Tools | Notes |
|---|---|---|
| Threads (browser) | `read_feed`, `search`, `read_post`, `post`, `reply`, `read_own_metrics` | One browser profile per agent |
| Outside sources | `fetch_trends` (Hacker News API, Product Hunt RSS, Google Trends VN RSS) | Bounded results round-robin across sources; any source failure fails the call. Network behavior is tested offline with fixtures, not claimed as current live availability |
| Notes | `note_write`, `note_search` | SQLite FTS5; vector search is not justified at this scale **(sourced)** |
| Playbook | `playbook_read`, `playbook_propose` | See section 7 |
| Owner | `notify_owner`, `ask_owner` | Telegram; `ask_owner` never blocks the wake |
| Contacts | `mark_do_not_contact` | The agent can add to the list, never remove |
| Scheduling | `schedule_next_wake` | Clamped to the allowed range |

The browser backend implements the measured `synthetic-v1` contract in
[`docs/research/testdata/browser/manifest.json`](research/testdata/browser/manifest.json), using
`chromedp v0.23.0`. It starts at most one browser per wake, closes it on every exit (including
cancellation), and retains an agent-owned operator profile. Only configured-origin navigation
and form submissions are allowed; page scripts are disabled for this synthetic contract.
Unknown or malformed pages return operation-specific safe errors without guessed selectors.
Current Threads DOM, login/session behavior and real-account operation remain unverified in
#2/#18; these fixtures do not claim live compatibility. **(decision)**

`dry_run` browser reads execute normally; writes retain a single guarded intent without
submitting. A configured non-dry-run backend submits only after that intent commits and stores
`thread_url` only after a recognized acknowledgement. Any uncertain submission ends the wake,
retains the action/counters and disables further tool effects; restart never replays it.
The replied-to thread quota uses `target_url`, independently of the confirmed new reply URL.
Logout/captcha pauses only the selected running agent (killed stays killed), ends its wake,
and queues a static owner notice; a disabled owner integration retains the notice locally.
Addressed inbound replies enter `ObserveStopRequest` transiently, without storing their text.
External tool strings are redacted against configured secrets before model delivery. **(decision)**

## 6. Guards (in code; the agent cannot disable them)

- Daily limits on posts and replies; token limits per wake and per day.
- Teasing rules: the persona may mock a take, never a person's traits; at most N replies to one
  username in 7 days.
- Do-not-contact list, filled three ways: a string match on stop requests in replies addressed to
  the agent, the agent calling `mark_do_not_contact` when someone is annoyed, and the owner's
  `/dnc <username>` command. Entries are permanent for the agent.
- The tools in section 5 are the agent's whole action surface: it cannot like, follow, message,
  delete, or edit the profile.
- Forbidden-phrase check by plain string match before posting.
- A reply contains no URL and mentions no username other than the author it answers.
- At most one reply per thread per day.
- No profiles of strangers: store only username and counters needed for rate limiting.
  **(sourced)**: Meta Platform Terms 3.a.v, and stored third-party content is a known
  memory-injection surface.
- Everything read from Threads is marked as untrusted data. Secrets never enter the LLM context.
- Kill switch: Telegram `/pause`, `/resume`, `/kill`, `/dnc`; automatic stop on reaching a daily limit or
  when the browser is logged out or shown a captcha.

Operational logging is allowlisted metadata only: no credentials, authorization headers,
endpoint URLs, request/response bodies, system prompts, private notes, Threads text or strangers'
usernames. Tool arguments/results and raw provider errors never enter logs; returned provider
errors use static diagnostic reasons while preserving cancellation/error identity. Log-level
selection never enables payload logging. Configured IDs and known tool-schema names are trusted
operator metadata; third-party tool IDs/names are not. **(decision)**

Guard policy uses UTC calendar days for post/reply and per-thread quotas. Contacts use the exact
rolling interval `(now - 7 days, now]`, with nanosecond precision and exact expiry at seven days.
The 500-character ceiling counts Unicode code points. Forbidden phrases and stop phrases use
case-insensitive substring matching. Usernames normalize by trimming whitespace, removing one
leading `@` and folding case; only username identifiers and rate counters/permanent fixed-code
contact flags persist. Reply URLs include schemes, `www` and bare domains; an allowed author's
`@mention` may contain dots. Thread identity removes query, fragment and trailing slash from
the target URL. **(decision, #6 boundary clarification)**

`internal/guard` commits attempted-call audit metadata before effects and atomically reserves an
action intent and contact counters under SQLite `BEGIN IMMEDIATE`. All dry-run/real intents,
including failed/unknown outcomes, count; the action builtin reports the reserved ID and never
inserts a duplicate. Persistence errors fail closed. A daily action quota refusal stops the wake with
`action_limit`; the agent remains eligible for a later wake and quotas reset at the next UTC day.
Restart never replays these intents.
The local runtime serializes wakes; the SQLite reservation also protects concurrent guards and
independent database handles. Contact counters refresh from timestamped intents during guarded
action checks; permanent do-not-contact flags survive refresh/restart. **(decision)**

`Guard.ObserveStopRequest` is harness-only ingestion of a username, transient reply text and an
explicit addressed-to-agent flag; it is never an LLM tool. Only addressed replies matching a
configured phrase add a permanent contact entry, without saving inbound text. Optional
`guard.stop_phrases` replaces the Vietnamese/English defaults in
[`internal/guard/guard.go`](../internal/guard/guard.go); configured lists must be nonempty.
The add-only `mark_do_not_contact` tool accepts a `username`, records a fixed reason code and
passes the same running-state/audit boundary as every other builtin. **(decision)**

Versioned synthetic guard cases and captured metadata-only event evidence live in
[`internal/eval/testdata/guard-v1.json`](../internal/eval/testdata/guard-v1.json) and
[`guard-v1-events.jsonl`](../internal/eval/testdata/guard-v1-events.jsonl), using the shared eval
report/logging contracts. Reproduction commands are in [`docs/evals.md`](evals.md).

The numeric limits are starting values in the template and have no experimental basis yet.

## 7. Learning

The constraint: tens of actions a day, with delayed, noisy feedback. The research estimates about
49 days to separate a 20% strategy from a 30% one across five arms, so learning from single posts
is learning from noise. **(sourced; the figure is the research's own calculation)**

Design **(proposed; no study has tested this combination)**:

- The playbook is a list of short hypotheses written by the agent, each `testing`, `established`
  or `rejected`.
- The agent tags each action with the hypotheses it is testing. **Code** counts outcomes; the
  agent never counts its own results.
- The gate, in code, moves a hypothesis to `established` or `rejected` once it has a minimum
  sample and a difference from baseline above a threshold.
- The agent may add or amend single entries. It may not rewrite the playbook wholesale.
  **(sourced)**: wholesale rewriting has collapsed an agent's context from 18,282 to 122 tokens.
- The template and system prompt are immutable to the agent.

Accepted consequence: the gate opens slowly, possibly weeks before the first entry is established.
How fast it opens is one of the experiment's results.

## 8. Budget

- A local token ledger per agent, priced from the template's resolved LLM configuration (inline
  rates or the selected model profile). Both adapters record provider-reported input and output
  tokens; native input includes cache-read and cache-creation categories when reported. The two
  configured flat rates do not represent cache discounts, cache-write surcharges, tier pricing
  or billed dollars. **(sourced)**: flat
  subscriptions expose no balance to read.
- The agent always sees its remaining budget and decides how to spend it; the harness only blocks
  at the limit.
- Feeding real revenue back into the budget is out of scope for v0.

Usage-accounting events at the client/ledger boundary distinguish a committed local ledger
write from a failed write, with known token counts and computed USD retained for diagnosis.
Every HTTP attempt has a correlated result, including budget rejection, cancellation, malformed
responses and accounting failures; retryable responses retain any valid reported usage before
retrying. Budget and step stops use stable reason codes. This logging does not replace the
transactional ledger, reserve future costs, or claim invoice/balance accuracy. An offline saved
trace and its reproducing test live in
[`internal/llm/testdata/retry-failure-trace.jsonl`](../internal/llm/testdata/retry-failure-trace.jsonl)
and `TestSyntheticRetryFailureTrace`; operator filtering commands are in
[`docs/setup.md`](setup.md). **(decision)**

## 9. Telegram

- The agent messages the owner when it meets something outside its permissions, is logged out or
  hits a captcha, or is close to its budget limit.
- A short daily digest: what it did, metrics, playbook changes.
- The owner sends `/pause`, `/resume`, `/kill`, `/dnc <username>`, `/status`, and replies to
  delivered `ask_owner` questions. A shared bot uses `/pause <agent_id>`, `/resume <agent_id>`,
  `/kill <agent_id>`, `/dnc <agent_id> <username>`, and `/status <agent_id>`; ambiguous or unknown
  agents fail safely. `/kill` is terminal: `/resume` cannot revive a killed agent.

Enabled integrations run one long poller per bot and reject conflicting owner chats for that bot.
Only the configured owner chat can issue commands or answer questions. Poll offsets, command
receipts, question delivery IDs and reply correlation persist across restarts. `ask_owner` and
`notify_owner` are private guarded tools that enqueue locally and return immediately. Only the
selected agent's latest ten answers enter its next wake context, with configured secrets redacted;
raw bot updates and chat identifiers never enter that context. Disabled integrations expose neither
owner tool and create no Bot API client. **(decision)**

Daily notices use UTC day identities for action limits and 80%/100% budget thresholds; killed
notices use the terminal state identity. The browser's `BrowserStopped` seam accepts only
`logged_out`/`captcha` plus an internal event ID, pauses that agent, and deduplicates its notice.
Browser detection itself belongs to #8. The daily digest runs at `telegram.digest_hour` (0..23 UTC,
loaded default 18), once per agent/day after that hour, and contains today's post/reply counts,
spend, latest daily metric samples per action, and playbook entries created or decided that day.
No missing metric is represented as measured engagement. **(decision)**

The outbox claims a message before HTTP and never automatically retries failed, in-flight or
uncertain delivery after restart. This favors avoiding duplicate messages over guaranteed delivery;
operators diagnose static failure reasons in `telegram.operation.finished`. Local message row IDs
and local operation counters provide correlation; credentials, chat IDs, message bodies, usernames
and raw HTTP errors are excluded. Owner polling and wake storage share a serialized SQLite
connection pool; no connection is held during Bot API HTTP. Canonical delivery DDL is
[`003_owner_delivery.sql`](../internal/store/migrations/003_owner_delivery.sql), implementation is
[`internal/telegram`](../internal/telegram), and synthetic evidence/reproduction is in
[`telegram-local.md`](research/telegram-local.md). Real-bot qualification remains #18. **(decision)**

## 10. Stack

| Part | Choice | Confidence |
|---|---|---|
| Language | Go, single binary | Research read the repos; nothing was run |
| DB | SQLite via a pure-Go driver (`modernc.org/sqlite`) | Research read the repo; nothing was run |
| LLM | Optional provider/model catalog ([canonical example](../llms.example.yaml)); templates select a profile or declare inline LLM settings | Standard-library adapters implement `openai` Chat Completions and native `anthropic` Messages, including custom base URLs; two-tool live evidence is in [spike-llm.md](research/spike-llm.md) |
| Browser | Headless Chromium over CDP via `chromedp v0.23.0` | Local synthetic transport/lifecycle and sampled process memory measured in [spike-browser.md](research/spike-browser.md); current Threads selectors, account survival, production memory and datacenter-IP behavior remain unverified |
| Deploy | Local process; optional systemd deployment on a chosen Linux host | decision |

## 11. Non-goals for v0

- The official Threads API.
- An LLM critic or multi-agent pipeline.
- Vector search, an ORM, a web framework.
- Delays designed to imitate a human. Spacing between actions exists only as a rate limit.
- A public dashboard and second template follow the local runtime and isolation tests; they do
  not require completion of a live deployment experiment before implementation.

## 12. Order of work

Implementation is local-first. A VPS, real Threads login, Telegram account or elapsed live-run
period is not a prerequisite for writing and testing the harness. Functional issues depend on
the code interfaces they use and finish with offline tests, synthetic fixtures, injected-time
soak tests, redacted logs and eval evidence. Local `dry_run` operation may leave Telegram disabled;
its credentials are required only when that integration is enabled. **(decision, owner update)**

The implementation sequence is the local wake/tool runtime, guards and fake-service integrations,
browser transport validated on local pages, metric collection, playbook/gate simulations, dashboard
and agent isolation. Independent modules are implemented in parallel behind agreed interfaces.
The canonical dependency graph is in GitHub Issues; no code issue waits for a deployment report.

Qualification is separate and remains evidence-based: real-account browser checks and the
three-day observation, a 48-hour single-agent dry run, real-bot controls, six real Threads
operations, the seven-day live experiment, and the two-agent 48-hour memory observation remain
open until actually measured. These may run on a local machine or an operator-chosen deployment
host; a VPS is optional. A datacenter-IP conclusion still requires measurement from that IP.
Synthetic DOM fixtures do not establish that selectors work on the current Threads site, and
simulated time does not count as elapsed soak/live time. Live posting still requires the named
issue scope, owner-provided account/session and explicit live-run approval. **(decision)**

## 13. Unknowns

- Whether Threads blocks or challenges logins from the VPS's datacenter IP.
- How long a browser-automated account survives.
- Whether a cheap model can sustain a long tool-calling loop without drifting.
- Whether the gate opens at all in the first 4 to 6 weeks.

## 14. Repo layout

```text
threadwalker/
├── cmd/threadwalker/main.go     # entrypoint: load config, open DB, run scheduler
├── internal/
│   ├── config/                  # load templates, optional LLM catalog and selected secrets
│   ├── store/                   # SQLite, migrations, queries
│   ├── llm/                     # protocol adapters, shared tool loop and ledger
│   ├── agent/                   # one wake: context assembly and tool loop
│   ├── tools/                   # tool definitions and dispatcher
│   ├── guard/                   # limits, forbidden phrases, do-not-contact
│   ├── threads/                 # browser backend for the Threads tools
│   ├── playbook/                # hypotheses and the gate
│   └── telegram/                # owner notifications and commands
├── agents/                      # one template per agent
│   └── solo-builder.example.yaml
├── llms.example.yaml             # canonical provider/model catalog example
├── deploy/threadwalker.service
└── docs/
```

Every tool implements one interface in `internal/tools`. `internal/guard` wraps the dispatcher, so
no code path executes a tool without passing the guards.

## 15. Database schema

Every table carries `agent_id`, even while only one agent runs. Canonical initial DDL lives in [`internal/store/migrations/001_initial_schema.sql`](../internal/store/migrations/001_initial_schema.sql); delivery/offset changes live in [`003_owner_delivery.sql`](../internal/store/migrations/003_owner_delivery.sql).

| Table | Main columns | Purpose |
|---|---|---|
| `agents` | `id`, `status` (`running`/`paused`/`killed`), `next_wake_at`, `self_note` | State and the note left for the next wake |
| `wakes` | `id`, `agent_id`, `started_at`, `ended_at`, `steps`, `tokens_in`, `tokens_out`, `cost_usd`, `end_reason` | One row per wake |
| `tool_calls` | `id`, `wake_id`, `tool`, `args_json`, `result_json`, `blocked_by`, `at` | Audit log; `blocked_by` names the guard that refused the call |
| `actions` | `id`, `agent_id`, `kind` (`post`/`reply`), `text`, `target_url`, `target_username`, `hypothesis_ids_json`, `dry_run`, `posted_at`, `thread_url` | Published or dry-run posts |
| `metrics` | `action_id`, `measured_at`, `views`, `likes`, `replies`, `reposts` | Repeated measurements |
| `notes` | `id`, `agent_id`, `text`, `created_at`, plus an FTS5 table | The agent's own notes |
| `playbook` | `id`, `agent_id`, `text`, `status` (`testing`/`established`/`rejected`), `n`, `successes`, `created_at`, `decided_at` | Hypotheses; counters are written by code only |
| `contacts` | `agent_id`, `username`, `reply_count_7d`, `do_not_contact`, `reason` | Rate limiting only; no description of the person |
| `ledger` | `agent_id`, `day`, `tokens_in`, `tokens_out`, `cost_usd` | Budget |
| `owner_msgs` | `id`, `agent_id`, `direction`, `text`, `answered_id`, `at` | Telegram exchange |

## 16. Agent template

Each agent template in `agents/*.yaml` (see [`agents/solo-builder.example.yaml`](../agents/solo-builder.example.yaml)) declares:

- `id`, `goal`, `dry_run`.
- `persona`: `name`, `language`, `bio_must_include` (the AI label from section 2), `identity`,
  `voice`, `research_focus` (what to search, read and track across wakes), `style_rules`, `topics`,
  `avoid_topics`, `good_posts`, `good_replies`, `bad_examples`, and `forbidden_phrases`.
- `seed_playbook`: initial `testing` hypotheses for the agent's first wakes.
- `llm`: either a `profile` selecting a model profile in the optional catalog, or the existing
  inline provider/model settings. Inline `protocol` defaults to `openai`; configured protocols
  are `openai` or `anthropic`. Never combine a profile with an inline LLM field, even an empty one.
  Missing catalog/profile/provider references, duplicate IDs and invalid values are errors.
  Provider and profile IDs, model names, endpoints and key environment-variable names are
  operator inputs, with no vendor/model allowlist. The canonical catalog shape lives in
  [`llms.example.yaml`](../llms.example.yaml); the canonical inline shape lives in
  [`agents/solo-builder.example.yaml`](../agents/solo-builder.example.yaml).
- `limits` (all positive): `posts_per_day`, `replies_per_day`, `replies_per_user_7d`,
  `min_seconds_between_actions`, `max_steps_per_wake`, `max_usd_per_day`,
  `min_minutes_between_wakes`.
- `guard`: optional nonempty `stop_phrases` list; omission selects the code defaults.
- `threads`: `profile_dir`, optional `backend` (`stub` by default, or `browser`). Browser mode
  requires an explicit HTTP(S) origin `base_url` and installed Chromium `executable_path`.
  Optional `no_sandbox` defaults to false and is an operator host choice, never a model field.
- `telegram`: optional `enabled` (defaults to `false`); `bot_token_env` and `owner_chat_id_env` are required and resolved only when enabled. Optional `digest_hour` is 0..23 UTC and defaults to 18. The canonical example keeps this optional integration disabled for local operation without a bot.

Secrets live only in environment variables named by `*_env`, never in a template, catalog, log
or the LLM context. Catalog loading validates references and configuration without reading keys;
loading a template resolves only its selected provider's key into a redacting `config.Secret`.
`check` accepts an optional `-llms` catalog path and preserves legacy inline invocation without it.
See [`docs/setup.md`](setup.md) for setup and validation commands. **(decision)**
