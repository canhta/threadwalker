# ThreadWalker — Spec v0 (thay cho handoff_spec.md)

Ngày: 2026-11-11. Trạng thái: chủ dự án đã giao triển khai; công việc chia thành issue trên GitHub.

Nguồn: `docs/research/handoff_spec_verification.md` (kiểm chứng spec cũ) và
`docs/research/agentic_architecture_research.md` (kiến trúc agent). Research về bốn repo
tham khảo chưa xong, chưa được đưa vào đây. Mỗi mục ghi rõ là **[quyết định]** của chủ dự án,
**[nguồn]** từ research, hay **[đề xuất]** của người viết và chưa được kiểm bằng thực nghiệm.

## 1. Thí nghiệm này trả lời câu hỏi gì

Một agent chỉ được giao goal, một tài khoản Threads và một ngân sách thì tự vận hành, tự học và
tự cải tiến được tới đâu khi không có người duyệt? Đây là thí nghiệm năng lực AI, không phải sản
phẩm để bán. **[quyết định]**

Thước đo, đọc theo tuần trên chính tài khoản đó: follower, lượt xem, số reply nhận được, chi phí
token. **[đề xuất]**

## 2. Đã chốt

| Điểm | Chốt | Loại |
|---|---|---|
| Mức tự chủ | Tự động hoàn toàn, không người duyệt bài | quyết định |
| Liên lạc với chủ | Telegram, chỉ khi cần | quyết định |
| Template | Chỉ khai báo goal, persona, ràng buộc; chiến thuật do agent tự vạch | quyết định |
| Tự sửa | Agent sửa playbook và ký ức trong DB, không sửa code | quyết định |
| Tài khoản | Tài khoản phụ, mất cũng được | quyết định |
| Truy cập Threads | Trình duyệt tự động, vì không có Business Verification | quyết định |
| LLM | Cắm được qua cấu hình, không gắn một model | quyết định |
| Ngôn ngữ | Rust hoặc Go; đề xuất Go | đề xuất |
| Hosting | VPS của chủ dự án: Ubuntu 24.04, 2 vCPU, 3,6 GB RAM (còn trống khoảng 2,6 GB), 48 GB đĩa trống; chưa có Docker, Go hay Chromium | đã kiểm trên máy |
| Nhiều agent | Có, nhưng sau khi một agent chạy ổn | đề xuất |
| Nhãn AI trong bio | Có ghi. Điều kiện của người viết, chủ dự án **chưa xác nhận** | chờ xác nhận |

## 3. Hình dạng tổng thể

Hai lớp, tách bạch:

- **Khung cứng (code Go):** lập lịch, tool, trần, sổ ngân sách, audit log, kill switch, cổng
  thống kê. Agent không sửa được lớp này.
- **Agent (LLM):** mỗi lần thức dậy nhận goal, playbook, số liệu và ngân sách còn lại, rồi tự
  quyết làm gì bằng cách gọi tool.

Một agent, một vòng lặp tool-calling viết tay trên Chat Completions. Không có pipeline
triage → compose → critic cố định, không có critic cùng model. **[nguồn]**: OpenAI khuyên dồn
năng lực vào một agent trước; Project Vend cho thấy agent giám sát cùng model mắc cùng điểm mù.

## 4. Vòng đời một lần thức

1. Khung đánh thức agent theo giờ agent tự đặt ở lần trước (có trần tần suất).
2. Khung đưa vào ngữ cảnh: template (bất biến), playbook, số liệu mới do code tính, ngân sách
   còn lại, ghi chú agent để lại cho chính nó.
3. Agent gọi tool cho tới khi tự kết thúc, hoặc chạm trần bước / trần token của lần thức.
4. Mọi lời gọi tool đi qua khung: kiểm trần và luật cứng, ghi audit log, rồi mới thực thi.
5. Agent ghi kế hoạch cho lần sau và giờ thức tiếp theo.

Không còn "02:00 dreaming" cố định: phản tư là một việc agent tự chọn làm khi có số liệu mới.
**[đề xuất]**

## 5. Bộ tool

| Nhóm | Tool | Ghi chú |
|---|---|---|
| Threads (trình duyệt) | `read_feed`, `search`, `read_post`, `post`, `reply`, `read_own_metrics` | Một profile trình duyệt cho mỗi agent |
| Nguồn ngoài | `fetch_trends` (HN, Product Hunt RSS, Google Trends VN RSS) | Research đã gọi thử, cả ba phản hồi |
| Ký ức | `note_write`, `note_search` | Tìm bằng FTS của SQLite, không dùng vector **[nguồn]**: quy mô này không cần |
| Playbook | `playbook_propose`, `playbook_read` | Xem mục 7 |
| Chủ dự án | `notify_owner`, `ask_owner` | Telegram; `ask_owner` không chặn, agent làm việc khác trong lúc chờ |
| Điều độ | `schedule_next_wake` | Trong khoảng khung cho phép |

## 6. Luật cứng (trong code, agent không thấy cách tắt)

- Trần bài gốc và reply mỗi ngày; trần token mỗi lần thức và mỗi ngày.
- Cà khịa: không nhắm đặc điểm cá nhân; tối đa N reply cho một username trong 7 ngày; ai chặn,
  bảo ngừng hoặc tỏ ra khó chịu thì vào danh sách không đụng tới.
- So chuỗi danh sách từ cấm trước khi đăng (không cần LLM).
- Không lưu hồ sơ người lạ: chỉ giữ username và bộ đếm để giới hạn tần suất. **[nguồn]**:
  Platform Terms §3.a.v và rủi ro cài ký ức trong nghiên cứu về ElizaOS.
- Nội dung đọc từ Threads được đánh dấu là dữ liệu không tin cậy; khóa và mật khẩu không bao
  giờ vào ngữ cảnh agent.
- Kill switch: lệnh Telegram `/pause`, `/resume`, `/kill`; tự dừng khi chạm trần ngày hoặc khi
  trình duyệt bị đăng xuất / gặp captcha.

Các con số trần là giá trị khởi đầu trong file cấu hình, chưa có cơ sở thực nghiệm.

## 7. Tự học và tự cải tiến

Vấn đề: vài chục hành động mỗi ngày, phản hồi trễ và nhiễu. Research tính cần khoảng 49 ngày để
tách chiến thuật 20% khỏi 30% khi chia 5 nhánh. Học từ từng bài là học từ nhiễu. **[nguồn, phần
số là tính toán của research]**

Thiết kế **[đề xuất, chưa nghiên cứu nào thử đúng tổ hợp này]**:

- Playbook là danh sách mục ngắn, mỗi mục là một giả thuyết agent viết ("reply dưới 25 chữ về
  chi phí được trả lời nhiều hơn") kèm trạng thái `đang thử` hoặc `đã xác lập`.
- Agent gắn nhãn giả thuyết cho mỗi hành động. **Code** đếm kết quả, agent không tự đếm.
- Cổng thống kê trong code quyết định khi nào một mục lên `đã xác lập` hoặc bị loại: đủ số mẫu
  tối thiểu và chênh lệch vượt ngưỡng so với nền.
- Agent chỉ thêm hoặc sửa từng mục; không được viết lại toàn bộ playbook. **[nguồn]**: viết lại
  toàn bộ từng làm ngữ cảnh sụp từ 18.282 xuống 122 token.
- Template bất biến. Agent không sửa prompt hệ thống.

Hệ quả phải chấp nhận: cổng mở chậm, vài tuần mới có mục đầu tiên được xác lập. Tốc độ mở cổng
là một kết quả của thí nghiệm.

## 8. Sinh tồn

- Sổ token cục bộ theo từng agent, tính tiền theo bảng giá khai trong cấu hình. **[nguồn]**: gói
  thuê bao không có số dư để đọc, nên `GET /credits` của spec cũ không dùng làm nền được.
- Agent luôn thấy ngân sách còn lại và tự quyết cách tiêu; khung chỉ chặn khi chạm trần.
- Nối doanh thu thật vào ngân sách (tự nuôi sống đúng nghĩa) để sau, khi có số liệu click.

## 9. Telegram

- Agent chủ động nhắn khi: gặp việc ngoài quyền, bị đăng xuất hoặc captcha, sắp hết ngân sách.
- Bản tin ngắn mỗi ngày: đã làm gì, số liệu, playbook đổi gì.
- Chủ dự án ra lệnh `/pause`, `/resume`, `/kill`, và trả lời `ask_owner`.

## 10. Stack

| Phần | Chọn | Mức chắc chắn |
|---|---|---|
| Ngôn ngữ | Go, một binary | Research đọc repo, chưa chạy thử |
| DB | SQLite, driver thuần Go (`modernc.org/sqlite`) | Research đọc repo, chưa chạy thử |
| LLM | Client OpenAI-compatible: `base_url`, `api_key`, `model` theo từng agent | DeepSeek dùng được; Antigravity không (điều khoản cấm); OpenCode Go rủi ro |
| Trình duyệt | Chromium headless điều khiển qua CDP | **Chưa kiểm**: thư viện Go nào, RAM thực tế, Threads có chặn IP datacenter không |
| Triển khai | systemd trên VPS, chưa cần Docker | đề xuất |

## 11. Cắt khỏi spec cũ

- Threads Graph API, App Review, `keyword_search`: không có Business Verification.
- Pipeline ba tầng, critic LLM, 3 phương án mỗi lần viết.
- `viral_autopsies`, `relationship_graph.vibe_summary`, `sqlite-vec`, Drizzle, Bun, Hono.
- Thompson sampling theo tên chiến thuật cố định; "predicted score".
- Survival Mode theo số dư OpenRouter; chia Tier 1 / Tier 2 theo tên model.
- Jitter để giả người. Giữ giãn cách giữa các hành động, nhưng với mục đích giới hạn tần suất.

Giữ nhưng làm sau: dashboard công khai (phải che username người lạ), nhiều agent, template thứ
hai.

## 12. Thứ tự làm

Mỗi bước có điều kiện dừng; bước trước chưa qua thì không làm bước sau.

1. **Spike trình duyệt trên VPS.** Đăng nhập tài khoản phụ, đọc feed, đăng một bài, reply một
   bài, đọc số liệu bài mình. Qua khi chạy lặp lại được 3 ngày không bị đăng xuất. Đây là điểm
   có thể giết cả dự án.
2. **Vòng lặp agent chạy khô.** Đủ tool, nhưng `post` và `reply` chỉ ghi log. Qua khi agent tự
   lập kế hoạch và tự đặt giờ thức trong 48 giờ mà không vượt trần.
3. **Chạy thật với trần thấp** và Telegram. Qua sau 7 ngày không cần can thiệp tay.
4. **Playbook và cổng thống kê.**
5. **Dashboard công khai.**
6. **Agent thứ hai, template thứ hai.**

## 13. Chưa biết

- Threads có chặn hoặc thử thách đăng nhập từ IP datacenter của VPS không.
- Tài khoản tự động qua trình duyệt sống được bao lâu.
- Model rẻ (DeepSeek) có giữ được vòng lặp tool-calling dài mà không trôi không.
- Cổng thống kê có mở được lần nào trong 4–6 tuần đầu không.
- Chủ dự án có đồng ý ghi nhãn AI trong bio không.
- Bốn repo tham khảo có mẫu nào đáng mượn không (research đang chạy).

## 14. Bố cục repo

```text
threadwalker/
├── cmd/threadwalker/main.go     # entrypoint: đọc cấu hình, mở DB, chạy scheduler
├── internal/
│   ├── config/                  # đọc và kiểm tra agents/*.yaml
│   ├── store/                   # SQLite, migration, truy vấn
│   ├── llm/                     # client OpenAI-compatible + sổ token
│   ├── agent/                   # vòng lặp một lần thức, lắp ngữ cảnh
│   ├── tools/                   # định nghĩa tool và bộ điều phối
│   ├── guard/                   # luật cứng: trần, từ cấm, danh sách không đụng tới
│   ├── threads/                 # backend trình duyệt cho các tool Threads
│   ├── playbook/                # mục giả thuyết + cổng thống kê
│   └── telegram/                # thông báo và lệnh của chủ dự án
├── agents/                      # mỗi agent một file template
│   └── solo-builder.example.yaml
├── deploy/threadwalker.service  # systemd unit
└── docs/
```

Mọi tool đi qua một interface duy nhất trong `internal/tools`; `internal/guard` bọc bộ điều phối,
nên không có đường nào gọi tool mà bỏ qua luật cứng.

## 15. Lược đồ DB

Mọi bảng có `agent_id`, kể cả khi mới chạy một agent.

| Bảng | Cột chính | Dùng cho |
|---|---|---|
| `agents` | `id`, `status` (`running`/`paused`/`killed`), `next_wake_at`, `self_note` | Trạng thái và ghi chú agent để lại cho lần thức sau |
| `wakes` | `id`, `agent_id`, `started_at`, `ended_at`, `steps`, `tokens_in`, `tokens_out`, `cost_usd`, `end_reason` | Mỗi lần thức |
| `tool_calls` | `id`, `wake_id`, `tool`, `args_json`, `result_json`, `blocked_by` (luật nào chặn, nếu có), `at` | Audit log |
| `actions` | `id`, `agent_id`, `kind` (`post`/`reply`), `text`, `target_url`, `target_username`, `hypothesis_ids_json`, `dry_run`, `posted_at`, `thread_url` | Bài đã đăng |
| `metrics` | `action_id`, `measured_at`, `views`, `likes`, `replies`, `reposts` | Số liệu đo lại theo thời gian |
| `notes` | `id`, `agent_id`, `text`, `created_at` (+ bảng FTS5) | Ký ức agent tự ghi |
| `playbook` | `id`, `agent_id`, `text`, `status` (`testing`/`established`/`rejected`), `n`, `successes`, `created_at`, `decided_at` | Giả thuyết và bộ đếm do code cập nhật |
| `contacts` | `agent_id`, `username`, `reply_count_7d`, `do_not_contact`, `reason` | Chỉ để giới hạn tần suất; không lưu mô tả về người |
| `ledger` | `agent_id`, `day`, `tokens_in`, `tokens_out`, `cost_usd` | Ngân sách |
| `owner_msgs` | `id`, `agent_id`, `direction`, `text`, `answered_id`, `at` | Trao đổi Telegram |

## 16. Template agent

```yaml
id: solo-builder
goal: >
  Xây một tài khoản Threads có người theo dõi thật trong cộng đồng builder và AI ở Việt Nam.
persona:
  name: Kaito
  bio_must_include: "thí nghiệm AI tự vận hành"   # nhãn AI, xem mục 2
  voice: ngắn, thực dụng, châm biếm nhẹ quan điểm, không công kích cá nhân
  forbidden_phrases: ["Tuy nhiên,", "Tóm lại,", "Trong kỷ nguyên", "Không thể phủ nhận"]
llm:
  base_url: https://api.deepseek.com
  model: deepseek-chat            # tên model lấy theo tài liệu nhà cung cấp lúc triển khai
  api_key_env: TW_SOLO_LLM_KEY
  price_per_mtok_in_usd: 0        # điền theo bảng giá hiện hành; sổ token dùng số này
  price_per_mtok_out_usd: 0
limits:                           # giá trị khởi đầu, chưa có cơ sở thực nghiệm
  posts_per_day: 3
  replies_per_day: 15
  replies_per_user_7d: 2
  min_seconds_between_actions: 300
  max_steps_per_wake: 30
  max_usd_per_day: 0.50
  min_minutes_between_wakes: 20
threads:
  profile_dir: data/profiles/solo-builder
telegram:
  bot_token_env: TW_TELEGRAM_TOKEN
  owner_chat_id_env: TW_OWNER_CHAT_ID
dry_run: true
```

Khóa bí mật chỉ nằm trong biến môi trường, không nằm trong file template và không bao giờ được
đưa vào ngữ cảnh LLM.
