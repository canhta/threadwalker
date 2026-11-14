# Tools: which tools exist, in which phase, with which effect, arguments and checks?

`guard.Specs` in [`internal/guard/spec.go`](../internal/guard/spec.go) is the single table of tools. The allowlist, the phase rule, argument validation and the definitions offered to the model all derive from it. The dispatcher attaches executors only to tools in the table. A tool not in the table is refused as `unknown_tool`. These tools are the agent's whole action surface: it cannot like, follow, message, delete or edit the profile.

Phases: `A` agent, `D` dream. Effect classes: `read`, `own-state`, `threads-write`, `owner`. `*` marks a required argument. Refusal codes are defined in [guards.md](guards.md).

## Tool table

| Tool | Phases | Effect | Arguments | Checks beyond schema | Result |
|---|---|---|---|---|---|
| `find_posts` | A | read, untrusted | `source*` (`feed`, `topic`, `search`, `profile`), `query`, `topic`, `username`, `recent` | Each source takes only its own arguments (`find_args`): `feed` none; `topic` exactly one of `topic` (the `t` ref of a Trending-now topic that `feed` returned this wake, a Threads topic link) or `query`; `search` `query*`, `recent`; `profile` `username*`. A `limited_access` result closes `search` or `profile` for 24 h (`refused_tools`, `find_posts:<source>`). A logged-out or captcha browser pauses the agent ([threads.md](threads.md)) | Posts with `ref` (`p12`), permalink, `likes`, `replies`, `reposts` and `age_minutes` when the page showed them, `topic`, and the numeric `id` and `timestamp` an API read carries; `feed` also returns `trends`: the Trending-now topics `{ref, title, summary, posts, post_count, url}` with `t` refs; at most 20 posts for `topic` |
| `profile` | A | read, untrusted | `username*` | Refused lookup is an ordinary tool error | `follower_count` and 7-day `views_count`; a missing field stays absent |
| `read_post` | A | read, untrusted | `id*` (numeric post id) | None | One post with its `ref` |
| `read_replies` | A | read, untrusted | `id` (numeric id of one of your posts) | With no id returns your mentions; every result is addressed to the agent | Replies, each with its `ref` |
| `fetch_trends` | A | read, untrusted | none | Per-wake cap ([guards.md](guards.md)) | Ranked outside stories, each with a `t` ref; no model call ([wake.md](wake.md)) |
| `web_search` | A | read, untrusted | `query*` | Per-wake cap ([guards.md](guards.md)); each call counts against the daily cap; off unless `web_search.enabled` | One bounded OpenAI Responses call with citations, each source with a `t` ref |
| `ideas` | A | own-state | none | Offered only with a `writer.llm` block | Up to 3 `{kind, ref, moment, why}` or none ([content.md](content.md)) |
| `post` | A | threads-write, untrusted | `text*`, `parts`, `topic_tag`, `hook`, `trend`, `rationale`, `answers_note`, `angle`, `fact`, `lesson_ids` | Whole content, review and quota chain in [guards.md](guards.md) | Reserved action id, permalink, media id; dry-run records intent only |
| `reply` | A | threads-write, untrusted | `text*`, `target*`, `hook`, `answers_note`, `angle`, `fact`, `lesson_ids` | Same chain plus target rules. `target` is the `p` ref of one stored post; the author, permalink and API id come from the stored row, and the dispatcher takes the API path when the row has an API id and the browser path when it has none | Same |
| `lesson_add` | D | own-state | `text*`, `kind*`, `polarity*`, `action_ids*` | Dream only; text, citation and count rules in [learning.md](learning.md#lessons) | The stored lesson id, or a refusal |
| `lesson_confirm` | D | own-state | `id*`, `action_ids*` | Dream only; at least one new action meeting the same rule | The renewed lesson |
| `lesson_retire` | D | own-state | `id*`, `reason*` | Dream only | The retired lesson |
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
| Clipping | A result is clipped to 6 KB (`tools.MaxResultBytes`) by dropping whole list items from the end; the top level then carries `clipped_lists` with `shown` and `total` per list path (such as `data.posts`). Every post read is stored whether or not the clip showed it. A result with no list to drop becomes `{"clipped": true, ...}` |
| Per-wake caps | `limit_per_wake` in [guards.md](guards.md) lists every cap |
| Hidden | Tools and `find_posts` sources named in `refused_tools` (24 h), `ideas` without a writer, and owner tools with Telegram off are absent from the tool list (the system prompt names no tool); a call to a closed source is refused `tool_hidden` |
| Size | The agent phase's tool definitions are at most 6 KB (5.9 KB now) and each description at most 300 characters, pinned by `TestAgentToolDefinitionsGolden` and `internal/tools/testdata/agent-tools.golden.json`. A description says what the tool does and each argument is described in the JSON schema; policy (caps, spacing, repeat and anchor rules, owner-note rules) lives in guard code and is stated only in the refusal hint |
| Owner mode | Disabled owner integrations expose neither owner tool and create no Bot API client |
| Audit | The pre-effect row in `tool_calls` holds only the argument byte count and validity, never arguments |

## Refs: p and t

Every post a read returns is stored per agent under a durable id, shown as `p<N>`: the same permalink keeps its id across wakes, for 7 days after it was last read (table `seen_posts`, [data.md](data.md)). Trends, `web_search` sources and Trending-now topics, which are not posts, get a `t<N>` ref that restarts every wake. Tool results carry the ref in the `ref` field beside the URL; the model passes the ref, never a copied URL. The inbox and the open reply candidates list `p` refs too. A `p` ref works only in the wake that issued it, through a read, the inbox or the open reply candidates; a ref from an earlier wake is refused until the post is read again. A ref the guard cannot resolve is refused: `unknown_target` for `reply.target`, `no_trend_anchor` for `post.trend`, `find_args` for `find_posts.topic`.

The data message lists up to 8 open reply candidates (`guard.OpenCandidates`): posts read in the last 6 hours, not replied to in the last day, not the account's own or a do-not-contact author, and inside the template's `targets` window and scoring at least 0.5 with their age advanced to now. It is the one source of "posts worth a reply"; the stop check names the same list.

| Argument | Meaning |
|---|---|
| `parts` | 1 to 4 later parts of a series (2 to 5 parts in all), each published as a reply to the previous one |
| `hook` | One of the hook types ([content.md](content.md)), an enum in the schema; with `guard.trend_anchor` a post also needs `trend` and `rationale` (`post_brief`) |
| `trend` | A `t` or `p` ref of a trend, source or post this wake issued; the action stores the item's URL or title, never the ref |
| `rationale` | One line, at most 300 characters, no newline: the trend, the hook, why a Vietnamese dev would reply |
| `answers_note` | Id of an open owner note |
| `angle`, `fact` | The brief for the writer: the one thing to say, and the detail it rests on, as read ([content.md](content.md)) |
| `target` | The `p` ref of the post to answer. The action stores the permalink and the author of the stored post |
