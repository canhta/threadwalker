# Kiểm chứng `handoff_spec.md` (ThreadWalker) với nguồn gốc

- Ngày truy cập mọi URL trong tài liệu này: **2026-11-11**.
- Tài liệu được kiểm chứng: `docs/handoff_spec.md` (không sửa spec).
- Phạm vi: 11 câu hỏi (9 câu ban đầu + câu 10 multi-account và câu 11 persona "toxic" được bổ sung giữa chừng). Theo thay đổi phạm vi của chủ dự án, câu 3 và 4 (provider/stack) chỉ ghi nhận đúng/sai của claim trong spec, không đào sâu phương án thay thế; công sức dồn vào câu 1, 2, 10, 11.

## Quy ước và giới hạn phương pháp

- **[Nguồn]** = nội dung có trong nguồn gốc, kèm URL. **[Suy luận]** = suy luận hoặc phép tính của người viết báo cáo, không phải lời của nguồn. **[Chưa kiểm chứng]** = không tìm được nguồn gốc.
- Phần lớn trang được đọc qua một công cụ fetch có tóm tắt; công cụ này cắt câu trích dài thành nhiều mẩu. Các câu trích dưới đây được ghép lại từ các mẩu liền kề. Riêng các trang Community Standards (Spam, Inauthentic Behavior, Bullying and Harassment, Hateful Conduct) và hai trang chính sách Reddit được tải dạng văn bản thô qua một reader proxy (`r.jina.ai`) và trích nguyên văn.
- Docs của Meta trả về bản tiếng Việt cho một số trang; các câu trích tiếng Anh lấy từ bản `?locale=en_US`.
- Không có token Threads và không cài Bun trên máy, nên **không có lệnh nào được chạy thật với Threads API hay `bun:sqlite`**. Mọi kết luận về hành vi runtime đều là đọc docs, không phải đo.
- Các lệnh đã chạy thật: `GET https://openrouter.ai/api/v1/models` (không cần key), npm registry, GitHub API công khai, và `curl` tới các nguồn trend.
- Không đọc được: `help.instagram.com` (chặn fetch; dùng bản mirror `facebook.com/help/instagram/...`), toàn văn Section 3 của Threads Terms of Use (công cụ chỉ trả về vài mẩu trích), `reddit.com` (không fetch được từ máy này).

---

## 1. Bảng kết luận nhanh

| # | Claim trong spec | Kết luận | Nguồn |
|---|---|---|---|
| 1 | `GET /keyword_search` với `q`, `search_type=TOP\|RECENT` tồn tại | **Đúng** (mặc định là `TOP`; spec đặt mặc định `RECENT` ở phía client, không sai) | https://developers.facebook.com/docs/threads/keyword-search |
| 2 | Giới hạn 2.200 queries / 24h | **Đúng**, nhưng tính theo *user* trên mọi app, và query lặp cùng keyword vẫn bị tính | cùng URL |
| 3 | `keyword_search` trả về bài public của người lạ (spec ngầm định, Phase 1 "1–2 giờ") | **Sai nếu chưa qua App Review**: chưa được duyệt thì chỉ tìm trong bài của chính user đã xác thực | cùng URL |
| 4 | Fields `id,text,timestamp,username,permalink` | **Đúng**; không có field đếm like/reply nào | cùng URL |
| 5 | Danh sách permission của spec là đủ | **Sai/thiếu**: thiếu `threads_read_replies` (bắt buộc cho GET replies/conversation) | https://developers.facebook.com/docs/threads/get-started |
| 6 | Reply (`reply_to_id`) vào bài của người lạ | **Được docs cho phép có điều kiện**: phải có `threads_keyword_search` hoặc `threads_manage_mentions`, và bài phải là bài "recently searched" | https://developers.facebook.com/docs/threads/retrieve-and-manage-replies/create-replies |
| 7 | `/{media-id}/conversation` đọc comment dưới bài người lạ | **Chưa kiểm chứng** (docs không nói rõ; mô tả permission nghiêng về "thread owned by the business app user"). Cần spike | https://developers.facebook.com/docs/permissions |
| 8 | `/{media-id}/insights` cho bài người khác; so sánh "viral vs flop" bằng like/reply của người lạ | **Sai theo docs**: insights chỉ cho bài của chính user; media object và keyword search không có field đếm tương tác | https://developers.facebook.com/docs/threads/insights |
| 9 | Quota 250 posts / 1.000 replies / 24h; fields `quota_usage,config,reply_quota_usage,reply_config` | **Đúng** (còn có quota 100 deletes, 500 location searches) | https://developers.facebook.com/docs/threads/overview |
| 10 | `Bun.sleep(15000)` rồi publish | **Sai so với khuyến nghị**: docs khuyên ~30 giây; có endpoint status để poll | https://developers.facebook.com/docs/threads/troubleshooting |
| 11 | Cap text 450 ký tự | **Thận trọng hơn giới hạn thật**: giới hạn là 500 | https://developers.facebook.com/docs/threads/posts |
| 12 | Token (spec không nhắc refresh) | **Thiếu**: long-lived token sống 60 ngày, phải refresh | https://developers.facebook.com/docs/threads/get-started/long-lived-tokens |
| 13 | Base URL `graph.threads.net/v1.0` | **Vẫn dùng được nhưng lỗi thời về ví dụ**: docs nay dùng `graph.threads.com`; cả hai host đều hợp lệ; version vẫn `v1.0` | https://developers.facebook.com/docs/threads/changelog |
| 14 | Metric `views,likes,replies,reposts,quotes` | **Đúng** (thêm `shares`; `views` và `shares` ghi "in development") | https://developers.facebook.com/docs/threads/insights |
| 15 | Thiết kế "reply dạo người lạ + persona như người thật + jitter + plug link" tuân thủ chính sách vì dùng API chính thức | **Sai về lập luận**: dùng API chính thức không miễn trừ Community Standards / Developer Policies; nhiều điều khoản áp dụng trực tiếp | mục 3 |
| 16 | `GET /api/v1/credits` của OpenRouter | **Đúng là tồn tại**, nhưng cần *management key* (key này không gọi được completions) | https://openrouter.ai/docs/api/api-reference/credits/get-credits |
| 17 | `google/gemini-2.5-flash-lite`, `deepseek/deepseek-chat`, `google/gemini-2.5-pro` | **Đúng** (còn trong danh sách model) | https://openrouter.ai/api/v1/models |
| 18 | `anthropic/claude-3.5-sonnet` | **Lỗi thời**: không còn trong danh sách model; trang model trả 404 | https://openrouter.ai/api/v1/models |
| 19 | `@openrouter/ai-sdk-provider` + Vercel AI SDK | **Đúng** (provider 3.1.0 peer `ai ^7.0.0`; `ai` latest 7.0.169) | https://registry.npmjs.org/@openrouter/ai-sdk-provider |
| 20 | `generateObject` cho mọi bước | **Lỗi thời**: bị deprecate từ AI SDK 6, thay bằng `generateText` + `output` | https://ai-sdk.dev/docs/migration-guides/migration-guide-6-0 |
| 21 | `sqlite-vec` chạy dưới `bun:sqlite` | **Có recipe chính thức; có caveat macOS**; Linux/Docker **chưa kiểm chứng** bằng chạy thật | https://alexgarcia.xyz/sqlite-vec/js.html |
| 22 | Drizzle hỗ trợ `bun:sqlite` | **Đúng**; cột vector / virtual table `vec0`: không thấy hỗ trợ gốc | https://orm.drizzle.team/docs/connect-bun-sqlite |
| 23 | "< 250MB RAM" | **Chưa kiểm chứng** (không đo được) | — |
| 24 | Google Trends VN RSS, Product Hunt RSS, HN API tồn tại | **Đúng** (cả ba trả HTTP 200 khi thử) | mục 6 |
| 25 | Dùng Reddit làm nguồn trend cho bot có mục đích thương mại | **Sai nếu không xin phép**: cần phê duyệt trước và thoả thuận riêng cho mục đích thương mại | https://support.reddithelp.com/hc/en-us/articles/42728983564564-Responsible-Builder-Policy |
| 26 | Jina Reader | **Đúng là dùng được không cần key**, giới hạn 20 RPM | https://jina.ai/reader/ |
| 27 | "Thompson Sampling (80% exploit / 20% explore)" | **Sai về khái niệm**: tách cố định 80/20 là epsilon-greedy, không phải Thompson sampling | mục 7 |
| 28 | Bandit "khôn lên theo từng ngày" với 15–35 reply/ngày | **Không khả thi về thống kê trong khung "tuần này"** [Suy luận, có phép tính] | mục 7 |
| 29 | `max_budget_usd: 0.35`, "$0.18/day" | **$0.18 không đạt được với pipeline như mô tả; $0.35 chỉ đạt ở kịch bản tiết kiệm nhất** [Suy luận, có phép tính] | mục 8 |
| 30 | Attribution doanh thu về từng reply qua Dub.co | **Chỉ khả thi ở mức link, không ở mức reply**, và cần script trên site đích | https://dub.co/docs/conversions/quickstart |
| 31 | "7 bảng cốt lõi" | **Sai**: liệt kê 9 bảng | spec dòng 106–130 |
| 32 | (Câu 10) Một app, nhiều tài khoản Threads, mỗi tài khoản một token | **Đúng về cơ chế** (token theo user); giới hạn số tester: chưa kiểm chứng | https://developers.facebook.com/docs/threads/get-started/get-access-tokens-and-permissions |
| 33 | (Câu 10) Nhiều tài khoản đóng vai những người không liên quan, tương tác/boost lẫn nhau | **Rất có khả năng vi phạm** Inauthentic Behavior và Authentic Identity Representation [Suy luận từ điều khoản trích dẫn] | mục 11 |
| 34 | (Câu 11) Persona "toxic" tự động chế giễu/xúc phạm cá nhân | **Rất có khả năng vi phạm** Bullying and Harassment (private adults được bảo vệ khỏi "Targeted cursing", "Negative character or ability claims", "Expressions of contempt") [Suy luận từ điều khoản trích dẫn] | mục 12 |
| 35 | (Câu 11) Chính sách có phân biệt công kích ý tưởng và nhắm vào con người | **Có**: "direct attacks against people — rather than concepts or institutions" | https://transparency.meta.com/policies/community-standards/hateful-conduct/ |
| 36 | ElizaOS và các agent tương tự có hỗ trợ Threads | **Không thấy**: repo `elizaOS/eliza` có `plugin-x`, không có plugin Threads | https://github.com/elizaOS/eliza |

---

## 2. Câu 1 — Threads API

### 2.1 `keyword_search`

[Nguồn] https://developers.facebook.com/docs/threads/keyword-search

- Endpoint: `GET https://graph.threads.com/v1.0/keyword_search`.
- Tham số: `q` (bắt buộc), `search_type` (`TOP` mặc định, hoặc `RECENT`), `search_mode` (`KEYWORD` mặc định hoặc `TAG`), `media_type`, `since`, `until`, `limit` (mặc định 25, tối đa 100), `author_username`.
- Fields ví dụ: `id,text,media_type,permalink,timestamp,username,has_replies,is_quote_post,is_reply`. "The `owner` field is excluded and will not be returned."
- Giới hạn: "A user can send a maximum of 2,200 queries within a rolling 24-hour period." / "This limit applies to a user across apps and is not differentiated for different apps." / "Subsequent queries against the same keyword within this timeframe will also count against this limit." / "Queries which return no results do not count against this limit for the user."
- Trước khi được duyệt: "If your app has not been approved for the `threads_keyword_search` permission, the search will be performed only on posts owned by the authenticated user." / "After approval, public posts will be searchable."
- Từ khoá nhạy cảm: "The API will return an empty array for any requests that include keywords that we have deemed sensitive or offensive."
- Tương tác với bài đã tìm: "You can interact with public Threads media that you have recently searched for." Hành động gồm reply, quote, repost; "Additional permissions may be required".

Hệ quả cho spec:

- [Nguồn] Spec có thể dùng `since`/`until` để lọc "bài > 6h" thay vì tự lọc phía client.
- [Suy luận] "Lưu cache để không gọi trùng" là đúng hướng, vì query lặp vẫn bị tính quota.
- [Suy luận] Phase 1 ("1–2 giờ", test `keyword_search` trên bài thật) không thực hiện được trước khi qua App Review: với Standard Access, kết quả chỉ là bài của chính tài khoản.

### 2.2 App Review cho `threads_keyword_search`

[Nguồn] https://developers.facebook.com/docs/permissions

- Mô tả: "allows a business app to search and fetch content with a specific keyword on behalf of a Threads user".
- Allowed usage: "Showing public content and the public content tree that a business app user searches using a specific keyword."
- Hạn chế: "using threads_keyword_search permission in a consumer app is strictly prohibited."
- Yêu cầu review: ví dụ use case cụ thể; screencast gồm luồng Threads OAuth login và liên kết tài khoản, một lần keyword search nội dung public, và kết quả hiển thị trong app và trong app Threads gốc.

[Nguồn] https://developers.facebook.com/docs/graph-api/overview/access-levels (bản tiếng Việt, trích theo bản dịch của trang)

- Standard Access: chỉ user có vai trò trên app mới dùng được.
- Advanced Access: app có thể xin quyền từ user bất kỳ; phải qua Business Verification; "trong một số trường hợp" từng permission phải qua App Review.

[Nguồn] https://developers.facebook.com/docs/threads/get-started: "each permission must first be approved through the App Review process, and your app must be published." Tester có thể cấp quyền bất kỳ lúc nào mà không cần App Review.

[Suy luận] "Allowed usage" mô tả việc *hiển thị* nội dung public cho user tìm kiếm; một bot tự động reply quảng bá không nằm trong mô tả đó. Kết quả review cho use case này là không biết trước. Thời gian review: [Chưa kiểm chứng] (trang App Review không nêu).

[Suy luận] Mô hình "OSS, 1 lệnh `docker compose up -d`" mâu thuẫn với thực tế: mỗi người tự host cần một Meta app riêng đã qua Business Verification và App Review (kèm screencast), hoặc phải dùng chung app của chủ dự án (khi đó chủ dự án chịu trách nhiệm cho mọi instance).

### 2.3 Reply vào bài của người chưa uỷ quyền cho app

[Nguồn] https://developers.facebook.com/docs/threads/retrieve-and-manage-replies/create-replies

- "`threads_manage_replies`: Required for creating replies with the `reply_to_id` parameter."
- "In addition, to reply to a thread, you must meet one of the following requirements:" / "You are the owner of the root Threads post." / "Your app has either the `threads_keyword_search` or the `threads_manage_mentions` permission."
- "If your app has not been approved for advanced access for the `threads_content_publish` permission, you can only post on behalf of Threads testers on your app."
- "Wait about 30 seconds before publishing to allow the Threads media pipeline to finish processing the upload."

Kết luận: docs **xác nhận** có đường reply vào bài public của người lạ (qua `threads_keyword_search`, với bài "recently searched"). Điểm chưa rõ:

- [Chưa kiểm chứng] "Has the permission" ở Standard Access (tester) có đủ để reply bài người lạ hay phải Advanced Access. Vì search ở Standard Access chỉ trả bài của chính mình, khả năng cao là không có bài người lạ nào ở trạng thái "recently searched". Cần spike.
- [Chưa kiểm chứng] "Recently" là bao lâu. Spec đặt delay 1,5–6 phút và có thể lâu hơn nếu hàng đợi dài.

Hạn chế từ reply audience của tác giả:

- [Nguồn] https://developers.facebook.com/docs/threads/reply-management: `reply_control` có 5 mức: everyone, accounts you follow, mentioned only, parent post author only, followers only.
- [Nguồn] https://developers.facebook.com/docs/threads/retrieve-and-manage-replies/replies-and-conversations: field `reply_audience` "only appears on top-level posts and replies that you own".
- [Suy luận] App không đọc được `reply_audience` của bài người lạ, nên không biết trước bài nào khoá reply; phải xử lý lỗi lúc tạo container. Mã lỗi cụ thể: [Chưa kiểm chứng], cần spike.

### 2.4 `/conversation` và `/insights` trên bài của người khác

`/conversation`:

- [Nguồn] https://developers.facebook.com/docs/threads/retrieve-and-manage-replies/replies-and-conversations: "`GET /{threads-media-id}/conversation` returns all replies, regardless of the depth"; "This endpoint is only intended to be used on the root-level posts with replies." Với `username` và `permalink`: "This only works for public users and your own user." Trang không nói rõ có gọi được trên media không thuộc sở hữu hay không.
- [Nguồn] https://developers.facebook.com/docs/threads/get-started: `threads_read_replies` "required for GET calls to reply endpoints". **Spec không liệt kê permission này.**
- [Nguồn] https://developers.facebook.com/docs/permissions, `threads_read_replies`: "allows a business app to read replies to a user's thread." Allowed usage: "Get replies to a thread owned by the business app user." / "It is not intended for any other purpose."
- [Nguồn] cùng trang, `threads_keyword_search` allowed usage nhắc "the public content tree".
- Kết luận: **không xác định được từ docs**. Mô tả `threads_read_replies` nghiêng về chỉ thread của chính user; cụm "public content tree" của `threads_keyword_search` gợi ý có thể đọc cây reply của bài đã search. Phải spike. Nếu không đọc được, cả Tầng 1 triage ("5 bình luận đầu tiên") và Viral Autopsy ("10 comment dưới bài Viral") mất input.

`/insights` và số đếm tương tác:

- [Nguồn] https://developers.facebook.com/docs/threads/insights: API "allows you to read the insights from users' own Threads posts and profiles." Metric media: `views`, `likes`, `replies`, `reposts`, `quotes`, `shares`. "An empty array will be returned for `REPOST_FACADE` posts because they are posts made by other users." / "Returned metrics do not capture nested replies' metrics."
- [Nguồn] https://developers.facebook.com/docs/permissions, `threads_manage_insights`: "Allow business apps to get insights for individual threads published by that profile."
- [Nguồn] https://developers.facebook.com/docs/threads/threads-media: danh sách field của media object không có like count, reply count hay views. Về bài của người khác: trước khi được duyệt "only posts created by a Threads tester are retrievable"; sau khi duyệt "public posts created by other users will be retrievable".
- [Nguồn] https://developers.facebook.com/docs/threads/threads-profiles (Profile Discovery, permission `threads_profile_discovery`): `profile_lookup` trả likes/quotes/reposts/views **cộng dồn 7 ngày của cả profile**, không theo từng bài; "Only returns public profiles with at least 100 followers."; với Standard Access chỉ tra được @meta, @threads, @instagram, @facebook; 1.000 request / 24h.

Kết luận: trong docs **không có nơi nào** lộ like/reply count của từng bài public của người khác. Hệ quả:

- [Suy luận] "Contrastive Viral Autopsy" không có cách xác định "bài flop không ai tương tác" bằng số liệu. Tín hiệu duy nhất là thứ tự `TOP` so với `RECENT` và boolean `has_replies`.
- [Suy luận] `threads_actions.likes_count` / `replies_count` chỉ điền được cho bài và reply của chính agent. Insights trên *reply* của chính mình có hoạt động như trên post hay không: [Chưa kiểm chứng], cần spike.

### 2.5 Publishing limits, thời gian chờ, độ dài text

- [Nguồn] https://developers.facebook.com/docs/threads/overview: 250 post / 24h (carousel tính 1), 1.000 reply / 24h, 100 delete, 500 location search. Rate limit lời gọi: "Calls within 24 hours = 4800 * Number of Impressions", impressions tối thiểu tính là 10.
- [Nguồn] https://developers.facebook.com/docs/threads/troubleshooting: `GET /{threads-user-id}/threads_publishing_limit` với `quota_usage`, `config`, `reply_quota_usage`, `reply_config`, `delete_quota_usage`, `delete_config`, `location_search_quota_usage`, `location_search_config`.
- [Nguồn] cùng trang, status container: `GET /{threads-container-id}?fields=status,error_message`; giá trị `EXPIRED`, `ERROR`, `FINISHED`, `IN_PROGRESS`, `PUBLISHED`. "We recommend querying a container's status once per minute, for no more than 5 minutes."
- [Nguồn] https://developers.facebook.com/docs/threads/posts: "It is recommended to wait on average 30 seconds before publishing a Threads media container." / "Text posts are limited to 500 characters." Emoji tính theo số byte UTF-8.
- [Nguồn] cùng trang: tối đa 5 link khác nhau mỗi bài (lỗi `THREADS_API__LINK_LIMIT_EXCEEDED` từ 2025-12-22).

Kết luận: 15 giây cứng là thấp hơn khuyến nghị (30 giây). Nên poll status tới `FINISHED` rồi publish. `z.string().max(450)` an toàn nhưng tiếng Việt có dấu và emoji tính theo byte: [Suy luận] cần đếm đúng đơn vị mà API dùng; cách đếm với ký tự tiếng Việt: [Chưa kiểm chứng], cần spike.

### 2.6 Token

[Nguồn] https://developers.facebook.com/docs/threads/get-started/long-lived-tokens

- Short-lived: "valid for one hour." Long-lived: "valid for 60 days."
- Refresh: `GET /refresh_access_token` với `grant_type=th_refresh_token`; token phải "at least 24 hours old but have not expired".
- Quyền do user có profile private cấp "valid for 90 days."

[Suy luận] Spec không có job refresh; agent sẽ chết im lặng sau 60 ngày. Cần lưu token + hạn trong DB và refresh định kỳ.

[Nguồn] https://developers.facebook.com/terms/?locale=en_US §7.e.iii: "We may suspend or end your App's access to any Platform APIs, permissions, or features that your App has not used or accessed within a 28-day period with or without notice to you." [Suy luận] Chế độ `SLEEPING`/`SURVIVAL` kéo dài có thể làm mất quyền.

### 2.7 Base URL và version

- [Nguồn] https://developers.facebook.com/docs/threads/changelog, mục 2025-06-06: "The Threads API can be accessed by either graph.threads.com or graph.threads.net."
- [Nguồn] Mọi ví dụ hiện tại dùng `https://graph.threads.com/v1.0/`. Changelog 2025–2026 không ghi version mới.
- Kết luận: `graph.threads.net/v1.0` vẫn hợp lệ; nên dùng `graph.threads.com` cho khớp docs.

### 2.8 Các tính năng API liên quan mà spec không dùng

- [Nguồn] https://developers.facebook.com/docs/threads/threads-mentions: `GET /{threads-user-id}/mentions` (permission `threads_manage_mentions`) "Returns a list of public Threads media objects in which a Threads profile has been tagged by another Threads profile." Trước khi duyệt: "the API returns only mentions made by a Threads tester on the app."
- [Nguồn] https://developers.facebook.com/docs/threads/webhooks: webhook `replies` (reply trên bài của user), `mentions`, `publish`, `delete`. "If the app permissions don't have an access level of advanced access, the app won't receive webhook notifications."
- [Suy luận] Trả lời người đã mention hoặc đã reply vào bài của agent là tương tác *được mời*; đây là vùng ít rủi ro chính sách nhất và spec hiện bỏ qua (dùng polling 15 phút thay vì webhook).

---

## 3. Câu 2 — Chính sách Meta

### 3.1 Điều khoản nào áp dụng

[Nguồn] Threads Terms of Use (bản mirror, cập nhật 2025-05-28): https://www.facebook.com/help/instagram/769983657850450?locale=en_US

- Threads Terms "supplement and amend the Instagram Terms of Use and the Meta Community Standards which are incorporated into these Terms by reference."
- §4(a)(ii): "all content that you upload or share must comply with the Meta Community Standards."
- §3(a): "You agree that you shall not, nor shall you cause or permit any third party to do, or attempt to do" … "(i) exploit the Threads Service for any commercial purpose" … "(iv) use any robot, spider, crawlers, scraper or other automatic device, process, software or queries that intercepts, "mines", scrapes, extracts or otherwise accesses the Threads Service".
- §2(b): "You are solely responsible for any activity on your profile".
- **Giới hạn kiểm chứng:** công cụ fetch không trả toàn văn §3(a), nên không biết các mục (i) và (iv) có ngoại lệ kiểu "except as expressly permitted" hay không. Hai mẩu trích trên đủ nghiêm trọng (mục đích thương mại, truy cập tự động) để chủ dự án phải tự đọc toàn văn trước khi quyết định. Chưa kết luận được là spec vi phạm hai mục này.

[Nguồn] Instagram Terms of Use (mirror): https://www.facebook.com/help/instagram/581066165581870?locale=en_US

- "You can't impersonate others or provide inaccurate information. You don't have to disclose your identity on Instagram, but you must provide us with accurate and up-to-date information (including registration information), which may include providing personal data. Also, you may not impersonate someone or something you aren't, and you can't create an account for someone else unless you have their express permission."
- "You can't do anything unlawful, misleading or fraudulent or for an illegal or unauthorised purpose."
- "You can't attempt to create accounts or access or collect information in unauthorised ways. This includes creating accounts or accessing or collecting information in an automated way without our express permission, regardless of whether such automated access or collection is undertaken while logged in to an Instagram account."
- Trang ghi: "We're updating our Terms of Use. These updates come into effect on 20 January 2027." Nội dung bản cập nhật: [Chưa kiểm chứng].

[Nguồn] Meta Platform Terms: https://developers.facebook.com/terms/?locale=en_US

- §1.b: "you agree to these Platform Terms ("Terms"), as well as all other applicable terms and policies."
- §3.a.v: "Processing Platform Data without valid User consent in order to build or augment user profiles for any purpose."
- §3.a.iii: "Processing Platform Data to perform, facilitate, or provide tools for surveillance."
- §3.a.viii: "Processing Platform Data for purposes other than the applicable permitted purposes set forth in Meta's Developer Docs."
- §3.d.i.1: "Update or delete Platform Data promptly after receiving a request from us or the User to do so".
- §7.e.i.2: enforcement khi "You or your App has violated or may have violated these Terms or any other applicable terms or policies"; §7.e.ii: "We may take enforcement action at any time, including while we investigate your App(s), with or without notice to you."

[Nguồn] Developer Policies: https://developers.facebook.com/devpolicy/?locale=en_US

- §1.4: "Don't confuse, deceive, defraud, mislead, spam or surprise anyone."
- §1.7: "Obtain consent from people before publishing content or taking any other action on their behalf."
- §2.7: "Don't participate in any program that promotes or facilitates the purchase, sale, or exchange of "Likes", "Shares"" (câu bị cắt ở nguồn fetch).
- §3.5: "If we determine that your App is creating a negative experience, we may require you to comply with one or more App Development Guidelines."
- Trang không có mục riêng cho Threads.

### 3.2 Spam (Community Standards)

[Nguồn, nguyên văn] https://transparency.meta.com/policies/community-standards/spam/

- Rationale: "We do not allow content that is designed to deceive, mislead, or overwhelm users in order to artificially increase viewership. This content detracts from people's ability to engage authentically on our platforms and can threaten the security, stability and usability of our services."
- "We do not allow: Posting, sharing, engaging with content or creating accounts, Groups, Pages, Events or other assets, either manually or automatically, at very high frequencies."
- "We may place restrictions on accounts that are acting at lower frequencies when other indicators of Spam (e.g., posting repetitive content) or signals of inauthenticity are present."
- "Attempting to or successfully selling, buying, or exchanging for engagement, such as likes, shares, views, follows, clicks, use of specific hashtags, etc."
- "Misleading Links: Content containing a link that promises one type of content but delivers something substantially different."
- "Notwithstanding the above, we do not prohibit: Cross promotion that is not triggered by payment to a third party".

### 3.3 Danh tính và hành vi không xác thực

[Nguồn] https://transparency.meta.com/policies/community-standards/authentic-identity-representation/

- "we do not allow for the creation of accounts or profiles that are created or used to deceive others."
- Áp dụng cho "Facebook, Instagram, and Threads accounts": "Engage in identity misrepresentation to mislead or deceive others, evade enforcement, or violate our Community Standards." Yếu tố xét: "Repeated or significant changes to identity details, such as name or age" / "Misleading profile information, such as bio details and profile location" / "Using stock imagery".
- Chỉ áp dụng cho Facebook: "Create or maintain multiple Facebook accounts"; "Create an account that represents a non-human entity, such as a business, pet, or fictional character".

[Nguồn, nguyên văn] https://transparency.meta.com/policies/community-standards/inauthentic-behavior/ — xem mục 11.

### 3.4 Gắn nhãn AI

- [Nguồn] https://developers.facebook.com/docs/threads/posts: "To disclose that a post contains AI-generated content, set `is_made_with_ai=true` when creating the media container". / "The published post displays the AI info label, as it does when you select the AI-label option in the Threads composer." / "This parameter supports text, image, video, and carousel posts." / "It defaults to `false`." / "Omitting it or setting it to `false` does not add an AI self-disclosure and does not prevent automatic AI labeling."
- [Nguồn] https://about.fb.com/news/2024/04/metas-approach-to-labeling-ai-generated-content-and-manipulated-media/: nhãn áp dụng cho "a wider range of video, audio and image content"; bài không nhắc tới văn bản. Phạm vi: "manipulated media on Facebook, Instagram and Threads".
- Kết luận: API **có** cơ chế tự khai AI cho cả bài text. Việc tự khai có **bắt buộc** với bài text thuần hay không: [Chưa kiểm chứng] (các nguồn Meta tìm được chỉ nêu bắt buộc với video/audio/hình ảnh chân thực). Nhãn dành riêng cho tài khoản tự động/bot trên Threads: [Chưa kiểm chứng], không tìm thấy.
- Spec không dùng `is_made_with_ai` ở bất kỳ đâu.

### 3.5 Đánh giá thiết kế của spec

Phần này là **[Suy luận]** từ các điều khoản đã trích.

| Yếu tố thiết kế | Điều khoản liên quan | Đánh giá |
|---|---|---|
| Reply tự động vào bài người lạ chưa từng liên hệ | Không tìm thấy điều khoản cấm riêng việc reply qua API; docs keyword search cho phép "interact with public Threads media that you have recently searched for". Nhưng: Spam ("either manually or automatically, at very high frequencies"; "lower frequencies when other indicators of Spam… or signals of inauthenticity are present"); Bullying and Harassment Tier 1: "Unwanted contact that is: Repeated, OR … Is directed at a large number of individuals with no prior solicitation." | Không bị cấm tuyệt đối. Rủi ro phụ thuộc tần suất, mức lặp lại và phản ứng của người nhận (report/block). 15–35 reply/ngày thấp hơn nhiều so với quota 1.000, nhưng điều khoản "lower frequencies" cho phép hạn chế cả ở tần suất thấp. |
| Persona "giao tiếp tự nhiên như người thật" | Instagram Terms: "you may not impersonate someone or something you aren't"; "You can't do anything unlawful, misleading or fraudulent"; Authentic Identity: "identity misrepresentation to mislead or deceive others"; Developer Policies §1.4 | Bio mẫu có nhắc "ban đêm để agent tự đi dạo Threads" và dashboard công khai, tức có tiết lộ một phần. Nhưng từng reply không mang dấu hiệu AI, và mục tiêu nêu rõ là để người đọc tin là người thật. Có khả năng bị coi là misleading. |
| Jitter 1,5–6 phút "thời gian đọc giả lập" | Không có điều khoản nào nêu tên jitter/delay. Liên quan gián tiếp: Spam ("signals of inauthenticity"), Inauthentic Behavior ("operators use adversarial methods to evade detection or appear authentic" — thuộc định nghĩa CIB, đòi hỏi "false identities are central to the operation") | Delay để giãn tải thì bình thường. Delay với mục đích ghi rõ trong spec là "giả lập" con người làm lộ ý định che giấu tự động hoá; đây là bằng chứng bất lợi nếu bị xem xét. Bản thân việc trễ không vi phạm điều khoản nào tìm được. |
| Plug link app (tự reply dưới bài mình; gợi ý app trong reply bài người khác, ≤2 lần/ngày) | Spam: chỉ cấm link lừa dối/misleading và mua bán engagement; "we do not prohibit: Cross promotion that is not triggered by payment to a third party". Threads Terms §3(a)(i) "exploit the Threads Service for any commercial purpose" (chưa đọc được toàn văn) | Link thật tới app thật, không lừa dối: không khớp các mục cấm của chính sách Spam. Mục §3(a)(i) của Threads Terms cần đọc toàn văn. |
| Không gắn nhãn AI | `is_made_with_ai` tồn tại, hỗ trợ text; tính bắt buộc với text chưa kiểm chứng | Bật `is_made_with_ai=true` là cách giảm rủi ro rẻ nhất; nó mâu thuẫn trực tiếp với mục tiêu "như người thật" của spec. |
| Dashboard công khai hiển thị @username người lạ, lý do từ chối ("Nội dung bán khóa học lùa gà"), và bảng `relationship_graph.vibe_summary` | Platform Terms §3.a.v ("build or augment user profiles" không có consent), §3.a.viii, §3.d.i.1 (xoá theo yêu cầu) | Đây là rủi ro spec hoàn toàn không nhắc tới. Lưu hồ sơ "vibe" về người dùng không cấp quyền và phát công khai nhận xét tiêu cực có gắn tên là khó bảo vệ nhất trong toàn bộ thiết kế. |
| Câu cảnh báo "CAUTION: chỉ dùng Official API… tránh khóa tài khoản và vi phạm chính sách" | §1.b Platform Terms; §4(a)(ii) Threads Terms | Lập luận của spec sai: dùng API chính thức tránh được vi phạm về scraping, nhưng nội dung và hành vi vẫn chịu Community Standards và Developer Policies. |

---

## 4. Câu 3 — OpenRouter và AI SDK (ghi nhận ngắn)

- **`GET /api/v1/credits`**: [Nguồn] https://openrouter.ai/docs/api/api-reference/credits/get-credits — tồn tại; "Management key required"; trả `{"data": {"total_credits": …, "total_usage": …}}`. Gọi không có key trả 401 (đã thử). [Suy luận] Số dư = `total_credits − total_usage`.
- [Nguồn] https://openrouter.ai/docs/guides/overview/auth/management-api-keys — management key "cannot be used to make API calls to OpenRouter's completion endpoints". [Suy luận] Spec cần 2 key, hoặc dùng `GET /api/v1/key` với key thường ([Nguồn] https://openrouter.ai/docs/api/reference/limits: trả `limit`, `limit_remaining`, `usage`, `usage_daily`).
- **Model ID và giá** — [Nguồn] `GET https://openrouter.ai/api/v1/models` (469 model, đã gọi trực tiếp), giá USD / 1M token input / output:
  - `google/gemini-2.5-flash-lite`: tồn tại, $0.10 / $0.40.
  - `deepseek/deepseek-chat`: tồn tại (tên "DeepSeek V3"), $0.32 / $0.89.
  - `google/gemini-2.5-pro`: tồn tại, $1.25 / $10.00.
  - `anthropic/claude-3.5-sonnet`: **không có trong danh sách**; https://openrouter.ai/anthropic/claude-3.5-sonnet trả 404. Ngày và lý do gỡ: [Chưa kiểm chứng]. Các Sonnet đang được liệt kê: `anthropic/claude-sonnet-5.5` ($2 / $10), `anthropic/claude-sonnet-5` ($2 / $10), `anthropic/claude-sonnet-4.6` ($3 / $15), `anthropic/claude-sonnet-4.5` ($3 / $15).
  - Không model nào trong 3 model còn lại có `expiration_date`.
- **AI SDK**: [Nguồn] npm registry — `ai` latest 7.0.169; `@openrouter/ai-sdk-provider` latest 3.1.0, peer `ai ^7.0.0`: tương thích.
- **`generateObject`**: [Nguồn] https://ai-sdk.dev/docs/migration-guides/migration-guide-6-0 — "`generateObject` and `streamObject` have been deprecated." / "They will be removed in a future version." / "Use `generateText` and `streamText` with an `output` setting instead." Hướng dẫn migrate 7.0 (https://ai-sdk.dev/docs/migration-guides/migration-guide-7-0) vẫn nhắc `generateObject` trong danh sách hàm, tức chưa bị gỡ ở v7. Kết luận: còn chạy, không còn là API được khuyến nghị.

## 5. Câu 4 — Stack (ghi nhận ngắn)

- **`sqlite-vec` + `bun:sqlite`**: [Nguồn] https://alexgarcia.xyz/sqlite-vec/js.html có recipe `sqliteVec.load(db)` cho Bun, kèm chú thích "MacOS *might* have to do this, as the builtin SQLite library on MacOS doesn't allow extensions" và gọi `Database.setCustomSQLite(...)`.
- [Nguồn] https://bun.com/docs/runtime/sqlite: "By default, macOS ships with Apple's proprietary build of SQLite, which doesn't support extensions."; phải gọi `Database.setCustomSQLite(path)` "before creating any `Database` instances". Ngoài macOS, SQLite được link tĩnh và `setCustomSQLite` không làm gì.
- Linux/Docker: [Chưa kiểm chứng] bằng chạy thật (máy không có Bun). Có một issue bên thứ ba báo `bun:sqlite` không load được extension (https://github.com/Soul-Brews-Studio/arra-oracle-v3/issues/3046, nguồn thứ cấp, chưa đối chiếu). Đưa vào danh sách spike.
- `sqlite-vec` latest 0.1.9 (npm), tức chưa 1.0.
- **Drizzle**: [Nguồn] https://orm.drizzle.team/docs/connect-bun-sqlite — "Drizzle ORM natively supports `bun:sqlite` module." Cột vector: [Nguồn] https://alexgarcia.xyz/sqlite-vec/features/knn.html — vector nằm trong virtual table `vec0` hoặc cột BLOB thường. Hỗ trợ gốc của Drizzle cho `vec0`: không tìm thấy; [Suy luận] phải dùng `customType` (BLOB) hoặc SQL thô.
- **"< 250MB RAM"**: [Chưa kiểm chứng]. Không tìm được số đo chính thức và không đo được. Lưu ý sơ đồ trong spec ghi "VPS (2GB RAM)" trong khi mục tiêu ghi "< 250MB".

## 6. Câu 5 — Nguồn trend

| Nguồn | Tồn tại? | Điều khoản |
|---|---|---|
| Google Trends VN RSS | [Nguồn, đã thử] `https://trends.google.com/trending/rss?geo=VN` trả HTTP 200, `text/xml` | Tài liệu chính thức và điều khoản dùng cho feed này: [Chưa kiểm chứng] |
| Product Hunt RSS | [Nguồn, đã thử] `https://www.producthunt.com/feed` trả HTTP 200, Atom | [Nguồn] https://api.producthunt.com/v2/docs: "The Product Hunt API must not be used for commercial purposes." Trang không nhắc RSS; RSS có chịu điều khoản này không: [Chưa kiểm chứng] |
| Hacker News API | [Nguồn, đã thử] `https://hacker-news.firebaseio.com/v0/topstories.json` trả HTTP 200 | [Nguồn] https://github.com/HackerNews/API: "There is currently no rate limit." |
| Reddit | [Đã thử] `https://www.reddit.com/r/SideProject/top.json` không kết nối được từ máy này (không rõ do chặn hay do mạng) | Xem dưới |
| Jina Reader | [Nguồn, đã thử] `https://r.jina.ai/<url>` trả HTTP 200 không cần key | [Nguồn] https://jina.ai/reader/: 20 RPM không key; 500 RPM với key; "Every new API key comes with 10M free tokens!" |

Reddit, [Nguồn, nguyên văn] https://support.reddithelp.com/hc/en-us/articles/42728983564564-Responsible-Builder-Policy:

- "Approval is required: You must request access and get explicit approval before accessing any Reddit data through our API, and you must agree to comply with all applicable terms."
- "If you'd like to use Reddit data for commercial purposes, you'll need to get explicit written approval."
- "You must not sell, license, share, or otherwise commercialize Reddit data without express written approval. This extends to commercial and non-commercial mining, scraping, or using data for purposes like ads targeting or to train machine learning or AI models."
- "Apps must not engage in spamming activity through automated posts, comments, or direct messages."

[Nguồn, nguyên văn] https://redditinc.com/policies/data-api-terms ("Effective June 19, 2023. Last Revised July 20, 2026"):

- "If you are interested in using the Data APIs for commercial purposes, research in excess of rate limits, or for any use that is not expressly permitted under the Data API Terms, then you will need to enter into a separate agreement with Reddit."
- "you will not misrepresent or mask either the user agent or OAuth identity when using the Data APIs."

[Suy luận] Agent có mục tiêu kéo traffic về app trả phí là mục đích thương mại; dùng Reddit làm nguồn cần phê duyệt và thoả thuận riêng. Nên bỏ Reddit khỏi phạm vi ban đầu. Ba nguồn còn lại dùng được về kỹ thuật; hai trong số đó chưa rõ điều khoản.

---

## 7. Câu 6 — Vòng lặp tự học

### 7.1 Thompson sampling và "80/20" không phải một thứ

- [Nguồn] Russo et al., "A Tutorial on Thompson Sampling", https://arxiv.org/abs/1707.02038: "Thompson sampling is an algorithm for online decision problems where actions are taken sequentially" trong đó phải cân bằng giữa khai thác hiểu biết hiện có và thu thập thông tin mới.
- [Suy luận, kiến thức chuẩn] Thompson sampling: mỗi lượt lấy một mẫu từ `Beta(alpha, beta)` của từng arm và chọn arm có mẫu lớn nhất. Tỷ lệ explore không cố định; nó tự giảm khi posterior hẹp lại. Tách cố định "80% chọn công thức đang thắng / 20% thử cái mới" là epsilon-greedy với ε = 0,2. Spec mô tả cả hai trong cùng một đoạn (dòng 279–281); người triển khai phải chọn một.
- [Suy luận] Thêm nữa, 20% "explore" của spec không phải chọn arm ít dữ liệu mà là *tạo arm mới* từ `viral_autopsies`; số arm K tăng dần, làm dữ liệu mỗi arm càng mỏng.

### 7.2 Cần bao nhiêu ngày để phân biệt các arm

Toàn bộ mục này là **[Suy luận / phép tính]**.

Giả định: phần thưởng nhị phân ("nổ" hay "flop", đúng như `alpha_wins` / `beta_losses`); 25 reply/ngày (giữa khoảng 15–35); K arm chia đều; kiểm định hai tỷ lệ, α = 0,05 hai phía, power 80%, nên `n mỗi arm ≈ 7,84 × (p1(1−p1) + p2(1−p2)) / (p1 − p2)²`.

| Tỷ lệ "nổ" arm kém → arm tốt | n mỗi arm | K = 3 (≈8,3 lượt/arm/ngày) | K = 5 (5 lượt/arm/ngày) |
|---|---|---|---|
| 10% → 20% | ≈ 196 | ≈ 24 ngày | ≈ 39 ngày |
| 10% → 15% | ≈ 682 | ≈ 82 ngày | ≈ 136 ngày |

Ghi chú:

- Thompson sampling dồn lượt cho arm đang dẫn nên tìm arm tốt nhất nhanh hơn chia đều, nhưng không đổi bậc độ lớn: vài tuần tới vài tháng, không phải "từng ngày".
- Spec muốn phản ứng theo "tuần này". Trong 7 ngày với K = 5, mỗi arm có ≈ 35 lượt; ở p = 0,15 sai số chuẩn ≈ 0,06, khoảng tin cậy 95% ≈ ±0,12. Không phân biệt được 10% với 20%.
- Quy tắc "5 lần liên tiếp flop → bỏ thói quen": với arm tốt có tỷ lệ nổ 20%, xác suất 5 lần flop liên tiếp là 0,8⁵ ≈ 33%. Quy tắc này sẽ loại arm tốt khoảng một phần ba số lần.
- Nếu dùng phần thưởng liên tục (số like) thay vì nhị phân: tương tác mạng xã hội có đuôi nặng, trung bình mẫu bị chi phối bởi vài bài ngoại lệ, cần nhiều mẫu hơn nữa. Spec không định nghĩa ngưỡng "nổ".
- Biến nhiễu lớn nhất không nằm trong mô hình: tương tác của một reply phụ thuộc chủ yếu vào reach của bài gốc và thời điểm vào comment, không phải công thức viết. Spec không lưu reach bài gốc (và theo mục 2.4, API không cung cấp).
- `threads_actions` không có cột `strategy_id`, nên không thể cập nhật bandit từ kết quả (xem mục 9).

### 7.3 Bằng chứng đã công bố

- **LLM dự đoán tương tác**: [Nguồn] El-amrany et al., "Reddit-V", RANLP 2025, https://aclanthology.org/2025.ranlp-1.41: "Our results show that zero-shot LLMs perform poorly". Kết quả tìm kiếm còn nêu arXiv 2510.08481 (LLM không đáng tin cho dự đoán số trực tiếp) và arXiv 2505.03769 (con người dự đoán độ phổ biến chỉ nhỉnh hơn đoán mò); hai bài này chỉ thấy qua tóm tắt tìm kiếm, **chưa đọc bản gốc**.
- Không tìm thấy nghiên cứu nào về LLM dự đoán tương tác của *chính bài nó viết* trên Threads: [Chưa kiểm chứng].
- **LLM tự phát hiện văn "AI"**: [Nguồn] Ji et al., "I know myself better, but not really greatly", https://arxiv.org/abs/2502.12743: "self-detection (LLMs identifying their own outputs) consistently outperforms cross-detection" nhưng "both remain suboptimal"; lỗi giải thích gồm "inaccurate features, hallucinations, and flawed reasoning".
- [Suy luận] `predicted_engagement_score` 1–10 với ngưỡng |Delta| ≥ 4 và `sounds_like_ai_slop` / `human_naturalness_score ≥ 8` đều dựa trên năng lực mà các nghiên cứu trên cho là yếu. Danh sách cụm từ cấm trong `character.json` kiểm tra bằng so khớp chuỗi sẽ đáng tin hơn tự chấm điểm. Ngoài ra `criticAndVerifyGate` vừa chọn, vừa sửa (`final_polished_text`), vừa chấm chính bản nó sửa; không có bước kiểm tra độc lập sau khi sửa.

---

## 8. Câu 7 — Kinh tế

### 8.1 Chi phí LLM mỗi ngày

Toàn bộ mục này là **[Suy luận / phép tính]** trên giá ở mục 4. Spec không nêu số token hay số candidate, nên mọi con số dưới đây là giả định.

Giả định token mỗi lượt: triage 1.200 in / 120 out; composer 3.000 in / 400 out (3 variants); critic 1.500 in / 250 out; autopsy 2.500 in / 400 out; phân tích sai số dự đoán 2.000 in / 300 out; dreaming 6.000 in / 1.500 out. Chưa tính reasoning token, retry, embedding (spec không nêu model embedding) và phí nạp credit.

| Bước | Kịch bản A — tiết kiệm | Kịch bản B — theo spec | Kịch bản C — critic/autopsy trên model đắt |
|---|---|---|---|
| Triage (`gemini-2.5-flash-lite`) | 150 bài: $0.025 | 300 bài: $0.050 | 300 bài: $0.050 |
| Composer | 22 lượt `gemini-2.5-pro`: $0.17 | 42 lượt `claude-sonnet-5.5`: $0.42 | 42 lượt `claude-sonnet-5.5`: $0.42 |
| Critic | flash-lite: $0.006 | flash-lite: $0.011 | sonnet-5.5: $0.231 |
| Autopsy (10/ngày) | flash-lite: $0.004 | flash-lite: $0.004 | sonnet-5.5: $0.090 |
| Phân tích sai số (5/ngày) | bỏ | bỏ | sonnet-5.5: $0.035 |
| Dreaming | $0.023 | $0.027 | $0.027 |
| **Tổng / ngày** | **≈ $0.23** | **≈ $0.51** | **≈ $0.85** |

- 42 lượt composer = 20 reply mục tiêu + 2 post, nhân đôi vì critic từ chối khoảng một nửa (giả định; spec đặt 3 điều kiện chặn khá gắt). Kịch bản A giả định không bị từ chối lần nào.
- Nếu dùng `claude-sonnet-4.6` ($3 / $15) cho composer: $0.63 thay cho $0.42.
- So với spec: "$0.18/day" thấp hơn cả kịch bản tiết kiệm nhất. `max_budget_usd: 0.35` chỉ đạt được ở kịch bản A. Kịch bản bám sát spec (B) vượt ngân sách khoảng 45%.
- Spec chỉ tính số dư OpenRouter vào "runway". Chi phí VPS được nêu ở mục tiêu (dòng 14) nhưng không có trong `ledger_metrics` hay công thức runway. Giá VPS: [Chưa kiểm chứng], không tra.
- Bậc độ lớn: $7–26/tháng tiền LLM. Con số này nhỏ; rủi ro kinh tế thật là phía doanh thu (không có ước lượng nào trong spec), không phải phía chi phí.

### 8.2 Attribution qua Dub.co khi không đặt link trong bài

- [Nguồn] https://dub.co/docs/conversions/quickstart: script Dub Analytics trên site đích "detects the `dub_id` query parameter and storing it as a first-party cookie"; sale đi qua tích hợp Stripe/Shopify hoặc `POST /track/sale`; server-side tracking: "This feature is only available on Business plans and above."
- [Nguồn] Changelog Threads 2025-07-02: metric `clicks` được thêm vào Insights (https://developers.facebook.com/docs/threads/changelog). Cấp độ (user hay media) và cách dùng: [Chưa kiểm chứng].
- [Suy luận] Chế độ 1 (link trong reply tự đăng dưới bài của mình): gán được click cho *bài* đó nếu mỗi bài dùng một short link riêng. Spec chỉ có một `tracking_url` cho mỗi app, nên chỉ gán được tới mức app.
- [Suy luận] Chế độ 2 ("gợi ý tên App hoặc mời vào trang cá nhân xem"): không có link trong reply, người dùng đi qua profile hoặc tự tìm kiếm; không thể gán về reply cụ thể. Phần lớn 15–35 reply/ngày thuộc loại này, nên `revenue_attributed_usd` và vòng "tự nuôi sống" gần như không đo được ở mức hành động.
- [Suy luận] Conversion cần script Dub trên site của từng app và tích hợp Stripe; đây là việc ngoài repo ThreadWalker mà spec không nêu. Sơ đồ có "Stripe Webhook" nhưng không có bảng hay handler nào nhận.

---

## 9. Câu 8 — Mâu thuẫn nội bộ trong spec

Số dòng theo `handoff_spec.md`.

1. **Số bảng**: "khởi tạo 7 bảng cốt lõi" (dòng 106) nhưng liệt kê 9 bảng (108–130).
2. **Enum trạng thái không khớp nhau**: `agent_state.status` = `AWAKE | SLEEPING | STARVING` (109); `survival_mode` = `THRIVING | NORMAL | SURVIVAL` (110); chú thích `state-machine.ts` = `THRIVING | NORMAL | STARVING | SLEEPING` (84); §5.5 dùng `THRIVING / NORMAL / SURVIVAL` (241–243). `STARVING` và `SURVIVAL` chỉ cùng một thứ; `STARVING` nằm trong `status` lẫn với trục thức/ngủ.
3. **Vị trí state machine**: cây thư mục đặt ở `src/agent/state-machine.ts` (84); §5.5 ghi "Survival State Machine (`src/connectors/finance.ts`)" (239).
4. **Cột không tồn tại**: `predicted_engagement_score`, lý do dự đoán, `actual_engagement_score` (271–272) không có trong bảng nào.
5. **Thiếu khoá nối bandit**: `threads_actions` không có `strategy_id`; không thể cộng `alpha_wins`/`beta_losses` cho đúng arm.
6. **Thiếu cột metric**: `getPostInsights` lấy `views, reposts, quotes` nhưng `threads_actions` chỉ có `likes_count`, `replies_count`; điều kiện auto-plug dùng `views >= 1000` (204).
7. **Tên cột**: `Clean_facts_markdown` (116) so với `clean_facts_markdown` (180).
8. **Schema autopsy lệch nhau**: bảng `viral_autopsies` có `hook_formula`, `trigger_psychology`, `why_flop_failed` (124); schema hàm có `hook_anatomy`, `comment_bait_trigger`, `flop_root_cause` (265–267).
9. **Số comment đọc**: "10 bình luận đầu tiên" (148), "5 bình luận đầu tiên" (164), "10 comment dưới bài Viral" (264).
10. **Permission thiếu**: danh sách (141) không có `threads_read_replies`, trong khi `getConversationContext` cần nó (mục 2.4).
11. **Thompson sampling so với 80/20** (256, 279–281): xem mục 7.1.
12. **"Flop không ai tương tác"** (263) cần số đếm tương tác của bài người lạ; `keyword_search` không trả (mục 2.4).
13. **RAM**: "< 250MB RAM" (21) so với "VPS (2GB RAM)" trong sơ đồ (37).
14. **Frontend chưa quyết**: "Next.js (Static Export) hoặc Vite + React" (32); cây thư mục ghi "React/Vite/Tailwind" (95).
15. **Ngân sách**: `max_budget_usd: 0.35` ở chế độ `NORMAL` (224) so với burn rate hiển thị "$0.18/day" (292). Không rõ `max_budget_usd` là trần hay mục tiêu, và cái gì cưỡng chế nó.
16. **Runway chỉ tính OpenRouter**: mục tiêu nói theo dõi cả "chi phí VPS" (14); `ledger_metrics` chỉ có `llm_cost_usd` (128).
17. **Model**: `THRIVING` dùng `claude-3.5-sonnet` (241), model đã bị gỡ khỏi OpenRouter. Critic, autopsy, dreaming không ghi tier model.
18. **Không có model embedding**: `dream_lessons.embedding` (122) cần embedding, spec không nêu model hay số chiều.
19. **Không có job refresh token** và không có chỗ lưu token/hạn (mục 2.6).
20. **Sleep 15 giây** (151) so với khuyến nghị 30 giây và endpoint status của docs.
21. **Mục tiêu "như người thật"** (12) so với bio mẫu tự nhận có agent (313) và dashboard công khai liệt kê mọi hành động kèm link "View on Threads" (299). Spec không quyết định tài khoản có tiết lộ là AI hay không.
22. **"Tuân thủ tuyệt đối"** (139) so với jitter "thời gian đọc giả lập" (238) và dashboard công khai @username + nhận xét tiêu cực về người lạ (296): xem mục 3.5.
23. **Phase 1 "1–2 giờ"** (365) giả định `keyword_search` và reply bài người lạ chạy ngay; cả hai phụ thuộc App Review (mục 2.2).
24. **Ngưỡng điểm**: `relevance_score < 7.5` và `human_naturalness_score < 8` trên thang do LLM tự chấm, không có bộ hiệu chuẩn nào. Phase 2 nói "tinh chỉnh prompt trên 20 bài" nhưng không có tiêu chí đạt.
25. **`AppTrack → SSE`** trong sơ đồ (54): webhook click/Stripe đi vào SSE broadcaster; không có endpoint nhận webhook nào trong `server/app.ts` (93).
26. **`reply_patrol` mỗi 15 phút** (204) không xuất hiện trong `scheduler.ts` hay bất kỳ file nào của cây thư mục, và không nói có chạy ngoài Active Window hay không.

---

## 10. Câu 9 — Prior art

- **ElizaOS và Threads**: [Nguồn, GitHub API] repo https://github.com/elizaOS/eliza (≈19,8 nghìn sao, push gần nhất 2026-11-11). Thư mục `plugins/` có 81 mục, gồm `plugin-x`, `plugin-discord`, `plugin-telegram`, `plugin-slack`; **không có plugin Threads**. README không nhắc Threads. Org `elizaOS` có 20 repo public, không repo nào tên liên quan Threads. Org `elizaos-plugins` hiện trả 0 repo public. Có plugin Threads do cộng đồng viết ở nơi khác hay không: [Chưa kiểm chứng].
- **Điều gì xảy ra với tài khoản của họ**: nguồn tìm được là báo chí (thứ cấp), không phải hồ sơ toà. Theo The Block (https://www.theblock.co/post/368744/ai-startup-eliza-labs-sues-x) và Decrypt (https://decrypt.co/337382/eliza-labs-sues-x-accuses-elon-musk-platform-copying-ai): Eliza Labs và nhà sáng lập Shaw Walters kiện X Corp ngày 2025-08-27 tại toà liên bang Northern District of California; đơn kiện cáo buộc tài khoản X của Eliza Labs và của Walters bị đình chỉ sau khi họ từ chối gói enterprise license khoảng $50.000/tháng. Đây là cáo buộc của nguyên đơn. Các nguồn bất nhất về ngày đình chỉ. Kết quả vụ kiện: [Chưa kiểm chứng].
- [Suy luận] Bài học áp dụng được: nền tảng có thể cắt quyền của một dự án agent mã nguồn mở ở cấp tài khoản lẫn cấp API mà không cần báo trước (khớp với Platform Terms §7.e.ii ở mục 3.1). Một dự án sống nhờ một tài khoản và một app duy nhất không có đường lui.
- Các agent mã nguồn mở khác trên Threads và số phận tài khoản của chúng: [Chưa kiểm chứng], không tìm được nguồn gốc.

---

## 11. Câu 10 — Nhiều tài khoản do một người vận hành

### 11.1 Về kỹ thuật

- [Nguồn] https://developers.facebook.com/docs/threads/get-started/get-access-tokens-and-permissions: "The Authorization Window allows app users to grant your app permissions and short-lived Threads user access tokens." Token gắn với user đã uỷ quyền; phản hồi trả kèm `user_id`. Trang không nêu giới hạn số user trên một app.
- [Nguồn] https://developers.facebook.com/docs/threads/get-started: tester (thêm ở App roles → Threads Tester, chấp nhận trong Account Settings của Threads) cấp quyền được mà không cần App Review.
- [Nguồn] Giới hạn theo user: 2.200 search / 24h "applies to a user across apps"; quota publish cũng theo user.
- Kết luận: **một app giữ nhiều token cho nhiều tài khoản là đúng với cơ chế của API**. Số tester tối đa: [Chưa kiểm chứng]. Số tài khoản Instagram/Threads một người được tạo: Help Center của Instagram (https://help.instagram.com/1682672155283228, chỉ thấy qua kết quả tìm kiếm, bài cũ) nói thêm được tối đa 5 tài khoản trong app; con số hiện hành và quy định cho Threads: [Chưa kiểm chứng].
- [Suy luận] Mọi tài khoản dùng chung một app ID và một Business đã verify, nên Meta thấy trực tiếp chúng cùng một người vận hành. Vi phạm của một persona có thể kéo theo cả app (Platform Terms §7.e.ii: "We may take enforcement action at any time, including while we investigate your App(s), with or without notice to you."; bản tiếng Việt của cùng mục nói biện pháp có thể gồm tạm ngừng hoặc gỡ vĩnh viễn app và tài khoản).

### 11.2 Về chính sách

[Nguồn, nguyên văn] https://transparency.meta.com/policies/community-standards/inauthentic-behavior/

- "Inauthentic Behavior refers to a variety of complex forms of deception, performed by a network of inauthentic assets controlled by the same individual or individuals, with the goal of deceiving Meta or our community or to evade enforcement under the Community Standards."
- "We do not allow: The creation, use, or claimed use of Inauthentic Meta Assets (Accounts, Pages, Groups, etc.) in order to: Deceive Meta or our users about the identity, or origin of an audience or the entity that they represent" / "To Evade enforcement under the Community Standards."
- CIB: "particularly sophisticated forms of Inauthentic Behavior where false identities are central to the operation and operators use adversarial methods to evade detection or appear authentic".
- "When we identify these networks, we remove the fake accounts, Pages, Groups, or other Meta assets directly involved in the operation."
- Ghi chú: công cụ fetch báo rằng một phiên bản trước (2025-12-11) có các mục "Inauthentic Distribution: Using a connected network of inauthentic Meta assets to increase the distribution of content" và "Inauthentic Engagement: … fake engagement in ways designed to look authentic, in order to deceive Meta and its users about the popularity of content". Bản thô tải hôm nay không còn hai mục này. Việc chúng bị bỏ hay gộp: [Chưa kiểm chứng].

[Nguồn] https://transparency.meta.com/policies/community-standards/authentic-identity-representation/

- Điều khoản cấm nhiều tài khoản ("Create or maintain multiple Facebook accounts") **chỉ áp dụng cho Facebook**. Với Threads, điều khoản áp dụng là: "Engage in identity misrepresentation to mislead or deceive others, evade enforcement, or violate our Community Standards."
- Tài khoản fan/parody/satire được phép nếu "The account must make its unofficial status abundantly clear at minimum in the account name".

[Nguồn] Spam: cấm "selling, buying, or exchanging for engagement"; cho phép "Cross promotion that is not triggered by payment to a third party".

[Nguồn] Instagram Terms: "you may not impersonate someone or something you aren't".

### 11.3 Đánh giá

**[Suy luận]** từ các điều khoản trên:

- **Bản thân việc có nhiều tài khoản Threads không bị cấm** theo văn bản hiện hành (mục cấm nhiều tài khoản chỉ ghi cho Facebook).
- **Điều quyết định là sự lừa dối.** Nhiều tài khoản "trình diện như những người khác nhau, không liên quan" khớp gần như nguyên văn định nghĩa: "a network of inauthentic assets controlled by the same individual… with the goal of deceiving… our community" và "Deceive… our users about the identity… or the entity that they represent".
- **Có, việc các tài khoản tương tác với nhau hoặc boost cùng link là yếu tố tăng nặng.** Nó biến "nhiều tài khoản" thành "network" phối hợp tạo ấn tượng sai về mức độ ủng hộ; các mục "Inauthentic Distribution / Engagement" của phiên bản trước mô tả đúng hành vi này. Hai persona cãi nhau có dàn dựng để kéo tương tác cũng rơi vào đây.
- **Phương án ít rủi ro hơn**: mỗi tài khoản ghi rõ trong tên/bio là agent tự động và cùng một chủ; không tương tác, không repost, không plug link chéo cho nhau; `is_made_with_ai=true`. Ngay cả vậy, đây là diễn giải của người viết báo cáo, không phải cam kết của Meta.
- Điều khoản nào nói rõ "một operator được chạy N tài khoản tự động qua API": [Chưa kiểm chứng], không tìm thấy.

---

## 12. Câu 11 — Persona khiêu khích ("toxic")

### 12.1 Bullying and Harassment

[Nguồn, nguyên văn] https://transparency.meta.com/policies/community-standards/bullying-harassment/ (phiên bản hiện hành; changelog gần nhất 2026-10-29)

- Phạm vi: "We do not tolerate this kind of behavior because it prevents people from feeling safe and respected on Facebook, Instagram, and Threads."
- Phân biệt đối tượng: "We distinguish between public figures and private individuals because we want to allow discussion, which often includes critical commentary of people who are featured in the news or who have a large public audience." Public figure gồm "people with over one million fans or followers on social media".
- "For private individuals, our protection goes further: We remove content that's meant to degrade or shame".
- Tier 1, mọi người đều được bảo vệ khỏi: "Unwanted contact that is: Repeated, OR Sexually harassing, OR Is directed at a large number of individuals with no prior solicitation." / "Calls for, or statements of intent to engage in, bullying and/or harassment."
- Tier 2 (private adults, minors, limited scope public figures): "Dehumanizing comparisons…", "Content that ranks them based on physical appearance or character traits."
- Tier 3, "private adults (who must self-report)": "Targeted cursing." / "Negative character or ability claims, except in the context of criminal allegations and business reviews against adults." / "Expressions of contempt, disgust, or content rejecting the existence of an individual".
- Mass harassment: "Disable accounts engaged in mass harassment as part of either… Adversarial networks targeting any individual via any surface with: Content that violates the bullying and harassment policies for private individuals".

### 12.2 Hateful Conduct

[Nguồn, nguyên văn] https://transparency.meta.com/policies/community-standards/hateful-conduct/

- "That is why we don't allow hateful conduct on Facebook, Instagram, or Threads."
- "We define hateful conduct as direct attacks against people — rather than concepts or institutions — on the basis of what we call protected characteristics (PCs): race, ethnicity, national origin, disability, religious affiliation, caste, sexual orientation, sex, gender identity, and serious disease."
- Tier 2 gồm "Insults, including those about: Character… Mental characteristics, including but not limited to allegations of stupidity, intellectual capacity…" và "Targeted cursing" khi nhắm vào người theo protected characteristic.
- Ngoại lệ có điều kiện: "Content attacking concepts, institutions, ideas, practices, or beliefs associated with protected characteristics, which are likely to contribute to imminent physical harm, intimidation or discrimination" (cần thêm ngữ cảnh mới xử lý).
- Satire: "Content will only be allowed if the violating elements of the content are being satirized or attributed to something or someone else in order to mock or criticize them."

### 12.3 Spam và engagement bait

- [Nguồn] Chính sách Spam hiện hành định nghĩa các hành vi kiểu engagement bait là *ép buộc* tương tác: "Requiring or claiming that users are required to engage with content (e.g., liking, sharing) before they are able to view or interact with promised content." và "Like/share-gating". Rationale: "content that is designed to deceive, mislead, or overwhelm users in order to artificially increase viewership."
- Nội dung "cố tình gây tranh cãi để kéo reply" (rage bait) **không được nêu tên** trong các trang Community Standards đã đọc. Việc thuật toán Threads có hạ phân phối loại nội dung này hay không: [Chưa kiểm chứng].
- Không tìm thấy điều khoản nào xử lý riêng "reply *tự động* mang tính xúc phạm". Các chính sách trên áp dụng bất kể người hay máy viết; tự động hoá chỉ thêm điều khoản tần suất của Spam ("either manually or automatically").

### 12.4 Đánh giá

**[Suy luận]** từ các điều khoản trên:

- **Chính sách có phân biệt ý tưởng với con người, và ranh giới đó rõ.** Phản bác gay gắt một quan điểm, một sản phẩm, một công ty, một xu hướng ("vibe coding là trò đùa") không khớp mục cấm nào đã trích, miễn không nhắm vào protected characteristic. Chế giễu *người viết* thì khớp.
- **Đối tượng reply của agent phần lớn là private adults** (dưới 1 triệu follower). Với họ, Tier 3 cấm đúng những thứ một persona "toxic" sẽ sinh ra: chửi nhắm đích danh, nhận định tiêu cực về năng lực/tính cách ("ông này không biết code"), biểu lộ khinh miệt. Tier 3 yêu cầu người bị nhắm tự report; mỗi reply là một lần mời report.
- **Tự động hoá làm rủi ro tăng theo cấp số**: cùng một kiểu xúc phạm lặp lại trên hàng chục người lạ mỗi ngày tiến gần "Unwanted contact… directed at a large number of individuals with no prior solicitation" (Tier 1, không cần self-report) và mô tả mass harassment.
- **Kết hợp với câu 10**: persona toxic và persona "tử tế" cùng một chủ, cùng plug một bộ app, nếu tương tác với nhau là dàn dựng xung đột bằng mạng tài khoản; nếu persona toxic bị khoá, hệ quả lan sang app và các tài khoản còn lại.
- **LLM là critic không đủ làm chốt chặn** cho ranh giới ý tưởng/con người (mục 7.3 cho thấy tự đánh giá của LLM không đáng tin), và ngữ cảnh tiếng Việt (mỉa mai, xưng hô) làm khó thêm. Đây là nhận định, chưa có số đo.
- **Phương án còn bảo vệ được**: persona "phản biện gay gắt ý tưởng" chỉ đăng trên tường của chính nó hoặc trả lời người đã mention/reply nó; cấm ngôi thứ hai mang tính đánh giá con người; `is_made_with_ai=true`. Vẫn là diễn giải, không phải bảo đảm.

---

## 13. Những điểm phải chốt bằng spike API thật

Xếp theo mức độ quyết định dự án có làm được hay không.

1. **App Review**: use case "agent tự động search rồi reply bài public" có được duyệt `threads_keyword_search` + `threads_manage_replies` + `threads_content_publish` (+ `threads_read_replies`, `threads_manage_insights`) ở Advanced Access không; cần Business Verification gì; mất bao lâu. Đây là điều kiện tiên quyết; mọi thứ khác vô nghĩa nếu bị từ chối.
2. **Standard Access làm được gì**: với tài khoản tester, `keyword_search` có đúng là chỉ trả bài của mình không; có reply được vào bài người lạ bằng media ID lấy từ nguồn khác (permalink) không.
3. **`/{media-id}/conversation` và `/replies` trên bài người lạ** (sau khi có Advanced Access): trả dữ liệu, mảng rỗng hay lỗi; cần permission nào.
4. **Số đếm tương tác của bài người lạ**: xác nhận không có field nào (thử `fields=like_count,...` trên media object và `/insights` trên bài người lạ) để chốt việc bỏ hoặc thiết kế lại Viral Autopsy.
5. **"Recently searched"**: reply còn được chấp nhận bao lâu sau lần search; lỗi trả về khi quá hạn.
6. **Reply audience bị khoá**: mã lỗi và thông điệp khi reply vào bài có `reply_control` hạn chế hoặc tác giả đã block.
7. **Insights trên reply của chính mình**: `/insights` có trả `views/likes/replies` cho reply (không phải root post) không; độ trễ số liệu; `views` "in development" trả gì.
8. **Container status**: thời gian thực tế tới `FINISHED` cho bài text; poll thay cho sleep cố định.
9. **Đếm ký tự**: giới hạn 500 tính thế nào với tiếng Việt có dấu, emoji, URL.
10. **`is_made_with_ai=true` trên reply text**: nhãn hiển thị ra sao; có ảnh hưởng phân phối không (chỉ quan sát được, không có nguồn).
11. **Refresh token**: chạy thật một vòng `th_refresh_token`; hành vi khi token hết hạn giữa chừng.
12. **Multi-account**: số tester tối đa trên một app; quota và rate limit có tách theo user như docs nói không.
13. **Webhook `replies` / `mentions`**: có nhận được ở Standard Access với tester không (docs nói không).
14. **Threads Terms §3(a) toàn văn**: không phải spike kỹ thuật, nhưng phải có người đọc nguyên văn mục (i) "commercial purpose" và (iv) "automatic device" cùng mọi ngoại lệ trước khi cam kết.
15. **`sqlite-vec` dưới `bun:sqlite` trong Docker Linux** và trên macOS dev (chỉ khi stack này còn được giữ).
16. **Chi phí thật**: đo token mỗi bước trên 20 bài thật để thay các giả định ở mục 8.1.
17. **Dub**: `dub_id` có sống sót qua in-app browser của Threads tới site đích không.

---

## 14. Danh sách không kiểm chứng được

- Toàn văn Threads Terms of Use §3(a) và các ngoại lệ của nó.
- Nội dung bản cập nhật Instagram Terms có hiệu lực 2027-01-20.
- Tự khai AI có bắt buộc với bài text thuần trên Threads hay không; có nhãn riêng cho tài khoản tự động hay không.
- Thời gian và tỷ lệ đậu App Review cho các permission Threads.
- Số tester tối đa mỗi app; số tài khoản Threads tối đa mỗi người.
- Việc hai mục "Inauthentic Distribution / Engagement" bị bỏ hay gộp trong bản Inauthentic Behavior hiện hành.
- Thuật toán Threads có hạ phân phối bài chứa link ngoài (tiền đề của "tuyệt đối không gắn link ngoài") hay nội dung rage bait hay không: không tìm thấy nguồn gốc nào.
- Điều khoản sử dụng của Google Trends RSS và Product Hunt RSS.
- Reddit: không fetch được `reddit.com` để xem quy tắc API wiki và rate limit hiện hành.
- `sqlite-vec` + `bun:sqlite` trên Linux; "< 250MB RAM".
- Ngày và lý do `anthropic/claude-3.5-sonnet` bị gỡ khỏi OpenRouter.
- Kết quả vụ Eliza Labs kiện X Corp; số phận tài khoản của các agent mã nguồn mở khác; sự tồn tại của plugin Threads do cộng đồng viết.
- Hai bài arXiv 2510.08481 và 2505.03769 chỉ thấy qua tóm tắt tìm kiếm.
- Nghiên cứu về LLM dự đoán tương tác của chính bài nó viết.
