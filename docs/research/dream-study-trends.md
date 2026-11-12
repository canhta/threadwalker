# Dreaming, study and trend signals

Evidence for the owner decisions of 2026-11-12: a nightly dream, a study schedule and
relevant-trend selection. All claims below are local measurements or a named limitation.

## What was measured

- `go test -count=1 ./...` passes with phase, ledger, trend, study-source and dashboard
  fixtures (offline; no live network).
- One live, read-only study fetch on **2026-11-12** from this host:
  `items=120 failed=[] skipped=[] stories=85` across the credential-free sources.
  It found the current Jensen Huang story and merged seven feed copies from four sources
  into the top cluster.
- Reddit RSS/JSON is blocked for unauthenticated clients from this host (connection reset,
  or an HTML "Welcome to Reddit" page). The Reddit sources are therefore opt-in app-only
  OAuth and are skipped, with no request, when `REDDIT_CLIENT_ID`/`REDDIT_CLIENT_SECRET`
  are unset.
- `ictnews.vn/*.rss` now redirects to `vietnamnet.vn` and returns HTML. The source ID is
  `vietnamnet` using `https://vietnamnet.vn/rss/cong-nghe.rss`, which returns RSS.
- YouTube channel feeds return Atom only with a browser-like User-Agent; the default
  channel IDs are OpenAI, Google DeepMind, Lex Fridman and The Verge.
- An optional bounded `web_search` tool is implemented for study only. It makes one OpenAI
  Responses request with `tools:[{"type":"web_search"}]`, no retries, no streaming,
  `store:false`, at most 1024 output tokens and at most 10 citations. It reuses the selected
  template's `OPENAI_API_KEY` and token prices, checks the shared daily cap before the call
  and records the reported usage to the study phase ledger. It is disabled unless
  `study.web_search.enabled: true`; the live viral template enables it with `gpt-6-luna`.
  It stays outside the canonical chat/tool loop, so the ordinary LLM adapter contract is
  unchanged.

## Live Threads read-only check

On 2026-11-12, 15:40–15:46 ICT, the Orca Browser session already logged in as `giaosullm`
was used read-only (no clicks, no writes, no likes) to take five accessibility snapshots:
home feed, then searches `AI thay dev`, `agent tự động`, `vibe code`, then home again.
Generated posts were parsed with visible counters and passed through the same
`internal/trends` computation as production.

```text
rising: ai (8 posts), code (6), vibe (5), dev (4), agent (3), app (3), claude (3)
formats: medium 11, medium+question 2, short 10, short+question 2
hot posts: (empty this run; the two home snapshots did not contain the same post, so no
           reply delta existed)
```

The snapshot showed visible `Like 92`, `Reply 5`, `Repost 2` buttons. The current
`agent-browser` measured mapping normalizes posts without counters, so this measurement used
the Orca snapshot as the read surface and stored NULL counters. Exposing counters through
`threads.Post` and the live parser remains a #2/#18 follow-up; until then live velocity is
not fabricated.

## Top 10 clustered stories from the live fetch

```text
items=120 failed=[] skipped=[] stories=85
 1. score=15 items=7 sources=[the_verge techcrunch arstechnica hacker_news]
    Nvidia is reportedly preparing to fire Jensen Huang
    https://www.theverge.com/tech/1031652/jensen-huang-rumor
 2. score=12 items=7 sources=[google_news_vn google_news_topics genk vietnamnet]
    Máy tính lượng tử có thể trở thành lời giải cho bài toán năng lượng của AI? - VnEconomy
    https://news.google.com/rss/articles/CBMioAFBVV95cUxNdWVWcDlrTEdyQTZYNHF4bWxZNnA1aGp4eUwxVjZpOHhDNDIxdnEzVjRjVE1maHpLRXhEOHZzWFVSVTFYRllDTmFzdF9UYm9fV0tFeExEQ3hEZnNodmJqVFpLamw3MjZQV2c4c21hM2ZCdi04UG4za0ROVjJ0QlZsbkVfYmZzQ1Q0ejZJdVJCUmplVDVHQ2pOTXJaZUxnNE1J?oc=5
 3. score=12 items=6 sources=[google_news_topics genk tinhte vietnamnet]
    AI phát triển, người cao tuổi và người khuyết tật không thể bị bỏ lại phía sau - Laodong.vn
    https://news.google.com/rss/articles/CBMiugFBVV95cUxNdngzeFRSLUFyWkhjSGV4MWlpUzRaRldnZGZVaHRsOHZEMmpuWWJtVTZZd0ZoclUxNjdrY21tRWhUYmoyZWVZUTBxbnFSZXhRMmhDV0d5ZnpPYmhPWUJTclBxVTFBU21WYUU1YmMwSU42SHJ6T1J2cVlFLXNubnl3MUZjcXY3UzJiUmV0b1FpT3ZrTDk2WlRLamJLT0p4ZlVubUFEcVdYNE5kRWtSMjh6LWUtUFJuZWpoY0E?oc=5
 4. score=9 items=2 sources=[github_trending youtube]
    addyosmani/agent-skills: Production-grade engineering skills for AI coding agents.
    https://github.com/addyosmani/agent-skills
 5. score=8 items=4 sources=[google_news_vn google_news_topics]
    Cán bộ, công chức, viên chức không được đưa dữ liệu mật lên AI để đặt câu hỏi - Báo VietNamNet
    https://news.google.com/rss/articles/CBMingFBVV95cUxQWXBNNEs0dEozVnJlWXgtUG9oV3oxQjdycFl3UFB5S1d1UTNOMHpzNFRZWFdFQXRBSnNZbC1CYk9oUzNOMGhmNF83bURQSEktVk5OYnNnMEpBczYySnhhajNTQ3YwQU1tdW5kU0FtbWMydUpmeWZVMEQyYkMzakV4cXJ5QmFmYlRKRmJ6aS1MMzBUYUtPLWQya090YlBXZw?oc=5
 6. score=8 items=6 sources=[google_news_topics vietnamnet]
    Từ sinh viên Bách khoa đến Chủ tịch Bkav, ông Nguyễn Tử Quảng tiết lộ: 1 kỹ sư dùng AI trong 1 tuần bằng 10 kỹ sư làm 8 tháng, chi phí từ 3 tỷ đồng còn 3 triệu - CafeF
    https://news.google.com/rss/articles/CBMiogJBVV95cUxPYXh5QmU3dk93NUJlUWVxc2I5V3gzZWtYVERGY2tLQl9EeFpPUE9vTHF3YXRsUUYyWUxlWjhrN2Q4cWhMZGxyZFhTWkU4c01Ba2RGV2xoQi11dWdtN2VWeWFtZDZiYmgwZzRYRWRkUVMxTktrbnJZMlF4REhSVk1HSDREa1RmdHlJakFvTWxhRmhaNzBHRkZFQ2ZhS1JpUW5rZG4xRnBzd1FsS3FuRHlkaGd5bzM5YWJvYk9RdDZCNHRhS3laUnkzZzFpZWludEp6Y1Bua1JOMHI3ZlVubGJpTTdHY2FXR19rUmZHUjJtUnk1dk8yMHo2SFVpdHI3djhKNXdUS1RZcWRQcUpjN0xjTXF2WmpSUW5GTTg3WkFURm5LQQ?oc=5
 7. score=7 items=3 sources=[github_trending youtube]
    openai/codex-security: OpenAI's Codex Security CLI and TypeScript SDK for finding, validating, and fixing security vulnerabilities. npm:  https://www.npmjs.com/package/@openai/codex-security
    https://github.com/openai/codex-security
 8. score=6 items=1 sources=[google_news_topics]
    Bê bối của CEO Nvidia và nghi vấn thương vụ chip AI 7,5 tỷ USD - VnExpress
    https://news.google.com/rss/articles/CBMingFBVV95cUxOY1MtWjRjRXpFY0tmcmJHY1ZSRHozRTZISXo3R1RVLVdXLTViVkNyck0wck1qdXNXaElNd2ZPaGFPaW56ZHQzWVJ0bnpyVzFUc3RpVmVNYURzMDdsVkFfZVQ2MlNVTkZyYVZzRHFxbHpnQXUxYVk3X2dVaWtWZTRxZElvRUlkajBlMlBwWEZpRzJLQUU2U3VDQk04TGxVdw?oc=5
 9. score=6 items=1 sources=[vnexpress]
    OpenAI triển khai hệ thống dừng khẩn cấp AI trong 35 giây
    https://vnexpress.net/openai-trien-khai-he-thong-dung-khan-cap-ai-trong-35-giay-5143494.html
10. score=6 items=2 sources=[the_verge github_trending]
    homeassistant-ai/ha-mcp: Configure and automate Home Assistant with Claude, ChatGPT, and other AI assistants.
    https://github.com/homeassistant-ai/ha-mcp
```

## Design notes

- Ranking is token/phrase matching over `trends.include_keywords` minus
  `trends.avoid_keywords`, with no model call. When the lists are omitted they are derived
  from the persona topics/avoid_topics with a fixed stopword list.
- Stories cluster when they share at least two distinctive tokens after removing generic
  topic keywords, so "Jensen Huang" from four sources is one story. Each extra source adds
  two points and each extra item one point (capped), which is why multi-source stories sort
  first.
- Study sources are independent: a failed source is listed in `failed_sources` and the
  rest still return. Per-source timeout is 10s, the overall deadline is 45s and at most four
  sources run at once. Every source is bounded to 8 items by default.
- Threads sampling during study uses the guarded `read_feed`/`search` tools. It is limited
  by `study.threads_sample.max_reads` (default 4), paced by `pause_ms` (default 1500) and
  writes only bounded observations. No write tool is offered and the guard refuses one with
  `phase_read_only`.
- The trend brief is computed from stored rows: rising keywords (24h rate versus a six-day
  baseline), reply velocity between observations, format counts, and merged external
  stories flagged `both`, `external_only` or `threads_only`.

## Limitations

- The measured Threads read mapping (`threads.Post`) exposes no reply/like/repost counters.
  Observations therefore store NULL counters, and live reply-velocity/hot-post rows stay
  empty until the read mapping exposes visible counters. They are never invented as zero.
- The live Threads check above used Orca Browser, not the production `agent_browser` profile;
  the production sampler remains covered by saved multi-observation fixtures. Live
  `agent_browser` sampling and counter extraction remain part of #2/#18.
- Clustering is a deterministic overlap heuristic, not entity resolution. It can merge two
  different articles that share two rare tokens, or keep one story split when headlines
  share only one name.
- `run -once -offline` and the isolation soak use a template with no schedule, so they keep
  their existing behaviour; the phase paths are covered by the dedicated offline tests.
