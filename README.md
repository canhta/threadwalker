# ThreadWalker

Thí nghiệm mã nguồn mở: một agent AI chỉ được giao goal, một tài khoản Threads và một ngân sách
thì tự vận hành, tự học và tự cải tiến được tới đâu khi không có người duyệt.

Trạng thái: chưa có code. Thiết kế đã chốt, công việc nằm trong
[GitHub Issues](https://github.com/canhta/threadwalker/issues).

## Đọc gì trước

| File | Nội dung |
|---|---|
| [`docs/spec.md`](docs/spec.md) | Spec có hiệu lực: kiến trúc, tool, luật cứng, lược đồ DB, thứ tự làm |
| [`GLOSSARY.md`](GLOSSARY.md) | Thuật ngữ dùng trong code và issue |
| [`AGENTS.md`](AGENTS.md) | Quy ước cho coding agent làm việc trong repo |
| [`docs/research/`](docs/research/) | Bằng chứng đằng sau các quyết định, kèm nguồn |
| [`docs/handoff_spec.md`](docs/handoff_spec.md) | Bản nháp đầu, đã bị thay thế, chỉ để đối chiếu |

## Lưu ý

Agent điều khiển Threads qua trình duyệt tự động, việc này trái điều khoản dịch vụ của Threads và
có thể làm mất tài khoản. Chỉ dùng tài khoản phụ, có ghi rõ trong bio là thí nghiệm AI.
