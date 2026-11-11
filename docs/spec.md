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
| Host | Owner's VPS: Ubuntu 24.04, 2 vCPU, 3.6 GB RAM (about 2.6 GB free), 48 GB disk free; no Go, Docker or Chromium installed yet | measured |
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

No fixed triage/compose/critic pipeline and no same-model critic. **(sourced)**: first-party guidance says
to maximise a single agent first, and in Anthropic's Project Vend a same-model supervisor shared
the blind spots of the agent it supervised.

## 4. One wake

1. The harness wakes the agent at the time the agent chose last wake, within a frequency limit.
2. The harness assembles context: template (immutable), playbook, fresh metrics computed by code,
   remaining budget, and the note the agent left for itself.
3. The agent calls tools until it ends the wake or hits the step or token limit.
4. Every tool call passes through the guards, is written to the audit log, then executes.
5. The agent writes a note for next time and schedules its next wake.

Two rules keep a wake from doing damage **(sourced: patterns in `docs/research/reference-repos.md`)**:

- **Stall stop.** A wake ends when the agent issues the same tool call with the same arguments
  three times; the end reason is recorded.
- **No replay.** An interrupted wake is never re-run. A `post` or `reply` whose outcome is unknown
  is recorded as sent and never retried, so a crash cannot double-post.

Reflection is something the agent chooses to do when new metrics exist; there is no fixed nightly
job. **(proposed)**

## 5. Tools

| Group | Tools | Notes |
|---|---|---|
| Threads (browser) | `read_feed`, `search`, `read_post`, `post`, `reply`, `read_own_metrics` | One browser profile per agent |
| Outside sources | `fetch_trends` (Hacker News API, Product Hunt RSS, Google Trends VN RSS) | All three responded when the research called them |
| Notes | `note_write`, `note_search` | SQLite FTS5; vector search is not justified at this scale **(sourced)** |
| Playbook | `playbook_read`, `playbook_propose` | See section 7 |
| Owner | `notify_owner`, `ask_owner` | Telegram; `ask_owner` never blocks the wake |
| Contacts | `mark_do_not_contact` | The agent can add to the list, never remove |
| Scheduling | `schedule_next_wake` | Clamped to the allowed range |

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

## 9. Telegram

- The agent messages the owner when it meets something outside its permissions, is logged out or
  hits a captcha, or is close to its budget limit.
- A short daily digest: what it did, metrics, playbook changes.
- The owner sends `/pause`, `/resume`, `/kill`, `/dnc <username>`, and answers `ask_owner` questions.

## 10. Stack

| Part | Choice | Confidence |
|---|---|---|
| Language | Go, single binary | Research read the repos; nothing was run |
| DB | SQLite via a pure-Go driver (`modernc.org/sqlite`) | Research read the repo; nothing was run |
| LLM | Optional provider/model catalog ([canonical example](../llms.example.yaml)); templates select a profile or declare inline LLM settings | Standard-library adapters implement `openai` Chat Completions and native `anthropic` Messages, including custom base URLs; two-tool live evidence is in [spike-llm.md](research/spike-llm.md) |
| Browser | Headless Chromium over CDP | **Untested**: which Go library, real memory use, whether Threads challenges datacenter IPs |
| Deploy | systemd unit on the VPS | proposed |

## 11. Non-goals for v0

- The official Threads API.
- An LLM critic or multi-agent pipeline.
- Vector search, an ORM, a web framework.
- Delays designed to imitate a human. Spacing between actions exists only as a rate limit.
- Later, not now: a public dashboard (must redact strangers' usernames), a second agent, a second
  template.

## 12. Order of work

Steps are gated: a step starts only when the one before it has passed. Each step's exit condition
is in its GitHub issue.

1. Browser spike on the VPS. This step can end the project.
2. Dry-run agent loop.
3. Live run with low limits and Telegram.
4. Playbook and gate.
5. Public dashboard.
6. Second agent, second template.

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

Every table carries `agent_id`, even while only one agent runs. Canonical DDL lives in [`internal/store/migrations/001_initial_schema.sql`](../internal/store/migrations/001_initial_schema.sql).

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
- `threads`: `profile_dir`.
- `telegram`: `bot_token_env`, `owner_chat_id_env`.

Secrets live only in environment variables named by `*_env`, never in a template, catalog, log
or the LLM context. Catalog loading validates references and configuration without reading keys;
loading a template resolves only its selected provider's key into a redacting `config.Secret`.
`check` accepts an optional `-llms` catalog path and preserves legacy inline invocation without it.
See [`docs/setup.md`](setup.md) for setup and validation commands. **(decision)**
