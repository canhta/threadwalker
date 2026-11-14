# Template: which keys does an agent template take, with which type, default, owner and effect?

A template is one YAML file in `templates/*.agent.yaml` (the shipped example is [`viral`](../templates/viral.agent.yaml)). The owner writes it; it is fixed while the agent runs and the agent cannot change it. `threadz check --agents DIR` validates a directory with no external calls, and all field errors of a file are reported together. The loader is [`internal/config/decode.go`](../internal/config/decode.go).

Owner column: `template` = text or numbers the agent and the LLM calls read as starting voice or rules; `code` = a hard limit or setting code enforces or computes from; `ops` = plumbing for a backend or host. Who owns what: [architecture.md](architecture.md).

"Required" means the template fails to load without it. Lists are YAML sequences of nonblank strings unless noted.

## Root

| Key | Type | Default | Owner | Effect |
|---|---|---|---|---|
| `id` | string | required | code | Agent ID; scopes every table. IDs must be distinct across the set |
| `goal` | string | required | template | The goal in the system prompt |
| `dry_run` | bool | required | code | True: reads run, writes keep one guarded intent and publish nothing. False enables measured guarded writes ([threads.md](threads.md)) |

## guard

Hard text limits ([guards.md](guards.md)).

| Key | Type | Default | Owner | Effect |
|---|---|---|---|---|
| `guard.stop_phrases` | list, nonempty | code defaults (Vietnamese and English) | code | Replaces the phrases that add a do-not-contact entry from an addressed reply |
| `guard.blocked_phrases` | list | none | code | Safety phrases; text containing one is refused `blocked_phrase` (case-folded substring) |
| `guard.self_stat_units` | list | none (check off) | code | Units (`cent`, `$`, `view`, `follower`...) after which a number must match the facts sheet, else `ungrounded_number` |
| `guard.reply_max_runes` | int 0–500 | 0 | code | A published reply longer than this many characters is refused as `text_length` with "N of MAX"; the writer gets the exact count. 0 leaves only the 500-character cap. A rough draft the writer rewrites is not measured |
| `guard.trend_anchor` | bool | false | code | A post carries `hook`, `rationale` and a `trend` read this wake; first line at most 80 characters |

## persona

All required unless noted. A phrase in both `guard.blocked_phrases` and `persona.style_avoid` is a configuration error.

| Key | Type | Owner | Effect |
|---|---|---|---|
| `persona.name` | string | template | Display name; also used in owner-facing copy |
| `persona.language` | string | template | Language of the account; used in the system prompt. Owner copy on Telegram is always English |
| `persona.bio_must_include` | string | template | The AI label the live bio must carry (the account states it is an AI experiment) |
| `persona.identity` | string | template | Who the account is, and that it says so when asked it is an AI; read by the agent, writer and `ideas` |
| `persona.voice` | string | template | How it sounds: pronouns, register, caps, emoticons, whose it teases; read by the agent and writer |
| `persona.audience` | string | template | Who it writes for, one paragraph; read by writer, critic and `ideas` |
| `persona.critic_rules` | list | template | Numbered conditions under which the critic fails a draft; critic only, at most 6 in the viral template |
| `persona.research_focus` | list | template | What to favour when reading across wakes, without naming tools; agent only, also seeds `fetch_trends` keywords when `trends.include_keywords` is omitted |
| `persona.style_rules` | list | template | The writer's craft rules, each stated once; writer only (the agent prompt does not carry them); also scored by `internal/quality` |
| `persona.topics` | list | template | Topics; seed `fetch_trends` keywords |
| `persona.avoid_topics` | list | template | Off-limits subjects; seed `fetch_trends` avoid keywords |
| `persona.good_posts` | list | template | Starting examples of posts for the agent and writer |
| `persona.good_replies` | list | template | Starting examples of replies |
| `persona.bad_examples` | list | template | Rejected drafts, one distinct flaw each; read by the quality judge |
| `persona.style_avoid` | list | template | The one list of literal phrases the writer steers clear of; shown to the writer and checked by `internal/quality`, never blocks (a refusal needs `guard.blocked_phrases`). Engagement asks are the guard's `engagement_bait`, not listed here |

## LLM blocks

`llm` is required. `writer.llm`, `writer.post_llm` and `critic.llm` are optional and use the same shape. A `profile` key is an error; model names, endpoints, key variable names and reasoning values are operator inputs with no vendor or model allowlist. The [model catalog](evals.md#model-catalog) is eval-only.

| Key (prefix `llm.`, `writer.llm.`, `writer.post_llm.`, `critic.llm.`) | Type | Default | Owner | Effect |
|---|---|---|---|---|
| `protocol` | `openai` or `anthropic` | `openai` | code | Wire adapter ([architecture.md](architecture.md)) |
| `base_url` | string | required | ops | API endpoint |
| `model` | string | required | template | Model ID |
| `api_key_env` | string | required | ops | Environment variable holding the key; resolved into a redacting `Secret` |
| `price_per_mtok_in_usd`, `price_per_mtok_out_usd` | number >= 0 | required | code | Flat rates for the local ledger |
| `cached_input_per_mtok` | number >= 0 | the input rate | code | Rate for cached input tokens (OpenAI `prompt_tokens_details.cached_tokens`, Anthropic `cache_read_input_tokens`); the rest of the input is billed at the input rate |
| `reasoning_effort` | string | unset (omitted) | template | `openai`: sent as `reasoning_effort`. `anthropic`: `thinking: {type: adaptive}` with `output_config.effort` set to it |

| Block | Effect |
|---|---|
| `writer.llm` | Enables the writer and the `ideas` tool: `post` and `reply` take `angle` and `fact`, and a tool-free call drafts the text. Without it the agent's text goes to the critic as written |
| `writer.post_llm` | Model for the writer of a `post` or series; replies and `ideas` keep `writer.llm`. Requires `writer.llm`. Its requests are billed to the same ledger phase at its own prices |
| `critic.llm` | Model for the critic. Without it the critic uses `llm` |


## limits

| Key | Type | Default | Owner | Effect |
|---|---|---|---|---|
| `limits.posts_per_day` | int > 0 | required | code | Posts per local day in `schedule.timezone` (a series counts once) |
| `limits.replies_per_day` | int > 0 | required | code | Replies per local day in `schedule.timezone` |
| `limits.replies_per_user_7d` | int > 0 | required | code | Replies to one username in the rolling 7 days |
| `limits.replies_per_conversation` | int 0..100 | 2 | code | Own replies under one root post (the post, its replies and ours); 0 is off. Lifted for a conversation whose first reply of ours has reached `limits.conversation_hot_likes` likes or `limits.conversation_hot_replies` replies in its newest stored metric |
| `limits.conversation_hot_likes` | int > 0 | 5 | code | Likes on the first reply that lift the conversation cap |
| `limits.conversation_hot_replies` | int > 0 | 3 | code | Replies to the first reply that lift the conversation cap |
| `limits.replies_per_hour` | int 0..1000 | 6 | code | Own replies in any 60 minutes, template's mode, failed rows excluded; 0 is off |
| `limits.breaker_min_likes` | int 0..1000000 | 2 | code | Outcome breaker: when the newest `limits.breaker_actions` own actions that have a metric sample earned fewer likes plus replies than this in total (newest sample each), replies are refused (`reply_breaker`) for `limits.breaker_hours` and one owner notice is queued; posts stay allowed. 0 is off |
| `limits.breaker_actions` | int 1..100 | 15 | code | Measured actions the breaker judges; it does not trip on fewer |
| `limits.breaker_hours` | int 1..168 | 6 | code | Length of the reply pause |
| `limits.browser_replies_per_day` | int > 0 | 5 | code | Cap on replies to feed posts (browser path) |
| `limits.critic_rejections_per_wake` | int 1–20 | 2 | code | Drafts the critic may reject in one wake before further posts and replies are refused (`critic_limit`) |
| `limits.min_seconds_between_actions` | int > 0 | required | code | Spacing between actions |
| `limits.min_minutes_between_posts` | int > 0 | off | code | Spacing between posts; replies only follow `min_seconds_between_actions` |
| `limits.max_steps_per_wake` | int > 0 | required | code | Model requests an agent wake may make; a request that returns several tool calls costs one |
| `limits.max_usd_per_day` | number > 0 | required | code | Daily spend cap shared by the agent and critic phases; the day runs 00:00 to 24:00 in `schedule.timezone` |
| `limits.min_minutes_between_wakes` | int 1..1440 | required | code | Wake spacing when behind, and the minimum cadence ([wake.md](wake.md)) |
| `limits.max_usd_per_dream` | number > 0 | 0.30 | code | Dream spend cap, outside the daily cap |
| `limits.max_steps_per_dream` | int > 0 | 8 | code | Model requests the dream may make |

## kpi

| Key | Type | Default | Owner | Effect |
|---|---|---|---|---|
| `kpi.engagements_per_day` | int > 0 | no block | code | Daily engagement target; behind when under it |
| `kpi.followers_per_week` | int > 0 | no block | code | Weekly follower target shown on the scoreboard |
| `kpi.max_hours_without_publish` | int > 0 | no block | code | Behind when the last publish is this old; also the silence, plus 3 hours, after which a live agent's owner gets one notice a day ([operations.md](operations.md)) |
| `kpi.on_target_minutes_between_wakes` | int, `limits.min_minutes_between_wakes`..1440 | no block | code | Wait before the next wake when on target |

The `kpi:` block is optional; once any of its four keys exists all four are required. The viral template starts at 10, 5, 8, 180.

## schedule

| Key | Type | Default | Owner | Effect |
|---|---|---|---|---|
| `schedule.timezone` | IANA name | `Asia/Ho_Chi_Minh` | code | Zone of the dream time, of the quiet window and of every daily cap |
| `schedule.dream_at` | `HH:MM` local | empty (off) | code | Nightly dream, once per local day |
| `schedule.quiet_from`, `schedule.quiet_until` | `HH:MM` local, both or neither | empty (off) | code | Night window with no agent wake; one scheduled inside it moves to `quiet_until`. May cross midnight. The dream keeps its own time |

## inbox

| Key | Type | Default | Owner | Effect |
|---|---|---|---|---|
| `inbox.poll_minutes` | int 0..1440 | 10 | code | Minutes between the scheduler's reply polls between wakes; 0 turns the polling off (each wake still lists the inbox) |
| `inbox.min_minutes_after_wake` | int 1..1440 | 5 | code | Least minutes between the start of a wake and an early wake the poll asks for |

## trends, targets, web_search

Source IDs are validated against the fixed set `google_news_vn`, `google_news_global`, `google_news_topics`, `vnexpress`, `genk`, `tinhte`, `vietnamnet`, `the_verge`, `techcrunch`, `arstechnica`, `hacker_news`, `product_hunt`, `google_trends`, `github_trending`. None needs credentials.

| Key | Type | Default | Owner | Effect |
|---|---|---|---|---|
| `trends.sources` | list of source IDs | Hacker News, Product Hunt, Google Trends VN | code | Sources a normal wake's `fetch_trends` reads |
| `trends.include_keywords` | list | derived from `persona.topics` and `persona.research_focus` | template | Ranking keywords (at most 48 derived) |
| `trends.avoid_keywords` | list | derived from `persona.avoid_topics` | template | Results matching are dropped |
| `targets.max_age_minutes` | int 1..10080 | 240 | code | A reply target older than this is `outside window`: it scores 0 and a reply to it is refused; the freshness term's zero |
| `targets.max_replies` | int 1..100000 | 30 | code | A reply target with more replies is outside the window; the room term's zero |
| `targets.min_likes_per_hour` | number > 0 | 5 | code | A reply target liked slower than this is outside the window; the momentum term's half point |
| `targets.min_likes` | int 0..1000000 | 0 (off) | code | A reply target whose like count is known and below this is outside the window |
| `web_search.enabled` | bool | false | code | Turns on `web_search`; requires `llm.protocol: openai` |
| `web_search.model` | string | `llm.model` | template | Model of the web-search call; required when enabled |
| `web_search.base_url` | HTTP(S) URL without query or fragment | `https://api.openai.com/v1` | ops | Endpoint |
| `web_search.max_results` | int 1..10 | 5 | code | Citation count |
| `web_search.max_output_tokens` | int 1..1024 | 512 | code | Output cap |

## threads

| Key | Type | Default | Owner | Effect |
|---|---|---|---|---|
| `threads.username` | string | required | code | Operator-declared own account, normalized, never inferred; drives the own-account guard |
| `threads.profile_dir` | path | required | ops | Browser profile of the agent; canonical paths must not overlap across agents |
| `threads.backend` | `stub` or `api` | `stub` | ops | `stub` is the in-memory backend and requires `dry_run`; `api` is the live backend |
| `threads.access_token_env` | string | `THREADS_ACCESS_TOKEN` | ops | `api` only. Optional seed of the long-lived token; a sealed stored token wins |
| `threads.user_id_env` | string | `THREADS_USER_ID` | ops | `api` only. Required value in the environment |
| `threads.base_url` | HTTPS `threads.com` or `threads.net` origin, no path | required for `api` | ops | Browser origin for the feed read |
| `threads.executable_path` | path | required for `api` | ops | Installed Chromium |
| `threads.agent_browser_path` | absolute path | required for `api` | ops | The pinned `agent-browser` CLI |
| `threads.no_sandbox` | bool | false | ops | Host choice for constrained hosts, never a model field |

The sealing key is `THREADZ_SECRET_KEY` ([threads.md](threads.md)).

## telegram

| Key | Type | Default | Owner | Effect |
|---|---|---|---|---|
| `telegram.enabled` | bool | false | ops | Enables the owner integration; when false neither owner tool exists |
| `telegram.bot_token_env`, `telegram.owner_chat_id_env` | string | required when enabled | ops | Environment variable names, resolved only when enabled |
| `telegram.report_hour` | int 0..23 | 21 | ops | Local hour of the daily report, in `telegram.timezone` |
| `telegram.timezone` | IANA name | `Asia/Ho_Chi_Minh` | ops | Owner's zone for displayed times and the report hour |
