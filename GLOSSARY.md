# Glossary

Dùng đúng các từ này trong code, issue và tài liệu. Tên trong code dùng cột "Tên code".

| Thuật ngữ | Tên code | Nghĩa |
|---|---|---|
| Khung | `harness` | Phần code Go cố định: lập lịch, tool, luật cứng, sổ ngân sách. Agent không sửa được. |
| Agent | `agent` | Một vòng lặp LLM gắn với một template, một tài khoản Threads và một ngân sách. |
| Template | `template` | File YAML khai báo goal, persona, giới hạn và LLM của một agent. Bất biến với agent. |
| Lần thức | `wake` | Một phiên agent chạy từ lúc được đánh thức tới khi tự kết thúc hoặc chạm trần. |
| Tool | `tool` | Hành động agent gọi được. Mọi tool đi qua luật cứng trước khi thực thi. |
| Luật cứng | `guard` | Kiểm tra trong code mà agent không tắt được: trần, từ cấm, danh sách không đụng tới. |
| Hành động | `action` | Một bài gốc hoặc một reply đã đăng (hoặc đã ghi log khi chạy khô). |
| Playbook | `playbook` | Danh sách giả thuyết của agent về cái gì hiệu quả. |
| Giả thuyết | `hypothesis` | Một mục playbook, trạng thái `testing`, `established` hoặc `rejected`. |
| Cổng thống kê | `gate` | Code quyết định một giả thuyết đổi trạng thái, dựa trên bộ đếm do code giữ. |
| Sổ ngân sách | `ledger` | Số token và chi phí theo agent theo ngày, tính cục bộ. |
| Chạy khô | `dry_run` | `post` và `reply` chỉ ghi log, không đăng thật. |
| Chủ dự án | `owner` | Người vận hành, liên lạc qua Telegram. |
| Danh sách không đụng tới | `do_not_contact` | Username mà agent không được reply nữa. |

Tránh dùng: "dreaming", "survival mode", "critic", "triage", "bandit" (thuộc spec cũ đã bỏ).
