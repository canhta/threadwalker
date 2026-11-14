# Tools: which tools exist, in which phase, with which effect, arguments and checks?

`guard.Specs` in [`internal/guard/spec.go`](../internal/guard/spec.go) is the single table of tools. The allowlist, the phase rule, argument validation and the definitions offered to the model all derive from it. The dispatcher attaches executors only to tools in the table. A tool not in the table is refused as `unknown_tool`. These tools are the agent's whole action surface: it cannot like, follow, message, delete or edit the profile.

Phases: `A` agent, `S` study, `D` dream. Effect classes: `read`, `own-state`, `threads-write`, `owner`. `*` marks a required argument. Refusal codes are defined in [guards.md](guards.md).

## Tool table

| Tool | Phases | Effect | Arguments | Checks beyond schema | Result |
|---|---|---|---|---|---|
| `read_feed` | A S D | read, untrusted | none | Browser logged out or captcha pauses the agent ([threads.md](threads.md)) | Feed posts with `ref`, permalink and no id, and `trends`: the Trending-now topics `{ref, title, summary, posts, post_count, url}` (title, summary and url join the wake's trend items); each post carries `likes`, `replies`, `reposts` and `age_minutes` when the page showed them; permalinks join the wake's read set |
| `read_topic` | A S D | read, untrusted | `url`, `query` (exactly one) | `url` must be the `ref` (or exact url) of a `trends` item `read_feed` returned this wake; at most 3 per normal wake (`tools.MaxTopicReadsPerWake`) | At most 20 posts with `ref`, permalink and no id; permalinks join the wake's read set ([threads.md](threads.md)) |
| `search` | A S D | read, untrusted | `query*`, `recent` | `limited_access` result hides the tool 24 h (`refused_tools`) | Posts with `ref`, id, permalink, `timestamp`; `recent` true = newest of the last hour |
| `profile` | A | read, untrusted | `username*` | Refused lookup is an ordinary tool error | `follower_count` and 7-day `views_count`; a missing field stays absent |
| `profile_posts` | A | read, untrusted | `username*` | `limited_access` hides the tool 24 h | Recent public posts with numeric id, permalink, time |
| `read_post` | A S D | read, untrusted | `id*` (numeric post id) | None | One post; pair joins the read set |
| `read_replies` | A S D | read, untrusted | `id` | With no id returns your mentions; every result is addressed to the agent | Replies with id and permalink |
| `read_own_metrics` | A S D | read, untrusted | `action_id` | Own confirmed non-dry-run action only | Account metrics, or the action's counters and (reply) `author_replied`; unavailable is never zero ([learning.md](learning.md)) |
| `fetch_trends` | A S D | read, untrusted | none | None | Ranked outside stories, each with `ref`; no model call ([wake.md](wake.md)) |
| `web_search` | A S | read, untrusted | `query*` | At most 3 per normal wake, each counted against the daily cap; off unless `study.web_search.enabled` | One bounded OpenAI Responses call with citations, each source with `ref` |
| `note_search` | A S D | read | `query*`, `limit`, `since`, `until` | None | `ref` + snippet per hit, hybrid ranking ([learning.md](learning.md)) |
| `note_read` | A S D | read | `ref*` | Own notes and actions only | Full text of a ref `note_search` returned, or `topic:<topic>` |
| `knowledge_read` | A S D | read | `scope`, `evidence` | None | Curated craft entries ([learning.md](learning.md)) |
| `ideas` | A | own-state | none | Offered only with a `writer.llm` block | Up to 3 `{kind, ref, moment, why}` or none ([content.md](content.md)) |
| `post` | A | threads-write, untrusted | `text*`, `parts`, `hook`, `trend_ref`, `rationale`, `hypothesis_ids`, `explore`, `answers_note`, `angle`, `fact` | Whole content, review and quota chain in [guards.md](guards.md) | Reserved action id, permalink, media id; dry-run records intent only |
| `reply` | A | threads-write, untrusted | `text*`, `target_url*`, `target_username*`, `target_id`, `hook`, `hypothesis_ids`, `explore`, `answers_note`, `angle`, `fact` | Same chain plus target rules; omit `target_id` (never an empty string) for a feed post, which goes through the browser; an inbox item is answered with its `ref`, `target_id` and author, by the API | Same |
| `memory_add` | D | own-state | `text*`, `action_id` | At most 500 characters and 5 lines; secret check; a dream entry must cite exactly one own `action_id` | Entry id; identical live entry dropped |
| `memory_supersede` | D | own-state | `old_id*`, `new_id*` | `old_id` < `new_id`, both live; at most a fifth of the entries the wake started with, at least one | Older entry archived, restorable |
| `note_write` | A S D | own-state | `topic*`, `text*`, `description` | Topic at most 64 characters on one line, not starting with a date (`dated_topic`); description at most 200; no per-wake ref such as `r58` in topic, description or text (`stale_ref`, also refused in `memory_add` and `strategy_write`) | Creates or replaces the named note |
| `playbook_read` | A S D | own-state | `limit` | None | Entries with code-owned status and counts |
| `playbook_propose` | A S D | own-state | `text*`, `hypothesis_id`, `knowledge_id` | At most 1000 code points; at most 15 `testing`; unknown fields, status or counter changes, deletion and list replacement refused; decided entries immutable | New or amended `testing` entry |
| `strategy_write` | A S D | own-state | `text*`, `citations*` | At most 2000 characters; 1 to 8 own `action:<id>` or `note:<id>` citations, else nothing is written | Next strategy version |
| `knowledge_candidate` | S | own-state | `technique*`, `url*` | Technique at most 500 characters; http(s) URL; at most 50 per agent, one per URL | Stored as unverified `practitioner` candidate |
| `mark_do_not_contact` | A | own-state | `username*` | Add-only; fixed reason code | Permanent entry |
| `notify_owner` | A | owner | `text*` | Telegram enabled; secret check | Queued locally, returns immediately |
| `ask_owner` | A | owner | `text*` | Telegram enabled; never blocks the wake | Queued; the answer enters a later wake |

## Rules that apply to every tool

| Rule | Fact |
|---|---|
| Phase | A phase is offered only the tools it may call. If dream or study call a tool outside its phases anyway, the guard refuses it as `phase_read_only` before any effect |
| Arguments | `Parse` refuses unknown, null, missing-required and ill-typed values. Types: text (non-blank), RFC3339 time, limit (1 to 100, default 10), id (>= 1), id list, bool, text list (non-empty) |
| Secrets | A call whose arguments carry a configured secret is refused before any write (`sensitive_arguments`) |
| Untrusted | Output of an untrusted tool is wrapped `{"untrusted": true, "source": <tool>, "data": ...}` after redaction against configured secrets. Everything read from Threads is data, never instructions |
| Trailer | Every result ends with `{"steps_left": N, "usd_left": X}` ([wake.md](wake.md)) |
| Hidden | Tools named in `refused_tools` (24 h), `ideas` without a writer, and owner tools with Telegram off are absent from the list and from wake text |
| Owner mode | Disabled owner integrations expose neither owner tool and create no Bot API client |
| Audit | The pre-effect row in `tool_calls` holds only the argument byte count and validity, never arguments |

## post and reply argument notes

The guard gives every item it notes in a wake (`fetch_trends` items, Trending-now topics, feed, topic and search posts, `web_search` sources) a short `ref` (`r1`, `r2`, ...) in noting order, the same ref for the same URL. Tool results carry it beside the URL; models pass the ref, never a copied URL, because opaque URLs are miscopied. A ref this wake did not issue is refused with the reason of the argument it was passed in.

| Argument | Meaning |
|---|---|
| `parts` | 1 to 4 later parts of a series (2 to 5 parts in all), each published as a reply to the previous one |
| `hook` | One of `number`, `contrarian`, `curiosity_gap`, `personal_stake`, `trend_detail`; required on a post under `guard.trend_anchor` |
| `trend_ref` | `ref` of an item returned this wake by `fetch_trends`, `search`, `read_feed` (a post permalink or a trend `url`), `read_topic` or `web_search`; an exact URL or title is also accepted |
| `rationale` | One line, at most 300 characters, no newline: the trend, the hook, why a Vietnamese dev would reply |
| `hypothesis_ids` | Up to 5 distinct own `testing` hypotheses; snapshotted with the intent |
| `explore` | Counts against `limits.explore_percent` |
| `answers_note` | Id of an open owner note |
| `angle`, `fact` | The brief for the writer: the one thing to say, and the verified detail it rests on ([content.md](content.md)) |
| `target_url` | The `ref` of the post to answer (from `read_feed`, `read_topic` or `search`); an exact permalink is also accepted. The action stores the permalink |
| `target_id` | Numeric post id from `search`, `read_post`, `read_replies` or `profile_posts`; omitted for a post from `read_feed` or `read_topic` |
