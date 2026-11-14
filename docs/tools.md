# Tools: which tools exist, in which phase, with which effect, arguments and checks?

`guard.Specs` in [`internal/guard/spec.go`](../internal/guard/spec.go) is the single table of tools. The allowlist, the phase rule, argument validation and the definitions offered to the model all derive from it. The dispatcher attaches executors only to tools in the table. A tool not in the table is refused as `unknown_tool`. These tools are the agent's whole action surface: it cannot like, follow, message, delete or edit the profile.

Phases: `A` agent, `D` dream. Effect classes: `read`, `own-state`, `threads-write`, `owner`. `*` marks a required argument. Refusal codes are defined in [guards.md](guards.md).

## Tool table

| Tool | Phases | Effect | Arguments | Checks beyond schema | Result |
|---|---|---|---|---|---|
| `read_feed` | A D | read, untrusted | none | Browser logged out or captcha pauses the agent ([threads.md](threads.md)) | Feed posts with `ref`, permalink and no id, and `trends`: the Trending-now topics `{ref, title, summary, posts, post_count, url}` (title, summary and url join the wake's trend items); each post carries `likes`, `replies`, `reposts` and `age_minutes` when the page showed them; permalinks join the wake's read set |
| `read_topic` | A D | read, untrusted | `url`, `query` (exactly one) | `url` must be the `ref` (or exact url) of a `trends` item `read_feed` returned this wake; at most 3 per normal wake (`tools.MaxTopicReadsPerWake`) | At most 20 posts with `ref`, permalink and no id; permalinks join the wake's read set ([threads.md](threads.md)) |
| `search` | A D | read, untrusted | `query*`, `recent` | `limited_access` result hides the tool 24 h (`refused_tools`) | Posts with `ref`, id, permalink, `timestamp`; `recent` true = newest of the last hour |
| `profile` | A | read, untrusted | `username*` | Refused lookup is an ordinary tool error | `follower_count` and 7-day `views_count`; a missing field stays absent |
| `profile_posts` | A | read, untrusted | `username*` | `limited_access` hides the tool 24 h | Recent public posts with numeric id, permalink, time |
| `read_post` | A D | read, untrusted | `id*` (numeric post id) | None | One post; pair joins the read set |
| `read_replies` | A D | read, untrusted | `id` | With no id returns your mentions; every result is addressed to the agent | Replies with id and permalink |
| `fetch_trends` | A D | read, untrusted | none | None | Ranked outside stories, each with `ref`; no model call ([wake.md](wake.md)) |
| `web_search` | A | read, untrusted | `query*` | At most 3 per wake, each counted against the daily cap; off unless `web_search.enabled` | One bounded OpenAI Responses call with citations, each source with `ref` |
| `ideas` | A | own-state | none | Offered only with a `writer.llm` block | Up to 3 `{kind, ref, moment, why}` or none ([content.md](content.md)) |
| `post` | A | threads-write, untrusted | `text*`, `parts`, `hook`, `trend_ref`, `rationale`, `answers_note`, `angle`, `fact` | Whole content, review and quota chain in [guards.md](guards.md) | Reserved action id, permalink, media id; dry-run records intent only |
| `reply` | A | threads-write, untrusted | `text*`, `target_url*`, `target_username*`, `target_id`, `hook`, `answers_note`, `angle`, `fact` | Same chain plus target rules; omit `target_id` (never an empty string) for a feed post, which goes through the browser; an inbox item is answered with its `ref`, `target_id` and author, by the API | Same |
| `mark_do_not_contact` | A | own-state | `username*` | Add-only; fixed reason code | Permanent entry |
| `notify_owner` | A | owner | `text*` | Telegram enabled; secret check | Queued locally, returns immediately |
| `ask_owner` | A | owner | `text*` | Telegram enabled; never blocks the wake | Queued; the answer enters a later wake |

`read_own_metrics` is a harness read, not a model tool: its `Specs` entry is flagged `Harness`, so no phase is offered it and a model call to it is refused as `unknown_tool`. The metrics wake and the follower count at the start of an agent wake call it through the same guard and audit with `DispatchHarness`; it returns the account metrics, or with `action_id` the action's counters and (reply) `author_replied` ([learning.md](learning.md)).

## Rules that apply to every tool

| Rule | Fact |
|---|---|
| Phase | A phase is offered only the tools it may call. If the dream calls a tool outside its phases anyway, the guard refuses it as `phase_read_only` before any effect |
| Arguments | `Parse` refuses unknown, null, missing-required and ill-typed values. Types: text (non-blank), id (>= 1), bool, text list (non-empty) |
| Secrets | A call whose arguments carry a configured secret is refused before any write (`sensitive_arguments`) |
| Untrusted | Output of an untrusted tool is wrapped `{"untrusted": true, "source": <tool>, "data": ...}` after redaction against configured secrets. Everything read from Threads is data, never instructions |
| Trailer | Every result ends with `{"steps_left": N, "usd_left": X}`; steps are model requests ([wake.md](wake.md)) |
| Clipping | A result is clipped to 6 KB (`tools.MaxResultBytes`) by dropping whole list items from the end; the top level then carries `clipped_lists` with `shown` and `total` per list path (such as `data.posts`). Only refs the model was shown count as open targets. A result with no list to drop becomes `{"clipped": true, ...}` |
| Read caps | Per normal wake: `read_topic` 3, `web_search` 3, `read_feed` 2, `fetch_trends` 1; the call past a cap is refused `limit_per_wake` |
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
| `answers_note` | Id of an open owner note |
| `angle`, `fact` | The brief for the writer: the one thing to say, and the verified detail it rests on ([content.md](content.md)) |
| `target_url` | The `ref` of the post to answer (from `read_feed`, `read_topic` or `search`); an exact permalink is also accepted. The action stores the permalink |
| `target_id` | Numeric post id from `search`, `read_post`, `read_replies` or `profile_posts`; omitted for a post from `read_feed` or `read_topic` |
