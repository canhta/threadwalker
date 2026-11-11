# Verifying the ThreadWalker First-Draft Spec Against Primary Sources

> The first-draft spec (since removed from the repo) that this report checks no longer exists; the current spec is `docs/spec.md`. Below, "the draft spec" means that first draft.

- Access date for every URL in this document: **2026-11-11**.
- Document verified: the draft spec (the spec was not edited).
- Scope: 11 questions (the 9 original questions, plus question 10 on multi-account and question 11 on the "toxic" persona, which were added midway). Following the project owner's change of scope, questions 3 and 4 (provider/stack) only record whether the draft spec's claims are right or wrong, without digging into alternatives; the effort went into questions 1, 2, 10 and 11.

## Conventions and methodological limits

- **[Sourced]** = content found in a primary source, with a URL. **[Inferred]** = inference or calculation by the report's author, not the words of the source. **[Unverified]** = no primary source could be found.
- Most pages were read through a fetch tool that summarizes; this tool splits long quotations into several fragments. The quotations below are assembled from adjacent fragments. Only the Community Standards pages (Spam, Inauthentic Behavior, Bullying and Harassment, Hateful Conduct) and the two Reddit policy pages were loaded as raw text through a reader proxy (`r.jina.ai`) and quoted verbatim.
- Meta's docs return a Vietnamese version for some pages; the English quotations are taken from the `?locale=en_US` version.
- There was no Threads token and Bun was not installed on the machine, so **no command was run against the real Threads API or `bun:sqlite`**. Every conclusion about runtime behavior comes from reading docs, not from measurement.
- Commands that were actually run: `GET https://openrouter.ai/api/v1/models` (no key needed), the npm registry, the public GitHub API, and `curl` against the trend sources.
- Could not be read: `help.instagram.com` (fetch blocked; the mirror `facebook.com/help/instagram/...` was used), the full text of Section 3 of the Threads Terms of Use (the tool returned only a few excerpts), `reddit.com` (cannot be fetched from this machine).

---

## 1. Quick Summary Table

| # | Claim in the draft spec | Verdict | Source |
|---|---|---|---|
| 1 | `GET /keyword_search` with `q`, `search_type=TOP\|RECENT` exists | **Confirmed** (the default is `TOP`; the draft spec sets the default to `RECENT` on the client side, which is not wrong) | https://developers.facebook.com/docs/threads/keyword-search |
| 2 | Limit of 2,200 queries / 24h | **Confirmed**, but counted per *user* across all apps, and repeated queries for the same keyword still count | same URL |
| 3 | `keyword_search` returns public posts by strangers (implied by the draft spec, Phase 1 "1–2 hours") | **Wrong before App Review**: until approved, it only searches the posts of the authenticated user themselves | same URL |
| 4 | Fields `id,text,timestamp,username,permalink` | **Confirmed**; there is no like/reply count field | same URL |
| 5 | The draft spec's permission list is sufficient | **Wrong/incomplete**: missing `threads_read_replies` (required for GET replies/conversation) | https://developers.facebook.com/docs/threads/get-started |
| 6 | Reply (`reply_to_id`) to a stranger's post | **Allowed by the docs, with conditions**: requires `threads_keyword_search` or `threads_manage_mentions`, and the post must be "recently searched" | https://developers.facebook.com/docs/threads/retrieve-and-manage-replies/create-replies |
| 7 | `/{media-id}/conversation` reads comments under a stranger's post | **Unverified** (the docs are not explicit; the permission description leans toward "thread owned by the business app user"). Needs a spike | https://developers.facebook.com/docs/permissions |
| 8 | `/{media-id}/insights` for other people's posts; comparing "viral vs flop" using strangers' likes/replies | **Wrong according to the docs**: insights are only for the user's own posts; the media object and keyword search have no engagement count fields | https://developers.facebook.com/docs/threads/insights |
| 9 | Quota 250 posts / 1,000 replies / 24h; fields `quota_usage,config,reply_quota_usage,reply_config` | **Confirmed** (there are also quotas of 100 deletes and 500 location searches) | https://developers.facebook.com/docs/threads/overview |
| 10 | `Bun.sleep(15000)` then publish | **Wrong versus the recommendation**: the docs recommend about 30 seconds; there is a status endpoint to poll | https://developers.facebook.com/docs/threads/troubleshooting |
| 11 | Text cap of 450 characters | **More conservative than the real limit**: the limit is 500 | https://developers.facebook.com/docs/threads/posts |
| 12 | Token (the draft spec does not mention refresh) | **Missing**: a long-lived token lasts 60 days and must be refreshed | https://developers.facebook.com/docs/threads/get-started/long-lived-tokens |
| 13 | Base URL `graph.threads.net/v1.0` | **Still usable but outdated in its examples**: the docs now use `graph.threads.com`; both hosts are valid; the version is still `v1.0` | https://developers.facebook.com/docs/threads/changelog |
| 14 | Metrics `views,likes,replies,reposts,quotes` | **Confirmed** (plus `shares`; `views` and `shares` are marked "in development") | https://developers.facebook.com/docs/threads/insights |
| 15 | The design "drive-by replies to strangers + human-like persona + jitter + plugged link" complies with policy because it uses the official API | **Wrong reasoning**: using the official API does not exempt a design from the Community Standards / Developer Policies; many terms apply directly | Section 3 |
| 16 | OpenRouter `GET /api/v1/credits` | **Confirmed to exist**, but it needs a *management key* (that key cannot call completions) | https://openrouter.ai/docs/api/api-reference/credits/get-credits |
| 17 | `google/gemini-2.5-flash-lite`, `deepseek/deepseek-chat`, `google/gemini-2.5-pro` | **Confirmed** (still in the model list) | https://openrouter.ai/api/v1/models |
| 18 | `anthropic/claude-3.5-sonnet` | **Outdated**: no longer in the model list; the model page returns 404 | https://openrouter.ai/api/v1/models |
| 19 | `@openrouter/ai-sdk-provider` + Vercel AI SDK | **Confirmed** (provider 3.1.0 peer `ai ^7.0.0`; `ai` latest 7.0.169) | https://registry.npmjs.org/@openrouter/ai-sdk-provider |
| 20 | `generateObject` for every step | **Outdated**: deprecated since AI SDK 6, replaced by `generateText` + `output` | https://ai-sdk.dev/docs/migration-guides/migration-guide-6-0 |
| 21 | `sqlite-vec` running under `bun:sqlite` | **There is an official recipe; there is a macOS caveat**; Linux/Docker **not verified** by an actual run | https://alexgarcia.xyz/sqlite-vec/js.html |
| 22 | Drizzle supports `bun:sqlite` | **Confirmed**; vector columns / `vec0` virtual table: no native support found | https://orm.drizzle.team/docs/connect-bun-sqlite |
| 23 | "< 250MB RAM" | **Unverified** (could not be measured) | — |
| 24 | Google Trends VN RSS, Product Hunt RSS, HN API exist | **Confirmed** (all three returned HTTP 200 when tried) | Section 6 |
| 25 | Using Reddit as a trend source for a bot with a commercial purpose | **Wrong without permission**: requires prior approval and a separate agreement for commercial purposes | https://support.reddithelp.com/hc/en-us/articles/42728983564564-Responsible-Builder-Policy |
| 26 | Jina Reader | **Confirmed usable without a key**, limit 20 RPM | https://jina.ai/reader/ |
| 27 | "Thompson Sampling (80% exploit / 20% explore)" | **Wrong conceptually**: a fixed 80/20 split is epsilon-greedy, not Thompson sampling | Section 7 |
| 28 | A bandit that "gets smarter day by day" with 15–35 replies/day | **Statistically infeasible within a "this week" time frame** [Inferred, with calculation] | Section 7 |
| 29 | `max_budget_usd: 0.35`, "$0.18/day" | **$0.18 is not achievable with the pipeline as described; $0.35 is reached only in the cheapest scenario** [Inferred, with calculation] | Section 8 |
| 30 | Revenue attribution to each reply via Dub.co | **Feasible only at the link level, not at the reply level**, and requires a script on the destination site | https://dub.co/docs/conversions/quickstart |
| 31 | "7 core tables" | **Wrong**: it lists 9 tables | draft spec, lines 106–130 |
| 32 | (Question 10) One app, several Threads accounts, one token per account | **Confirmed in mechanism** (tokens are per user); limit on the number of testers: unverified | https://developers.facebook.com/docs/threads/get-started/get-access-tokens-and-permissions |
| 33 | (Question 10) Several accounts playing unrelated people, interacting with/boosting each other | **Very likely violates** Inauthentic Behavior and Authentic Identity Representation [Inferred from the quoted terms] | Section 11 |
| 34 | (Question 11) A "toxic" persona that automatically mocks/insults individuals | **Very likely violates** Bullying and Harassment (private adults are protected from "Targeted cursing", "Negative character or ability claims", "Expressions of contempt") [Inferred from the quoted terms] | Section 12 |
| 35 | (Question 11) The policy distinguishes attacking ideas from targeting people | **Yes**: "direct attacks against people — rather than concepts or institutions" | https://transparency.meta.com/policies/community-standards/hateful-conduct/ |
| 36 | ElizaOS and similar agents support Threads | **Not found**: the `elizaOS/eliza` repo has `plugin-x`, no Threads plugin | https://github.com/elizaOS/eliza |

---

## 2. Question 1 — Threads API

### 2.1 `keyword_search`

[Sourced] https://developers.facebook.com/docs/threads/keyword-search

- Endpoint: `GET https://graph.threads.com/v1.0/keyword_search`.
- Parameters: `q` (required), `search_type` (`TOP` by default, or `RECENT`), `search_mode` (`KEYWORD` by default, or `TAG`), `media_type`, `since`, `until`, `limit` (default 25, maximum 100), `author_username`.
- Example fields: `id,text,media_type,permalink,timestamp,username,has_replies,is_quote_post,is_reply`. "The `owner` field is excluded and will not be returned."
- Limits: "A user can send a maximum of 2,200 queries within a rolling 24-hour period." / "This limit applies to a user across apps and is not differentiated for different apps." / "Subsequent queries against the same keyword within this timeframe will also count against this limit." / "Queries which return no results do not count against this limit for the user."
- Before approval: "If your app has not been approved for the `threads_keyword_search` permission, the search will be performed only on posts owned by the authenticated user." / "After approval, public posts will be searchable."
- Sensitive keywords: "The API will return an empty array for any requests that include keywords that we have deemed sensitive or offensive."
- Interacting with searched posts: "You can interact with public Threads media that you have recently searched for." Actions include reply, quote, repost; "Additional permissions may be required".

Implications for the draft spec:

- [Sourced] The draft spec could use `since`/`until` to filter "posts > 6h" instead of filtering client-side.
- [Inferred] "Cache so the same call is not repeated" is the right direction, because repeated queries still count against the quota.
- [Inferred] Phase 1 ("1–2 hours", testing `keyword_search` on real posts) cannot be carried out before App Review: with Standard Access, the results are only the account's own posts.

### 2.2 App Review for `threads_keyword_search`

[Sourced] https://developers.facebook.com/docs/permissions

- Description: "allows a business app to search and fetch content with a specific keyword on behalf of a Threads user".
- Allowed usage: "Showing public content and the public content tree that a business app user searches using a specific keyword."
- Restriction: "using threads_keyword_search permission in a consumer app is strictly prohibited."
- Review requirements: a concrete example use case; a screencast covering the Threads OAuth login and account-linking flow, one keyword search of public content, and the results displayed in the app and in the native Threads app.

[Sourced] https://developers.facebook.com/docs/graph-api/overview/access-levels (Vietnamese version, quoted from the page's translation)

- Standard Access: only users with a role on the app can use it.
- Advanced Access: the app can request permissions from any user; it must pass Business Verification; "in some cases" each permission must pass App Review.

[Sourced] https://developers.facebook.com/docs/threads/get-started: "each permission must first be approved through the App Review process, and your app must be published." Testers can grant permissions at any time without App Review.

[Inferred] "Allowed usage" describes *displaying* public content to the user who searches; an automated bot that replies with promotion is not within that description. The review outcome for this use case cannot be known in advance. Review time: [Unverified] (the App Review page does not state it).

[Inferred] The model "OSS, 1 command `docker compose up -d`" contradicts reality: each self-hoster needs their own Meta app that has passed Business Verification and App Review (with a screencast), or must share the project owner's app (in which case the project owner is responsible for every instance).

### 2.3 Replying to posts by people who have not authorized the app

[Sourced] https://developers.facebook.com/docs/threads/retrieve-and-manage-replies/create-replies

- "`threads_manage_replies`: Required for creating replies with the `reply_to_id` parameter."
- "In addition, to reply to a thread, you must meet one of the following requirements:" / "You are the owner of the root Threads post." / "Your app has either the `threads_keyword_search` or the `threads_manage_mentions` permission."
- "If your app has not been approved for advanced access for the `threads_content_publish` permission, you can only post on behalf of Threads testers on your app."
- "Wait about 30 seconds before publishing to allow the Threads media pipeline to finish processing the upload."

Conclusion: the docs **confirm** that there is a path to reply to strangers' public posts (via `threads_keyword_search`, for "recently searched" posts). Points that remain unclear:

- [Unverified] Whether "Has the permission" at Standard Access (tester) is enough to reply to a stranger's post or whether Advanced Access is required. Since search at Standard Access returns only the account's own posts, it is highly likely that no stranger's post is in the "recently searched" state. Needs a spike.
- [Unverified] How long "recently" is. The draft spec sets a delay of 1.5–6 minutes, and it could be longer if the queue is long.

Restriction from the author's reply audience:

- [Sourced] https://developers.facebook.com/docs/threads/reply-management: `reply_control` has 5 levels: everyone, accounts you follow, mentioned only, parent post author only, followers only.
- [Sourced] https://developers.facebook.com/docs/threads/retrieve-and-manage-replies/replies-and-conversations: the field `reply_audience` "only appears on top-level posts and replies that you own".
- [Inferred] The app cannot read `reply_audience` of a stranger's post, so it cannot know in advance which posts lock replies; it must handle the error at container creation. The specific error code: [Unverified], needs a spike.

### 2.4 `/conversation` and `/insights` on other people's posts

`/conversation`:

- [Sourced] https://developers.facebook.com/docs/threads/retrieve-and-manage-replies/replies-and-conversations: "`GET /{threads-media-id}/conversation` returns all replies, regardless of the depth"; "This endpoint is only intended to be used on the root-level posts with replies." For `username` and `permalink`: "This only works for public users and your own user." The page does not say explicitly whether it can be called on media the caller does not own.
- [Sourced] https://developers.facebook.com/docs/threads/get-started: `threads_read_replies` "required for GET calls to reply endpoints". **The draft spec does not list this permission.**
- [Sourced] https://developers.facebook.com/docs/permissions, `threads_read_replies`: "allows a business app to read replies to a user's thread." Allowed usage: "Get replies to a thread owned by the business app user." / "It is not intended for any other purpose."
- [Sourced] same page, the allowed usage of `threads_keyword_search` mentions "the public content tree".
- Conclusion: **cannot be determined from the docs**. The description of `threads_read_replies` leans toward only the user's own threads; the phrase "public content tree" in `threads_keyword_search` suggests that the reply tree of a searched post might be readable. Needs a spike. If it cannot be read, both Tier 1 triage ("first 5 comments") and Viral Autopsy ("10 comments under the Viral post") lose their input.

`/insights` and engagement counts:

- [Sourced] https://developers.facebook.com/docs/threads/insights: the API "allows you to read the insights from users' own Threads posts and profiles." Media metrics: `views`, `likes`, `replies`, `reposts`, `quotes`, `shares`. "An empty array will be returned for `REPOST_FACADE` posts because they are posts made by other users." / "Returned metrics do not capture nested replies' metrics."
- [Sourced] https://developers.facebook.com/docs/permissions, `threads_manage_insights`: "Allow business apps to get insights for individual threads published by that profile."
- [Sourced] https://developers.facebook.com/docs/threads/threads-media: the media object's field list has no like count, reply count or views. On other people's posts: before approval "only posts created by a Threads tester are retrievable"; after approval "public posts created by other users will be retrievable".
- [Sourced] https://developers.facebook.com/docs/threads/threads-profiles (Profile Discovery, permission `threads_profile_discovery`): `profile_lookup` returns likes/quotes/reposts/views **accumulated over 7 days for the whole profile**, not per post; "Only returns public profiles with at least 100 followers."; with Standard Access it can only look up @meta, @threads, @instagram, @facebook; 1,000 requests / 24h.

Conclusion: **nowhere** in the docs are the like/reply counts of individual public posts by other people exposed. Implications:

- [Inferred] "Contrastive Viral Autopsy" has no way to identify a "flop post that nobody engaged with" using numbers. The only signals are the `TOP` versus `RECENT` ordering and the boolean `has_replies`.
- [Inferred] `threads_actions.likes_count` / `replies_count` can only be filled for the agent's own posts and replies. Whether insights on the agent's own *replies* work as they do on posts: [Unverified], needs a spike.

### 2.5 Publishing limits, wait times, text length

- [Sourced] https://developers.facebook.com/docs/threads/overview: 250 posts / 24h (a carousel counts as 1), 1,000 replies / 24h, 100 deletes, 500 location searches. Call rate limit: "Calls within 24 hours = 4800 * Number of Impressions", with a minimum counted impressions of 10.
- [Sourced] https://developers.facebook.com/docs/threads/troubleshooting: `GET /{threads-user-id}/threads_publishing_limit` with `quota_usage`, `config`, `reply_quota_usage`, `reply_config`, `delete_quota_usage`, `delete_config`, `location_search_quota_usage`, `location_search_config`.
- [Sourced] same page, container status: `GET /{threads-container-id}?fields=status,error_message`; values `EXPIRED`, `ERROR`, `FINISHED`, `IN_PROGRESS`, `PUBLISHED`. "We recommend querying a container's status once per minute, for no more than 5 minutes."
- [Sourced] https://developers.facebook.com/docs/threads/posts: "It is recommended to wait on average 30 seconds before publishing a Threads media container." / "Text posts are limited to 500 characters." Emoji are counted by their UTF-8 byte count.
- [Sourced] same page: at most 5 distinct links per post (error `THREADS_API__LINK_LIMIT_EXCEEDED` since 2025-12-22).

Conclusion: a hard 15 seconds is below the recommendation (30 seconds). Poll the status until `FINISHED`, then publish. `z.string().max(450)` is safe, but Vietnamese with diacritics and emoji are counted by bytes: [Inferred] the count must use the unit the API uses; how Vietnamese characters are counted: [Unverified], needs a spike.

### 2.6 Token

[Sourced] https://developers.facebook.com/docs/threads/get-started/long-lived-tokens

- Short-lived: "valid for one hour." Long-lived: "valid for 60 days."
- Refresh: `GET /refresh_access_token` with `grant_type=th_refresh_token`; the token must be "at least 24 hours old but have not expired".
- Permissions granted by a user with a private profile are "valid for 90 days."

[Inferred] The draft spec has no refresh job; the agent would die silently after 60 days. The token and its expiry need to be stored in the DB and refreshed periodically.

[Sourced] https://developers.facebook.com/terms/?locale=en_US §7.e.iii: "We may suspend or end your App's access to any Platform APIs, permissions, or features that your App has not used or accessed within a 28-day period with or without notice to you." [Inferred] A prolonged `SLEEPING`/`SURVIVAL` mode could cause loss of access.

### 2.7 Base URL and version

- [Sourced] https://developers.facebook.com/docs/threads/changelog, entry 2025-06-06: "The Threads API can be accessed by either graph.threads.com or graph.threads.net."
- [Sourced] All current examples use `https://graph.threads.com/v1.0/`. The 2025–2026 changelog lists no new version.
- Conclusion: `graph.threads.net/v1.0` is still valid; use `graph.threads.com` to match the docs.

### 2.8 Related API features that the draft spec does not use

- [Sourced] https://developers.facebook.com/docs/threads/threads-mentions: `GET /{threads-user-id}/mentions` (permission `threads_manage_mentions`) "Returns a list of public Threads media objects in which a Threads profile has been tagged by another Threads profile." Before approval: "the API returns only mentions made by a Threads tester on the app."
- [Sourced] https://developers.facebook.com/docs/threads/webhooks: webhooks `replies` (replies on the user's posts), `mentions`, `publish`, `delete`. "If the app permissions don't have an access level of advanced access, the app won't receive webhook notifications."
- [Inferred] Replying to people who mentioned the agent or replied to its posts is an *invited* interaction; this is the lowest policy-risk area, and the draft spec currently skips it (it uses 15-minute polling instead of webhooks).

---

## 3. Question 2 — Meta Policy

### 3.1 Which terms apply

[Sourced] Threads Terms of Use (mirror, updated 2025-05-28): https://www.facebook.com/help/instagram/769983657850450?locale=en_US

- The Threads Terms "supplement and amend the Instagram Terms of Use and the Meta Community Standards which are incorporated into these Terms by reference."
- §4(a)(ii): "all content that you upload or share must comply with the Meta Community Standards."
- §3(a): "You agree that you shall not, nor shall you cause or permit any third party to do, or attempt to do" … "(i) exploit the Threads Service for any commercial purpose" … "(iv) use any robot, spider, crawlers, scraper or other automatic device, process, software or queries that intercepts, "mines", scrapes, extracts or otherwise accesses the Threads Service".
- §2(b): "You are solely responsible for any activity on your profile".
- **Verification limit:** the fetch tool did not return the full text of §3(a), so it is not known whether items (i) and (iv) carry an exception of the "except as expressly permitted" kind. The two excerpts above are serious enough (commercial purpose, automated access) that the project owner must read the full text personally before deciding. It cannot be concluded that the draft spec violates these two items.

[Sourced] Instagram Terms of Use (mirror): https://www.facebook.com/help/instagram/581066165581870?locale=en_US

- "You can't impersonate others or provide inaccurate information. You don't have to disclose your identity on Instagram, but you must provide us with accurate and up-to-date information (including registration information), which may include providing personal data. Also, you may not impersonate someone or something you aren't, and you can't create an account for someone else unless you have their express permission."
- "You can't do anything unlawful, misleading or fraudulent or for an illegal or unauthorised purpose."
- "You can't attempt to create accounts or access or collect information in unauthorised ways. This includes creating accounts or accessing or collecting information in an automated way without our express permission, regardless of whether such automated access or collection is undertaken while logged in to an Instagram account."
- The page states: "We're updating our Terms of Use. These updates come into effect on 20 January 2027." Content of the update: [Unverified].

[Sourced] Meta Platform Terms: https://developers.facebook.com/terms/?locale=en_US

- §1.b: "you agree to these Platform Terms ("Terms"), as well as all other applicable terms and policies."
- §3.a.v: "Processing Platform Data without valid User consent in order to build or augment user profiles for any purpose."
- §3.a.iii: "Processing Platform Data to perform, facilitate, or provide tools for surveillance."
- §3.a.viii: "Processing Platform Data for purposes other than the applicable permitted purposes set forth in Meta's Developer Docs."
- §3.d.i.1: "Update or delete Platform Data promptly after receiving a request from us or the User to do so".
- §7.e.i.2: enforcement when "You or your App has violated or may have violated these Terms or any other applicable terms or policies"; §7.e.ii: "We may take enforcement action at any time, including while we investigate your App(s), with or without notice to you."

[Sourced] Developer Policies: https://developers.facebook.com/devpolicy/?locale=en_US

- §1.4: "Don't confuse, deceive, defraud, mislead, spam or surprise anyone."
- §1.7: "Obtain consent from people before publishing content or taking any other action on their behalf."
- §2.7: "Don't participate in any program that promotes or facilitates the purchase, sale, or exchange of "Likes", "Shares"" (the sentence is cut off in the fetch source).
- §3.5: "If we determine that your App is creating a negative experience, we may require you to comply with one or more App Development Guidelines."
- The page has no section specific to Threads.

### 3.2 Spam (Community Standards)

[Sourced, verbatim] https://transparency.meta.com/policies/community-standards/spam/

- Rationale: "We do not allow content that is designed to deceive, mislead, or overwhelm users in order to artificially increase viewership. This content detracts from people's ability to engage authentically on our platforms and can threaten the security, stability and usability of our services."
- "We do not allow: Posting, sharing, engaging with content or creating accounts, Groups, Pages, Events or other assets, either manually or automatically, at very high frequencies."
- "We may place restrictions on accounts that are acting at lower frequencies when other indicators of Spam (e.g., posting repetitive content) or signals of inauthenticity are present."
- "Attempting to or successfully selling, buying, or exchanging for engagement, such as likes, shares, views, follows, clicks, use of specific hashtags, etc."
- "Misleading Links: Content containing a link that promises one type of content but delivers something substantially different."
- "Notwithstanding the above, we do not prohibit: Cross promotion that is not triggered by payment to a third party".

### 3.3 Identity and inauthentic behavior

[Sourced] https://transparency.meta.com/policies/community-standards/authentic-identity-representation/

- "we do not allow for the creation of accounts or profiles that are created or used to deceive others."
- Applies to "Facebook, Instagram, and Threads accounts": "Engage in identity misrepresentation to mislead or deceive others, evade enforcement, or violate our Community Standards." Factors considered: "Repeated or significant changes to identity details, such as name or age" / "Misleading profile information, such as bio details and profile location" / "Using stock imagery".
- Applies to Facebook only: "Create or maintain multiple Facebook accounts"; "Create an account that represents a non-human entity, such as a business, pet, or fictional character".

[Sourced, verbatim] https://transparency.meta.com/policies/community-standards/inauthentic-behavior/ — see Section 11.

### 3.4 AI labeling

- [Sourced] https://developers.facebook.com/docs/threads/posts: "To disclose that a post contains AI-generated content, set `is_made_with_ai=true` when creating the media container". / "The published post displays the AI info label, as it does when you select the AI-label option in the Threads composer." / "This parameter supports text, image, video, and carousel posts." / "It defaults to `false`." / "Omitting it or setting it to `false` does not add an AI self-disclosure and does not prevent automatic AI labeling."
- [Sourced] https://about.fb.com/news/2024/04/metas-approach-to-labeling-ai-generated-content-and-manipulated-media/: the label applies to "a wider range of video, audio and image content"; the post does not mention text. Scope: "manipulated media on Facebook, Instagram and Threads".
- Conclusion: the API **does** have a self-disclosure mechanism for AI, including for text posts. Whether self-disclosure is **mandatory** for plain text posts: [Unverified] (the Meta sources found state it as mandatory only for realistic video/audio/images). A label specifically for automated/bot accounts on Threads: [Unverified], none found.
- The draft spec does not use `is_made_with_ai` anywhere.

### 3.5 Assessment of the draft spec's design

This section is **[Inferred]** from the quoted terms.

| Design element | Relevant terms | Assessment |
|---|---|---|
| Automated replies to posts by strangers who were never in contact | No term found that specifically prohibits replying via the API; the keyword search docs allow "interact with public Threads media that you have recently searched for". But: Spam ("either manually or automatically, at very high frequencies"; "lower frequencies when other indicators of Spam… or signals of inauthenticity are present"); Bullying and Harassment Tier 1: "Unwanted contact that is: Repeated, OR … Is directed at a large number of individuals with no prior solicitation." | Not absolutely prohibited. The risk depends on frequency, degree of repetition and the recipients' reaction (report/block). 15–35 replies/day is far below the 1,000 quota, but the "lower frequencies" clause allows restrictions even at low frequency. |
| Persona that "communicates naturally like a real person" | Instagram Terms: "you may not impersonate someone or something you aren't"; "You can't do anything unlawful, misleading or fraudulent"; Authentic Identity: "identity misrepresentation to mislead or deceive others"; Developer Policies §1.4 | The sample bio mentions "at night the agent wanders Threads on its own" and there is a public dashboard, so there is partial disclosure. But no individual reply carries any sign of AI, and the stated goal is to make readers believe it is a real person. It could be considered misleading. |
| Jitter of 1.5–6 minutes as "simulated reading time" | No term names jitter/delay. Indirectly relevant: Spam ("signals of inauthenticity"), Inauthentic Behavior ("operators use adversarial methods to evade detection or appear authentic" — part of the CIB definition, which requires that "false identities are central to the operation") | Delay to spread load is normal. A delay whose stated purpose in the draft spec is to "simulate" a human reveals an intent to conceal the automation; this is adverse evidence if reviewed. The delay itself violates no term found. |
| App link plug (self-reply under its own post; suggesting the app in replies to other people's posts, ≤2 times/day) | Spam: only prohibits deceptive/misleading links and the buying/selling of engagement; "we do not prohibit: Cross promotion that is not triggered by payment to a third party". Threads Terms §3(a)(i) "exploit the Threads Service for any commercial purpose" (full text not read) | A real link to a real app, not deceptive: does not match the prohibited items of the Spam policy. Item §3(a)(i) of the Threads Terms needs the full text to be read. |
| No AI label | `is_made_with_ai` exists and supports text; whether it is mandatory for text is unverified | Turning on `is_made_with_ai=true` is the cheapest way to reduce risk; it directly contradicts the draft spec's goal of being "like a real person". |
| Public dashboard showing strangers' @username, rejection reasons ("Content selling a 'lùa gà' course"), and the `relationship_graph.vibe_summary` table | Platform Terms §3.a.v ("build or augment user profiles" without consent), §3.a.viii, §3.d.i.1 (delete on request) | This is a risk that the draft spec does not mention at all. Storing "vibe" profiles of users who did not grant permission and publicly broadcasting named negative remarks is the hardest part of the whole design to defend. |
| The warning "CAUTION: only use the Official API… to avoid account lockout and policy violations" | Platform Terms §1.b; Threads Terms §4(a)(ii) | The draft spec's reasoning is wrong: using the official API avoids scraping violations, but content and behavior are still subject to the Community Standards and Developer Policies. |

---

## 4. Question 3 — OpenRouter and AI SDK (brief notes)

- **`GET /api/v1/credits`**: [Sourced] https://openrouter.ai/docs/api/api-reference/credits/get-credits — exists; "Management key required"; returns `{"data": {"total_credits": …, "total_usage": …}}`. Calling without a key returns 401 (tried). [Inferred] Balance = `total_credits − total_usage`.
- [Sourced] https://openrouter.ai/docs/guides/overview/auth/management-api-keys — a management key "cannot be used to make API calls to OpenRouter's completion endpoints". [Inferred] The draft spec needs 2 keys, or should use `GET /api/v1/key` with a regular key ([Sourced] https://openrouter.ai/docs/api/reference/limits: returns `limit`, `limit_remaining`, `usage`, `usage_daily`).
- **Model IDs and prices** — [Sourced] `GET https://openrouter.ai/api/v1/models` (469 models, called directly), prices in USD per 1M input / output tokens:
  - `google/gemini-2.5-flash-lite`: exists, $0.10 / $0.40.
  - `deepseek/deepseek-chat`: exists (named "DeepSeek V3"), $0.32 / $0.89.
  - `google/gemini-2.5-pro`: exists, $1.25 / $10.00.
  - `anthropic/claude-3.5-sonnet`: **not in the list**; https://openrouter.ai/anthropic/claude-3.5-sonnet returns 404. Date and reason for removal: [Unverified]. The Sonnet models currently listed: `anthropic/claude-sonnet-5.5` ($2 / $10), `anthropic/claude-sonnet-5` ($2 / $10), `anthropic/claude-sonnet-4.6` ($3 / $15), `anthropic/claude-sonnet-4.5` ($3 / $15).
  - None of the other 3 models has an `expiration_date`.
- **AI SDK**: [Sourced] npm registry — `ai` latest 7.0.169; `@openrouter/ai-sdk-provider` latest 3.1.0, peer `ai ^7.0.0`: compatible.
- **`generateObject`**: [Sourced] https://ai-sdk.dev/docs/migration-guides/migration-guide-6-0 — "`generateObject` and `streamObject` have been deprecated." / "They will be removed in a future version." / "Use `generateText` and `streamText` with an `output` setting instead." The 7.0 migration guide (https://ai-sdk.dev/docs/migration-guides/migration-guide-7-0) still mentions `generateObject` in its function list, i.e. it has not been removed in v7. Conclusion: it still works, but it is no longer the recommended API.

## 5. Question 4 — Stack (brief notes)

- **`sqlite-vec` + `bun:sqlite`**: [Sourced] https://alexgarcia.xyz/sqlite-vec/js.html has a `sqliteVec.load(db)` recipe for Bun, with the note "MacOS *might* have to do this, as the builtin SQLite library on MacOS doesn't allow extensions" and a call to `Database.setCustomSQLite(...)`.
- [Sourced] https://bun.com/docs/runtime/sqlite: "By default, macOS ships with Apple's proprietary build of SQLite, which doesn't support extensions."; `Database.setCustomSQLite(path)` must be called "before creating any `Database` instances". Outside macOS, SQLite is statically linked and `setCustomSQLite` does nothing.
- Linux/Docker: [Unverified] by an actual run (the machine has no Bun). A third-party issue reports that `bun:sqlite` could not load the extension (https://github.com/Soul-Brews-Studio/arra-oracle-v3/issues/3046, a secondary source, not cross-checked). Put it on the spike list.
- `sqlite-vec` latest is 0.1.9 (npm), i.e. not yet 1.0.
- **Drizzle**: [Sourced] https://orm.drizzle.team/docs/connect-bun-sqlite — "Drizzle ORM natively supports `bun:sqlite` module." Vector columns: [Sourced] https://alexgarcia.xyz/sqlite-vec/features/knn.html — vectors live in a `vec0` virtual table or a plain BLOB column. Native Drizzle support for `vec0`: not found; [Inferred] `customType` (BLOB) or raw SQL must be used.
- **"< 250MB RAM"**: [Unverified]. No official measurement was found and none could be taken. Note that the draft spec's diagram says "VPS (2GB RAM)" while the goal says "< 250MB".

## 6. Question 5 — Trend sources

| Source | Exists? | Terms |
|---|---|---|
| Google Trends VN RSS | [Sourced, tried] `https://trends.google.com/trending/rss?geo=VN` returns HTTP 200, `text/xml` | Official documentation and terms of use for this feed: [Unverified] |
| Product Hunt RSS | [Sourced, tried] `https://www.producthunt.com/feed` returns HTTP 200, Atom | [Sourced] https://api.producthunt.com/v2/docs: "The Product Hunt API must not be used for commercial purposes." The page does not mention RSS; whether RSS is subject to these terms: [Unverified] |
| Hacker News API | [Sourced, tried] `https://hacker-news.firebaseio.com/v0/topstories.json` returns HTTP 200 | [Sourced] https://github.com/HackerNews/API: "There is currently no rate limit." |
| Reddit | [Tried] `https://www.reddit.com/r/SideProject/top.json` could not be reached from this machine (unclear whether blocked or a network issue) | See below |
| Jina Reader | [Sourced, tried] `https://r.jina.ai/<url>` returns HTTP 200 without a key | [Sourced] https://jina.ai/reader/: 20 RPM without a key; 500 RPM with a key; "Every new API key comes with 10M free tokens!" |

Reddit, [Sourced, verbatim] https://support.reddithelp.com/hc/en-us/articles/42728983564564-Responsible-Builder-Policy:

- "Approval is required: You must request access and get explicit approval before accessing any Reddit data through our API, and you must agree to comply with all applicable terms."
- "If you'd like to use Reddit data for commercial purposes, you'll need to get explicit written approval."
- "You must not sell, license, share, or otherwise commercialize Reddit data without express written approval. This extends to commercial and non-commercial mining, scraping, or using data for purposes like ads targeting or to train machine learning or AI models."
- "Apps must not engage in spamming activity through automated posts, comments, or direct messages."

[Sourced, verbatim] https://redditinc.com/policies/data-api-terms ("Effective June 19, 2023. Last Revised July 20, 2026"):

- "If you are interested in using the Data APIs for commercial purposes, research in excess of rate limits, or for any use that is not expressly permitted under the Data API Terms, then you will need to enter into a separate agreement with Reddit."
- "you will not misrepresent or mask either the user agent or OAuth identity when using the Data APIs."

[Inferred] An agent whose goal is to drive traffic to a paid app has a commercial purpose; using Reddit as a source requires approval and a separate agreement. Reddit should be dropped from the initial scope. The other three sources are technically usable; the terms of two of them are unclear.

---

## 7. Question 6 — The self-learning loop

### 7.1 Thompson sampling and "80/20" are not the same thing

- [Sourced] Russo et al., "A Tutorial on Thompson Sampling", https://arxiv.org/abs/1707.02038: "Thompson sampling is an algorithm for online decision problems where actions are taken sequentially", in which one must balance exploiting existing knowledge against gathering new information.
- [Inferred, standard knowledge] Thompson sampling: on each round, draw a sample from each arm's `Beta(alpha, beta)` and choose the arm with the largest sample. The explore rate is not fixed; it decreases on its own as the posterior narrows. A fixed split of "80% pick the currently winning formula / 20% try something new" is epsilon-greedy with ε = 0.2. The draft spec describes both in the same paragraph (lines 279–281); the implementer must choose one.
- [Inferred] In addition, the draft spec's 20% "explore" is not choosing the arm with the least data but *creating a new arm* from `viral_autopsies`; the number of arms K keeps growing, making the data per arm ever thinner.

### 7.2 How many days it takes to tell the arms apart

This entire section is **[Inferred / calculation]**.

Assumptions: binary reward ("hit" or "flop", exactly as in `alpha_wins` / `beta_losses`); 25 replies/day (the middle of the 15–35 range); K arms split evenly; a two-proportion test, α = 0.05 two-sided, power 80%, so `n per arm ≈ 7.84 × (p1(1−p1) + p2(1−p2)) / (p1 − p2)²`.

| "Hit" rate of the worse arm → the better arm | n per arm | K = 3 (≈8.3 pulls/arm/day) | K = 5 (5 pulls/arm/day) |
|---|---|---|---|
| 10% → 20% | ≈ 196 | ≈ 24 days | ≈ 39 days |
| 10% → 15% | ≈ 682 | ≈ 82 days | ≈ 136 days |

Notes:

- Thompson sampling concentrates pulls on the leading arm, so it finds the best arm faster than an even split, but the order of magnitude does not change: a few weeks to a few months, not "day by day".
- The draft spec wants to react on a "this week" time frame. In 7 days with K = 5, each arm has ≈ 35 pulls; at p = 0.15 the standard error is ≈ 0.06 and the 95% confidence interval is ≈ ±0.12. It cannot tell 10% from 20%.
- The rule "5 flops in a row → drop the habit": for a good arm with a 20% hit rate, the probability of 5 consecutive flops is 0.8⁵ ≈ 33%. This rule would eliminate a good arm about one third of the time.
- If a continuous reward (number of likes) is used instead of binary: social media engagement is heavy-tailed, the sample mean is dominated by a few outlier posts, and even more samples are needed. The draft spec does not define the "hit" threshold.
- The biggest noise variable is not in the model: a reply's engagement depends mainly on the original post's reach and on when the comment lands, not on the writing formula. The draft spec does not store the original post's reach (and per Section 2.4, the API does not provide it).
- `threads_actions` has no `strategy_id` column, so the bandit cannot be updated from outcomes (see Section 9).

### 7.3 Published evidence

- **LLMs predicting engagement**: [Sourced] El-amrany et al., "Reddit-V", RANLP 2025, https://aclanthology.org/2025.ranlp-1.41: "Our results show that zero-shot LLMs perform poorly". The search results also cited arXiv 2510.08481 (LLMs are unreliable for direct numeric prediction) and arXiv 2505.03769 (humans predict popularity only slightly better than guessing); these two papers were only seen through search summaries, **the originals were not read**.
- No study was found on LLMs predicting the engagement of *the very post they wrote* on Threads: [Unverified].
- **LLMs self-detecting "AI" prose**: [Sourced] Ji et al., "I know myself better, but not really greatly", https://arxiv.org/abs/2502.12743: "self-detection (LLMs identifying their own outputs) consistently outperforms cross-detection" but "both remain suboptimal"; explanations for errors include "inaccurate features, hallucinations, and flawed reasoning".
- [Inferred] `predicted_engagement_score` 1–10 with a threshold of |Delta| ≥ 4, and `sounds_like_ai_slop` / `human_naturalness_score ≥ 8`, all rely on abilities that the studies above find weak. The banned-phrase list in `character.json`, checked by string matching, would be more reliable than self-scoring. In addition, `criticAndVerifyGate` both selects, edits (`final_polished_text`), and scores its own edited version; there is no independent check after the edit.

---

## 8. Question 7 — Economics

### 8.1 LLM cost per day

This entire section is **[Inferred / calculation]** based on the prices in Section 4. The draft spec does not state token counts or the number of candidates, so every figure below is an assumption.

Assumed tokens per call: triage 1,200 in / 120 out; composer 3,000 in / 400 out (3 variants); critic 1,500 in / 250 out; autopsy 2,500 in / 400 out; prediction-error analysis 2,000 in / 300 out; dreaming 6,000 in / 1,500 out. Not counted: reasoning tokens, retries, embeddings (the draft spec names no embedding model) and credit top-up fees.

| Step | Scenario A — economical | Scenario B — per the draft spec | Scenario C — critic/autopsy on an expensive model |
|---|---|---|---|
| Triage (`gemini-2.5-flash-lite`) | 150 posts: $0.025 | 300 posts: $0.050 | 300 posts: $0.050 |
| Composer | 22 calls of `gemini-2.5-pro`: $0.17 | 42 calls of `claude-sonnet-5.5`: $0.42 | 42 calls of `claude-sonnet-5.5`: $0.42 |
| Critic | flash-lite: $0.006 | flash-lite: $0.011 | sonnet-5.5: $0.231 |
| Autopsy (10/day) | flash-lite: $0.004 | flash-lite: $0.004 | sonnet-5.5: $0.090 |
| Prediction-error analysis (5/day) | skipped | skipped | sonnet-5.5: $0.035 |
| Dreaming | $0.023 | $0.027 | $0.027 |
| **Total / day** | **≈ $0.23** | **≈ $0.51** | **≈ $0.85** |

- 42 composer calls = 20 target replies + 2 posts, doubled because the critic rejects about half (assumption; the draft spec sets 3 fairly harsh blocking conditions). Scenario A assumes no rejections at all.
- If `claude-sonnet-4.6` ($3 / $15) is used for the composer: $0.63 instead of $0.42.
- Compared with the draft spec: "$0.18/day" is lower even than the cheapest scenario. `max_budget_usd: 0.35` is only reachable in scenario A. The scenario that follows the draft spec closely (B) exceeds the budget by about 45%.
- The draft spec counts only the OpenRouter balance in "runway". VPS cost is mentioned in the goals (line 14) but is not in `ledger_metrics` or the runway formula. VPS price: [Unverified], not looked up.
- Order of magnitude: $7–26/month in LLM spend. This is a small number; the real economic risk is on the revenue side (the draft spec has no estimate), not the cost side.

### 8.2 Attribution via Dub.co when no link is placed in the post

- [Sourced] https://dub.co/docs/conversions/quickstart: the Dub Analytics script on the destination site "detects the `dub_id` query parameter and storing it as a first-party cookie"; sales go through the Stripe/Shopify integration or `POST /track/sale`; server-side tracking: "This feature is only available on Business plans and above."
- [Sourced] Threads changelog 2025-07-02: the `clicks` metric was added to Insights (https://developers.facebook.com/docs/threads/changelog). Level (user or media) and how to use it: [Unverified].
- [Inferred] Mode 1 (a link in a reply the agent posts itself under its own post): clicks can be attributed to that *post* if each post uses its own short link. The draft spec has only one `tracking_url` per app, so attribution is possible only at the app level.
- [Inferred] Mode 2 ("suggest the App name or invite people to view the profile"): there is no link in the reply, users go through the profile or search on their own; it cannot be attributed to a specific reply. Most of the 15–35 replies/day are of this kind, so `revenue_attributed_usd` and the "self-sustaining" loop are almost unmeasurable at the level of individual actions.
- [Inferred] Conversion requires the Dub script on each app's site and a Stripe integration; this is work outside the ThreadWalker repo that the draft spec does not mention. The diagram has a "Stripe Webhook" but no table or handler receives it.

---

## 9. Question 8 — Internal contradictions in the draft spec

Line numbers refer to the draft spec.

1. **Number of tables**: "initialize 7 core tables" (line 106) but 9 tables are listed (108–130).
2. **Status enums do not match each other**: `agent_state.status` = `AWAKE | SLEEPING | STARVING` (109); `survival_mode` = `THRIVING | NORMAL | SURVIVAL` (110); the `state-machine.ts` comment = `THRIVING | NORMAL | STARVING | SLEEPING` (84); §5.5 uses `THRIVING / NORMAL / SURVIVAL` (241–243). `STARVING` and `SURVIVAL` refer to the same thing; `STARVING` sits in `status`, mixed in with the awake/asleep axis.
3. **Location of the state machine**: the directory tree puts it at `src/agent/state-machine.ts` (84); §5.5 says "Survival State Machine (`src/connectors/finance.ts`)" (239).
4. **Nonexistent columns**: `predicted_engagement_score`, the prediction reason, `actual_engagement_score` (271–272) are in no table.
5. **Missing bandit join key**: `threads_actions` has no `strategy_id`; `alpha_wins`/`beta_losses` cannot be incremented for the correct arm.
6. **Missing metric columns**: `getPostInsights` fetches `views, reposts, quotes` but `threads_actions` has only `likes_count`, `replies_count`; the auto-plug condition uses `views >= 1000` (204).
7. **Column names**: `Clean_facts_markdown` (116) versus `clean_facts_markdown` (180).
8. **Autopsy schemas diverge**: the `viral_autopsies` table has `hook_formula`, `trigger_psychology`, `why_flop_failed` (124); the function schema has `hook_anatomy`, `comment_bait_trigger`, `flop_root_cause` (265–267).
9. **Number of comments read**: "first 10 comments" (148), "first 5 comments" (164), "10 comments under the Viral post" (264).
10. **Missing permission**: the list (141) lacks `threads_read_replies`, while `getConversationContext` needs it (Section 2.4).
11. **Thompson sampling versus 80/20** (256, 279–281): see Section 7.1.
12. **"Flop that nobody engaged with"** (263) needs engagement counts of strangers' posts; `keyword_search` does not return them (Section 2.4).
13. **RAM**: "< 250MB RAM" (21) versus "VPS (2GB RAM)" in the diagram (37).
14. **Frontend undecided**: "Next.js (Static Export) or Vite + React" (32); the directory tree says "React/Vite/Tailwind" (95).
15. **Budget**: `max_budget_usd: 0.35` in `NORMAL` mode (224) versus the displayed burn rate "$0.18/day" (292). It is unclear whether `max_budget_usd` is a cap or a target, and what enforces it.
16. **Runway counts only OpenRouter**: the goals say to track "VPS cost" too (14); `ledger_metrics` has only `llm_cost_usd` (128).
17. **Model**: `THRIVING` uses `claude-3.5-sonnet` (241), a model that has been removed from OpenRouter. Critic, autopsy and dreaming have no model tier stated.
18. **No embedding model**: `dream_lessons.embedding` (122) needs embeddings; the draft spec names no model or dimension count.
19. **No token refresh job** and nowhere to store the token/expiry (Section 2.6).
20. **15-second sleep** (151) versus the docs' recommendation of 30 seconds and the status endpoint.
21. **The "like a real person" goal** (12) versus the sample bio that admits to being an agent (313) and the public dashboard that lists every action with a "View on Threads" link (299). The draft spec does not decide whether the account discloses that it is AI.
22. **"Absolute compliance"** (139) versus the "simulated reading time" jitter (238) and the public dashboard with @username plus negative remarks about strangers (296): see Section 3.5.
23. **Phase 1 "1–2 hours"** (365) assumes `keyword_search` and replying to strangers' posts work immediately; both depend on App Review (Section 2.2).
24. **Score thresholds**: `relevance_score < 7.5` and `human_naturalness_score < 8` on a scale the LLM scores itself, with no calibration set. Phase 2 says "tune the prompt on 20 posts" but has no pass criterion.
25. **`AppTrack → SSE`** in the diagram (54): click/Stripe webhooks go into the SSE broadcaster; there is no webhook-receiving endpoint in `server/app.ts` (93).
26. **`reply_patrol` every 15 minutes** (204) does not appear in `scheduler.ts` or any file of the directory tree, and it does not say whether it runs outside the Active Window.

---

## 10. Question 9 — Prior art

- **ElizaOS and Threads**: [Sourced, GitHub API] repo https://github.com/elizaOS/eliza (≈19.8 thousand stars, last push 2026-11-11). The `plugins/` directory has 81 entries, including `plugin-x`, `plugin-discord`, `plugin-telegram`, `plugin-slack`; **there is no Threads plugin**. The README does not mention Threads. The `elizaOS` org has 20 public repos, none with a Threads-related name. The `elizaos-plugins` org currently returns 0 public repos. Whether a community-written Threads plugin exists elsewhere: [Unverified].
- **What happened to their accounts**: the sources found are press (secondary), not court records. According to The Block (https://www.theblock.co/post/368744/ai-startup-eliza-labs-sues-x) and Decrypt (https://decrypt.co/337382/eliza-labs-sues-x-accuses-elon-musk-platform-copying-ai): Eliza Labs and its founder Shaw Walters sued X Corp on 2025-08-27 in federal court in the Northern District of California; the complaint alleges that the X accounts of Eliza Labs and of Walters were suspended after they declined an enterprise license package of about $50,000/month. This is the plaintiffs' allegation. The sources are inconsistent about the suspension date. Outcome of the lawsuit: [Unverified].
- [Inferred] The applicable lesson: a platform can cut off an open-source agent project at both the account level and the API level without prior notice (consistent with Platform Terms §7.e.ii in Section 3.1). A project that lives on a single account and a single app has no fallback.
- Other open-source agents on Threads and the fate of their accounts: [Unverified], no primary source found.

---

## 11. Question 10 — Multiple accounts run by one person

### 11.1 Technically

- [Sourced] https://developers.facebook.com/docs/threads/get-started/get-access-tokens-and-permissions: "The Authorization Window allows app users to grant your app permissions and short-lived Threads user access tokens." The token is tied to the authorizing user; the response returns a `user_id` with it. The page states no limit on the number of users per app.
- [Sourced] https://developers.facebook.com/docs/threads/get-started: a tester (added under App roles → Threads Tester, accepted in Threads Account Settings) can grant permissions without App Review.
- [Sourced] Per-user limits: 2,200 searches / 24h "applies to a user across apps"; the publishing quota is also per user.
- Conclusion: **one app holding several tokens for several accounts is consistent with the API's mechanism**. Maximum number of testers: [Unverified]. Number of Instagram/Threads accounts one person may create: the Instagram Help Center (https://help.instagram.com/1682672155283228, seen only through search results, an old article) says up to 5 accounts can be added in the app; the current figure and the rule for Threads: [Unverified].
- [Inferred] All accounts share one app ID and one verified Business, so Meta can directly see that they have the same operator. A violation by one persona can pull in the whole app (Platform Terms §7.e.ii: "We may take enforcement action at any time, including while we investigate your App(s), with or without notice to you."; the Vietnamese version of the same section says measures may include suspending or permanently removing the app and accounts).

### 11.2 Policy-wise

[Sourced, verbatim] https://transparency.meta.com/policies/community-standards/inauthentic-behavior/

- "Inauthentic Behavior refers to a variety of complex forms of deception, performed by a network of inauthentic assets controlled by the same individual or individuals, with the goal of deceiving Meta or our community or to evade enforcement under the Community Standards."
- "We do not allow: The creation, use, or claimed use of Inauthentic Meta Assets (Accounts, Pages, Groups, etc.) in order to: Deceive Meta or our users about the identity, or origin of an audience or the entity that they represent" / "To Evade enforcement under the Community Standards."
- CIB: "particularly sophisticated forms of Inauthentic Behavior where false identities are central to the operation and operators use adversarial methods to evade detection or appear authentic".
- "When we identify these networks, we remove the fake accounts, Pages, Groups, or other Meta assets directly involved in the operation."
- Note: the fetch tool reported that an earlier version (2025-12-11) had the items "Inauthentic Distribution: Using a connected network of inauthentic Meta assets to increase the distribution of content" and "Inauthentic Engagement: … fake engagement in ways designed to look authentic, in order to deceive Meta and its users about the popularity of content". The raw version loaded today no longer has these two items. Whether they were dropped or merged: [Unverified].

[Sourced] https://transparency.meta.com/policies/community-standards/authentic-identity-representation/

- The term prohibiting multiple accounts ("Create or maintain multiple Facebook accounts") **applies to Facebook only**. For Threads, the applicable term is: "Engage in identity misrepresentation to mislead or deceive others, evade enforcement, or violate our Community Standards."
- Fan/parody/satire accounts are allowed if "The account must make its unofficial status abundantly clear at minimum in the account name".

[Sourced] Spam: prohibits "selling, buying, or exchanging for engagement"; allows "Cross promotion that is not triggered by payment to a third party".

[Sourced] Instagram Terms: "you may not impersonate someone or something you aren't".

### 11.3 Assessment

**[Inferred]** from the terms above:

- **Having multiple Threads accounts is not in itself prohibited** under the current text (the multiple-accounts prohibition is stated only for Facebook).
- **Deception is what decides it.** Several accounts that "present themselves as different, unrelated people" match the definition almost word for word: "a network of inauthentic assets controlled by the same individual… with the goal of deceiving… our community" and "Deceive… our users about the identity… or the entity that they represent".
- **Yes: accounts interacting with each other or boosting the same link is an aggravating factor.** It turns "several accounts" into a coordinated "network" creating a false impression of the level of support; the earlier version's "Inauthentic Distribution / Engagement" items describe exactly this behavior. Two personas staging an argument to draw engagement also fall here.
- **A lower-risk option**: each account states in its name/bio that it is an automated agent and has the same owner; no interaction, no reposting, no cross-plugging of links among them; `is_made_with_ai=true`. Even so, this is the report author's interpretation, not a commitment from Meta.
- Which term states explicitly "one operator may run N automated accounts via the API": [Unverified], none found.

---

## 12. Question 11 — Provocative ("toxic") persona

### 12.1 Bullying and Harassment

[Sourced, verbatim] https://transparency.meta.com/policies/community-standards/bullying-harassment/ (current version; most recent changelog entry 2026-10-29)

- Scope: "We do not tolerate this kind of behavior because it prevents people from feeling safe and respected on Facebook, Instagram, and Threads."
- Distinction between targets: "We distinguish between public figures and private individuals because we want to allow discussion, which often includes critical commentary of people who are featured in the news or who have a large public audience." Public figures include "people with over one million fans or followers on social media".
- "For private individuals, our protection goes further: We remove content that's meant to degrade or shame".
- Tier 1, everyone is protected from: "Unwanted contact that is: Repeated, OR Sexually harassing, OR Is directed at a large number of individuals with no prior solicitation." / "Calls for, or statements of intent to engage in, bullying and/or harassment."
- Tier 2 (private adults, minors, limited scope public figures): "Dehumanizing comparisons…", "Content that ranks them based on physical appearance or character traits."
- Tier 3, "private adults (who must self-report)": "Targeted cursing." / "Negative character or ability claims, except in the context of criminal allegations and business reviews against adults." / "Expressions of contempt, disgust, or content rejecting the existence of an individual".
- Mass harassment: "Disable accounts engaged in mass harassment as part of either… Adversarial networks targeting any individual via any surface with: Content that violates the bullying and harassment policies for private individuals".

### 12.2 Hateful Conduct

[Sourced, verbatim] https://transparency.meta.com/policies/community-standards/hateful-conduct/

- "That is why we don't allow hateful conduct on Facebook, Instagram, or Threads."
- "We define hateful conduct as direct attacks against people — rather than concepts or institutions — on the basis of what we call protected characteristics (PCs): race, ethnicity, national origin, disability, religious affiliation, caste, sexual orientation, sex, gender identity, and serious disease."
- Tier 2 includes "Insults, including those about: Character… Mental characteristics, including but not limited to allegations of stupidity, intellectual capacity…" and "Targeted cursing" when aimed at a person on the basis of a protected characteristic.
- Conditional exception: "Content attacking concepts, institutions, ideas, practices, or beliefs associated with protected characteristics, which are likely to contribute to imminent physical harm, intimidation or discrimination" (additional context is required before action is taken).
- Satire: "Content will only be allowed if the violating elements of the content are being satirized or attributed to something or someone else in order to mock or criticize them."

### 12.3 Spam and engagement bait

- [Sourced] The current Spam policy defines engagement-bait-type behaviors as *coercing* engagement: "Requiring or claiming that users are required to engage with content (e.g., liking, sharing) before they are able to view or interact with promised content." and "Like/share-gating". Rationale: "content that is designed to deceive, mislead, or overwhelm users in order to artificially increase viewership."
- Content that "deliberately provokes controversy to draw replies" (rage bait) is **not named** in the Community Standards pages read. Whether the Threads algorithm demotes this kind of content: [Unverified].
- No term was found that deals specifically with "*automated* insulting replies". The policies above apply regardless of whether a human or a machine writes; automation only adds the frequency clause of Spam ("either manually or automatically").

### 12.4 Assessment

**[Inferred]** from the terms above:

- **The policy distinguishes ideas from people, and the line is clear.** Sharply rebutting a viewpoint, a product, a company, a trend ("vibe coding is a joke") matches none of the quoted prohibitions, provided it does not target a protected characteristic. Mocking *the author* does match.
- **The agent's reply targets are mostly private adults** (under 1 million followers). For them, Tier 3 prohibits exactly what a "toxic" persona would produce: cursing aimed at a named person, negative claims about ability/character ("this guy can't code"), expressions of contempt. Tier 3 requires the targeted person to self-report; every reply is an invitation to report.
- **Automation multiplies the risk**: the same kind of insult repeated on dozens of strangers a day approaches "Unwanted contact… directed at a large number of individuals with no prior solicitation" (Tier 1, no self-report needed) and the description of mass harassment.
- **Combined with question 10**: a toxic persona and a "nice" persona with the same owner, plugging the same set of apps: if they interact with each other, that is staging conflict through a network of accounts; if the toxic persona is locked, the consequences spread to the apps and the remaining accounts.
- **An LLM critic is not sufficient as the gate** for the ideas/people boundary (Section 7.3 shows that LLM self-assessment is unreliable), and the Vietnamese context (sarcasm, forms of address) makes it harder still. This is a judgment, with no measurement behind it.
- **The option that can still be defended**: a persona that "sharply critiques ideas" posting only on its own wall or replying to people who mentioned/replied to it; second-person evaluative statements about people are banned; `is_made_with_ai=true`. Still an interpretation, not a guarantee.

---

## 13. Points that must be settled by a real API spike

Ordered by how much each decides whether the project is feasible at all.

1. **App Review**: whether the use case "an automated agent searches then replies to public posts" is approved for `threads_keyword_search` + `threads_manage_replies` + `threads_content_publish` (+ `threads_read_replies`, `threads_manage_insights`) at Advanced Access; what Business Verification is needed; how long it takes. This is the prerequisite; everything else is meaningless if it is rejected.
2. **What Standard Access can do**: with a tester account, whether `keyword_search` really returns only its own posts; whether it can reply to a stranger's post using a media ID obtained from another source (permalink).
3. **`/{media-id}/conversation` and `/replies` on a stranger's post** (after Advanced Access is obtained): whether it returns data, an empty array or an error; which permission is needed.
4. **Engagement counts of strangers' posts**: confirm that no field exists (try `fields=like_count,...` on the media object and `/insights` on a stranger's post) to decide whether to drop or redesign Viral Autopsy.
5. **"Recently searched"**: how long after the search a reply is still accepted; the error returned after the window expires.
6. **Locked reply audience**: the error code and message when replying to a post with a restrictive `reply_control` or where the author has blocked the account.
7. **Insights on the agent's own replies**: whether `/insights` returns `views/likes/replies` for a reply (not a root post); data latency; what `views` ("in development") returns.
8. **Container status**: the real time to `FINISHED` for a text post; poll instead of a fixed sleep.
9. **Character counting**: how the 500 limit is counted for Vietnamese with diacritics, emoji, URLs.
10. **`is_made_with_ai=true` on a text reply**: how the label is displayed; whether it affects distribution (can only be observed; no source).
11. **Token refresh**: run one real `th_refresh_token` cycle; behavior when a token expires midway.
12. **Multi-account**: the maximum number of testers per app; whether quotas and rate limits are split per user as the docs say.
13. **Webhooks `replies` / `mentions`**: whether they are received at Standard Access with a tester (the docs say no).
14. **Threads Terms §3(a) full text**: not a technical spike, but someone must read items (i) "commercial purpose" and (iv) "automatic device" verbatim, with all exceptions, before committing.
15. **`sqlite-vec` under `bun:sqlite` in Docker on Linux** and on a macOS dev machine (only if this stack is kept).
16. **Real cost**: measure tokens per step on 20 real posts to replace the assumptions in Section 8.1.
17. **Dub**: whether `dub_id` survives the Threads in-app browser to the destination site.

---

## 14. List of what could not be verified

- The full text of Threads Terms of Use §3(a) and its exceptions.
- The content of the Instagram Terms update taking effect on 2027-01-20.
- Whether AI self-disclosure is mandatory for plain text posts on Threads; whether there is a separate label for automated accounts.
- App Review duration and approval rate for Threads permissions.
- Maximum number of testers per app; maximum number of Threads accounts per person.
- Whether the two items "Inauthentic Distribution / Engagement" were dropped or merged in the current Inauthentic Behavior text.
- Whether the Threads algorithm demotes posts containing external links (the premise of "absolutely no external links") or rage bait content: no primary source found.
- The terms of use of Google Trends RSS and Product Hunt RSS.
- Reddit: `reddit.com` could not be fetched to see the API wiki rules and current rate limits.
- `sqlite-vec` + `bun:sqlite` on Linux; "< 250MB RAM".
- The date and reason `anthropic/claude-3.5-sonnet` was removed from OpenRouter.
- The outcome of Eliza Labs v. X Corp; the fate of other open-source agents' accounts; the existence of a community-written Threads plugin.
- The two arXiv papers 2510.08481 and 2505.03769 were seen only through search summaries.
- Research on LLMs predicting the engagement of the very post they wrote.
