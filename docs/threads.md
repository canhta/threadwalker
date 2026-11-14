# Threads: which operations use the API or the browser, with which permissions, limits and reply routing?

Every own-account Threads operation uses the official Threads API, except two: the browser reads the home feed and its Trending-now panel, topic and search pages (the API has no home-feed endpoint and keyword search is limited without App Review) and replies to feed posts (the API rejects ids decoded from permalinks, `400 code 100`). Both sit behind the `threads.Backend` contract; tests use the in-memory `threads.Memory` backend.

`internal/threads.API` calls `https://graph.threads.net/v1.0` with a context timeout on every call. The long-lived token is a `config.Secret` covered by the redactor and travels in the `Authorization` header, never a URL. An API error carries only its HTTP status and error code, never the body.

## Operations

| Operation | Via | Call | Notes |
|---|---|---|---|
| `post` | API | `POST /{user}/threads` (container), `GET /{container}?fields=status` until `FINISHED` (5 polls, 2 s apart), `POST /{user}/threads_publish`, then permalink and media id | Series: repeats the flow per later part with `reply_to_id` = media id of the part before, after a 5 s pause from the injected wait; each part's permalink and media id are stored in `action_parts` before the next is created. A later part that fails ends the wake with the published parts recorded; the series is never retried |
| `reply` to a post with an API id | API | Same flow with `reply_to_id` = numeric post id | The stored post has a `media_id` |
| `reply` to a post with no API id | Browser | Opens the feed post's permalink, types in the inline composer, clicks Reply, reads the new permalink from the confirmation | Then looks the reply's media id up in `GET /me/replies` by matching text; no match stores no media id and seeds no metric jobs. Capped by `limits.browser_replies_per_day`; the action stores a `browser` flag |
| `read_post` | API | `GET /{id}` | Argument is the numeric id a search, profile, reply or mention result carries |
| `read_replies` | API | `GET /{id}/replies` for an own post, or `GET /{user}/mentions` with no id | Every result is addressed to the agent |
| `find_posts` source `search` | API | `GET /keyword_search` (`search_type=TOP`; with `recent` true `RECENT` and `since` one hour back) | Each result carries `timestamp`. Without App Review for `threads_keyword_search` the endpoint searches only the authenticated user's own posts, so an empty result carries `limited_access` and the agent anchors on `fetch_trends` and `web_search` |
| `find_posts` source `profile` | API | `GET /profile_posts?username=` | Standard access serves only a few official Meta accounts; other profiles, and profiles under 100 followers, are refused with a `limited_access` page, not an error. Flow: `find_posts` `feed` -> `find_posts` `profile` -> `read_post` or `reply` by `target` |
| `profile` | API | `GET /profile_lookup?username=` | `follower_count` and 7-day `views_count`; a missing field stays absent, a refused lookup is an ordinary tool error |
| `read_own_metrics` (account) | API and browser | `GET /{user}/threads_insights` (views, likes, replies, reposts, quotes); the follower count is read from the own profile page `/@{threads.username}`, not the API | The API `followers_count` lags or reads 0 on a live account, so the page count ("1 follower", "1.2K followers", "N người theo dõi") is the source of truth. The API value is the fallback when `threads.username` is unset or the page shows no count; the log names the source as `followers_browser` or `followers_api_fallback` |
| `read_own_metrics` with `action_id`, and metric jobs | API | `GET /{post}/insights` (views, likes, replies, reposts, quotes, shares) | By the media id stored at publish; none or no views, likes, replies and reposts = unavailable, not zero |
| `find_posts` source `feed` | Browser | Pinned `agent-browser` profile | Each post has a permalink and no id |
| `find_posts` source `topic` | Browser | Same session as `feed`: opens the topic URL of a Trending-now `t` ref (`/search?q=&serp_type=trends&trend_fbid=`) or, for `query`, `/search?q=&serp_type=default` | Posts like `feed`, at most 20; a feed-post reply path |
| Own-thread listing | API | `GET /{user}/threads` | Reconciles unpublished intents; a deleted post is told by `400 code 100` plus a listing without it |

## Permissions and limits

| Item | Fact |
|---|---|
| OAuth scopes (8) | `threads_basic`, `threads_content_publish`, `threads_manage_insights`, `threads_manage_replies`, `threads_read_replies`, `threads_manage_mentions`, `threads_keyword_search`, `threads_profile_discovery` |
| Access level | The app's tester role needs no Business Verification. Keyword search and profile discovery are limited without App Review (rows above) |
| Publishing quota | `threads_publishing_limit`: 250 posts and 1000 replies per 24 hours at the measured account. An extra guard limit beside the template's ([guards.md](guards.md)) |
| Publish outcome | A publish that returns an id is confirmed with its permalink. A failure before the publish call (container error, not `FINISHED` after the polls) is a normal failure with no effect. After the publish call is sent, any failure, timeout or unreadable permalink is `ErrUnknownOutcome` and never retried |
| `dry_run` | Reads execute normally. Writes retain one guarded intent without publishing. A non-dry-run backend publishes only after the intent commits, and stores `thread_url` only after the API returns the permalink. Any uncertain publish ends the wake, retains the action and counters, and disables further tool effects |
| Reply quota key | The replied-to thread quota uses the stored target permalink, independent of the confirmed new reply URL |
| Measured (tester account, 2026-11-12) | Account `views` is a per-day series (`values`, summed over the default two-day window); likes, replies, reposts, quotes and `followers_count` carry `total_value`. The account read stores the follower count as the latest own follower count (the API `followers_count` there is not what the page shows, see above) |

## Reply routing

`reply` names one `target`, the `p` ref of a stored post ([data.md](data.md), `seen_posts`); the author, permalink and API id come from that row and the dispatcher picks the path:

| Stored post | Path | Guard proof required |
|---|---|---|
| Has a `media_id` (an API read: `find_posts` `search` or `profile`, `read_post`, `read_replies`, the inbox) | API | The ref is a `p` ref this wake issued, through a read, the inbox or the open reply candidates (`unknown_target`) |
| Has none (a browser read: `find_posts` `feed` or `topic`) | Browser | Same; counts against `limits.browser_replies_per_day` |

A post first read by the browser and later by the API gets its `media_id` filled and takes the API path from then on. The API has no permalink-to-id resolver, which is why the id is stored at read time. Other reply limits ([guards.md](guards.md)) apply to both paths.

## Conversation inbox

Replies to the account's own actions wait for an answer; code lists them, the model does not have to find them. The read is `GET /{id}/replies` for each of the account's confirmed live posts and replies of the last 72 hours (at most 15, newest first) and `GET /{user}/mentions`, then one `GET /{reply}/replies` per candidate (at most 12) to see whether the account already answered under it. It never uses the browser and makes at most 28 API calls.

| Rule | Fact |
|---|---|
| Kept | A reply or mention with an id, a permalink and a timestamp from the last 72 hours, newest first, at most 10 |
| Skipped | The account's own usernames, `do_not_contact` authors, a reply a stored reply action targets, a reply the account answered under, a reply without a timestamp |
| Stop requests | A stop phrase in an inbox reply marks the author do-not-contact before the list is built ([guards.md](guards.md)) |
| Reply routing | Each kept item is stored as a post with a `p` ref, so `reply` with `target` = ref passes `unknown_target` and takes the API path: no `browser_replies_per_day` use |
| When | At the start of every agent wake, before the first model request, and by the scheduler between wakes ([wake.md](wake.md)) |

## Token lifecycle

| Step | Fact |
|---|---|
| Refresh | `refresh_access_token` (`th_refresh_token`) when within 7 days of expiry or the expiry is unknown (first run, from the environment seed) |
| Storage | The refreshed token and expiry are sealed with AES-256-GCM into the `secrets` table. The key is the base64 `THREADZ_SECRET_KEY`, held only in the environment. The stored token wins over the environment seed |
| Failure | A failed refresh queues one owner notice per day and keeps the current token. The refreshed token joins the redactor before any use |

## HTTP surface (`internal/api`)

One HTTP server of the harness process for Meta's calls: loopback only behind the Cloudflare tunnel (`run --api-listen`, public origin `--api-url`). Each route is logged as `api.request.finished` with the route as operation. Limit: burst 60, refilling 1 per second per client (shared limiter, client address as in [operations.md](operations.md)); 256 KiB bodies; fixed timeouts. Pending links and states live in memory, so a restart voids them.

| Route | Behaviour |
|---|---|
| `GET /threads/oauth/start?k=` | `k` is a one-time link (15 minutes) the owner receives over Telegram from `ctl threads connect`. Redirects to the consent page with the eight scopes and a random `state` valid once for 10 minutes |
| `GET /threads/oauth/callback` | Refuses an unknown, reused or expired `state`; exchanges the code for a short-lived then a long-lived token; refuses a token whose user id is not the agent's `THREADS_USER_ID`; stores it through the sealed slot |
| `GET /threads/webhooks` | Meta's handshake: answers `hub.challenge` only when `hub.verify_token` equals `THREADS_WEBHOOK_VERIFY_TOKEN` |
| `POST /threads/webhooks` | Requires a valid `X-Hub-Signature-256` (HMAC-SHA256 of the body with `THREADS_APP_SECRET`); anything else is 403 |
| `GET /privacy`, `GET /data-deletion` | Static pages the Meta app requires |

Webhook delivery has top-level `app_id`, `topic`, `target_id` (the agent's app-scoped `THREADS_USER_ID`), `time`, `subscription_id` and one `values` object (`values.field` is `replies` or `mentions`; `values.value.id` is the media id). The decoder reads only `target_id`, `values.field`, `values.value.id` and `values.value.username`; text and every other field are never decoded, stored or logged. A `replies` or `mentions` event from another user records only its field and id in `webhook_events` (30 days) and is a wake condition ([wake.md](wake.md)). A redelivered event does nothing.

## Browser (`agent-browser`)

| Item | Fact |
|---|---|
| Client | Pinned `vercel-labs/agent-browser 0.42.0` behind a harness-owned client with fixed verbs only: no `eval`, `batch`, CDP attach, cookie, storage, credential or generic browser command |
| Isolation | Per-agent session, daemon namespace and operator profile; bounded JSON output, context deadlines, process-group kill, owned-daemon cleanup; reads a compact noninteractive accessibility snapshot |
| Lifetime | At most one browser per wake, started on the first browser read, only configured-origin navigation, closed on every exit. A close failure after the outcome is decided is a cleanup failure: one close retry, lease release, owned-daemon kill, `wake.cleanup.failed`, no change to `end_reason` |
| Feed parser | Reads roles, nesting, link URLs and visible text. Threads renders navigation and column body on the client, so a read retries the snapshot up to 8 s (150 ms apart, inside the per-operation timeout) while the structure is absent; a rendered, logged-out or captcha page resolves on its first snapshot. Still absent at the end: `unknown_page` or `logged_out`. A feed or topic page renders only its first 3 to 5 posts (a `status "Loading..."` follows them) and loads more on scroll while dropping the posts scrolled past (measured 2026-11-13: 5, 10, 11, 8, 6 post links after 0 to 4 scrolls of 1500 px), so those reads scroll up to 6 times within the same 8 s and keep every post seen, stopping at 20 posts or a scroll that adds none. A post block's author and permalink links may sit inside plain `generic` wrappers. Unknown pages return operation-specific safe errors without guessed selectors |
| Logged out or captcha | Pauses only the selected running agent (killed stays killed), ends its wake, queues a static owner notice (retained locally when Telegram is off). The `BrowserStopped` seam accepts only `logged_out` and `captcha` plus an internal event ID and deduplicates the notice |
| Session check | Once a day (the clock starts at process start) the scheduler opens the browser, reads the feed once and closes it. A logged-out or captcha page pauses the agent and sends the owner one notice a day that says how to send cookies ([Cookie login](#cookie-login)). Skipped while a cookie import holds the profile and while the agent is not running |
| Graceful close | Every exit of a wake closes the agent-browser session, including cancellation by service stop (SIGTERM) and errors; the unit uses `KillMode=mixed` so systemd signals only the harness first and leaves Chrome and the daemon to that close |
| What ends the web session | Threads invalidates it on its own schedule: a password change, a logout elsewhere, a security check or a long idle period. Chrome refreshes the cookies in the profile itself; the harness has no rotation or refresh for the web session, unlike the API token (see Token lifecycle). Killing Chrome mid-write can corrupt the profile's cookie store, which reads as logged out. The repair is a cookie import ([Cookie login](#cookie-login)) |
| Trending now and topic pages (measured 2026-11-13, logged-in profile) | The home snapshot carries the panel outside the `Column body` region: a `StaticText` "Trending now", then one `link` per topic to `/search?q=<topic>&serp_type=trends&trend_fbid=<id>` whose text children are title, one-line summary, "·" and a post count; `find_posts` source `feed` takes the panel from the same snapshot as the feed (no extra navigation) and returns at most 10 items. A topic page and the search page (`/search?q=&serp_type=default`) render posts in the same post blocks as the feed, with extra hashtag links and media links inside a block, which the parser ignores. `find_posts` source `topic` reuses the wake's one browser, takes at most 3 reads per normal wake and returns at most 20 posts. Trends and topic posts live only in the wake's read sets |
| Post counters and age (measured 2026-11-13, fixtures `feed`, `topic`, `post-real`, `post-replies`) | A post block holds `button` nodes named `Like`, `Reply`, `Repost` or `Share`, each followed by its figure when there is one (`Like 6.5K`, `Reply 83`); Threads shows no figure at zero, so a counter button without one is 0 and an absent button is unknown. The permalink link carries the age as its name (`45 minutes ago`, `about an hour ago`) and as short text under it (`45m`). `Post` returns `likes`, `replies`, `reposts` and `age_minutes` as numbers, nil when the page did not show them. Figures parse as `83`, `1,234`, `1.234`, `6.5K`, `1,2K`, `1.2M`, `2 N`, `3 Tr` (K or N thousand, M or Tr million, B billion; with a suffix the separator is the decimal point, without one it groups three digits). A trending item's `posts` text (`2K posts`) also returns as `post_count`. Counter buttons are matched by their English labels; other interface languages leave the counters nil. A post found only through the API carries no age or counters from this path |
| Inbound text | Addressed replies and mentions enter `ObserveStopRequest` transiently, without storing text ([guards.md](guards.md)) |
| Not measured | Captcha flows, session portability, account survival, datacenter-IP behavior |

## Cookie login

The browser session is logged in by cookies the owner sends; the harness never types a password and keeps none. A logged-out wake or the daily session check pauses the agent and queues one notice a day, in the persona's language, that says to send `/cookie` (or run the ctl command below) and how to get the cookies. There is one import, with two entry points:

| Entry point | Fact |
|---|---|
| Telegram `/cookie <cookies>` | The owner's message is deleted with `deleteMessage` first, whether or not the import works. A message that looks like a cookie export (a JSON array naming `sessionid`, or text containing `sessionid=`) is handled the same without the prefix. With several agents the first word is the agent ID |
| `threadz ctl threads cookies --agent ID` | Reads the cookies from stdin, never from arguments, so they stay out of shell history and `ps`; the admin socket carries them to the running service, which owns the profile |

| Rule | Fact |
|---|---|
| Formats | A JSON array as a browser extension such as Cookie-Editor exports it (preferred: Export, JSON), or a `Cookie:` header string `name=value; name=value` copied from the Network tab of devtools on any logged-in threads.com request. A JSON object holding `"cookies"` also works |
| JSON mapping | `domain`, `path`, `secure`, `httpOnly`, `value` and `expirationDate` (Unix seconds) are kept; `sameSite` `lax`, `strict` and `no_restriction` map to Lax, Strict and None; `session` cookies carry no expiry. Only domains ending in `threads.com`, `threads.net` or `instagram.com` are kept; others are dropped silently |
| Header mapping | Each cookie is set on the origin's domain (`.threads.com`), path `/`, secure, expiring in 365 days, HttpOnly for `sessionid` and `ps_*`. Quoted values and octal escapes such as `\054` are decoded |
| Required | `sessionid` and `ds_user_id` must both be present, else the answer is "missing sessionid" and nothing is imported |
| Import | Through the pinned agent-browser CLI, the session a wake uses (same profile, same Chrome), with `cookies set`. The client method is write-only: it returns no cookie value, rejects names, values, domains and paths outside a strict charset and a value that starts with `-`. A page is loaded first because cookies written before any page loads do not survive |
| Verify and resume | After the write the feed is read with the usual logged-out and captcha detection, the browser is closed gracefully, and on success the agent resumes and the owner gets one confirmation. A still-logged-out or captcha result is answered once with its reason and the agent stays paused |
| Gating | An import holds the profile: no wake, phase or check starts meanwhile, and an import refuses to start while a wake runs or another import is under way |
| Secrets | The cookie text is never logged, stored in `owner_msgs` or any table, put in an error, a tool result or the model context. Owner notes pass the same secret screen tool arguments do (the configured secrets) and the cookie heuristic before they are stored; a refused note gets a one-line reply and is not saved |

## Metrics reads

`read_own_metrics` with `action_id` resolves the stored media id and returns that local action ID
with bounded nonnegative counters. It never attributes account aggregates to an action. `quotes`,
`shares` and `followers_count` are nullable and stay absent when missing.

For a reply it also returns `author_replied`: whether the replied-to post's author is among the
direct replies to our reply. That read is `GET /{id}/replies`, usernames only, with no text kept.
Only the first page is read, so a later answer is not seen, and an unreadable list leaves the field
absent.

Each metric sample stores views, likes, replies, reposts, quotes, shares, `author_replied`, and the
account follower count, which is read once per batch of claimed targets.
