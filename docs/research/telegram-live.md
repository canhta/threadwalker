# Telegram live bot qualification

Real `@thread_walker_bot` run on 2026-11-12 from the owner's Mac (macOS, local timezone
Asia/Ho_Chi_Minh). This qualifies the owner contact surface of spec section 9 against the real Bot
API. Threads was not touched: the template used `dry_run: true` and `threads.backend: stub`, with a
fresh SQLite DB under the scratch directory. The owner supplied the bot token and chat ID privately
in the gitignored `.env`; neither appears here.

The run used the `viral` agent template (persona **Giáo sư LLM**, language `vi`) with two deltas in
a scratch copy: `telegram.enabled: true` and `digest_hour: 0` so the daily digest could be observed
without waiting. The LLM provider in the scratch copy was Anthropic `claude-sonnet-5-5` because
`LLM_API_KEY` (the template's DeepSeek key) is unset in `.env`; see the provider note below.

Commands it exercises: menu registration, digest, `notify_owner`, `ask_owner` delivery,
`/status`, `/pause`, `/resume`, `/dnc`, `/help`, `/start`, free text, and `/kill`. The owner did not
reply to the delivered question before `/kill`, so the live answer row is the one missing result.

## Menu

`setMyCommands` runs on every service start. `getMyCommands` immediately after the service started
returned:

```json
[{"command":"help","description":"xem danh sách lệnh"},
 {"command":"status","description":"xem đang làm gì"},
 {"command":"pause","description":"tạm dừng"},
 {"command":"resume","description":"chạy tiếp"},
 {"command":"kill","description":"tắt hẳn, ko bật lại"},
 {"command":"dnc","description":"né username: /dnc <username>"}]
```

This replaced the menu that had been registered by hand. The service logged
`operation=commands outcome=success reason=menu_registered`; a failed call logs `menu_failed` and
the poller keeps running (fake-bot test `TestServiceRegistersMenuAtStartAndSurvivesFailure`).

`getUpdates` sends `allowed_updates: ["message"]` on every call. With a fresh DB and a fresh offset,
no stale persisted filter hid owner messages.

## Owner-facing messages

Messages are plain text in the persona voice from the template's `telegram.messages`, assembled
only from code-owned facts. Samples delivered to the owner (test text only):

| Trigger | Delivered text |
|---|---|
| daily digest (forced with `digest_hour: 0`) | `hôm nay tao đăng 0/3 bài, reply 0/15, xài $0.00 trên $2.00.`<br>`playbook sửa 3 chỗ.`<br>`15:49 (còn 59 phút) tao lên lớp tiếp.` |
| `/status` | `đang nghỉ.`<br>`lần lên lớp tới: 07:00 (còn 49 ngày)`<br>`hôm nay: 0/3 bài · 0/15 reply · $0.01/$2.00`<br>`playbook: 3 chỗ vừa sửa` |
| `/pause` | `ừ, tao nghỉ. gõ /resume khi muốn tao lên lớp tiếp.` |
| `/resume` | `tao dậy rồi, lớp tiếp tục. được chưa?` |
| `/dnc test_user_abc` | `ok, tao không đụng @test_user_abc nữa. ghi sổ rồi.` |
| `/help` and `/start` | `lệnh điều khiển:`<br>`/status — xem tao đang làm gì`<br>`/pause — cho tao nghỉ`<br>`/resume — gọi tao dậy`<br>`/kill — tắt hẳn, không bật lại được`<br>`/dnc <username> — tao né username đó` |
| free text `nhớ giữ giọng cà khịa nha` | `ghi rồi. lần dậy tới tao đọc.` |

The `/status` text shows `07:00` in the owner zone for a stored `00:00Z` next wake, and "còn 49
ngày" was the deliberately far-future wake used to keep the control run from spending a model call.
The earlier digest showed `15:49 (còn 59 phút)` for the wake that the failed model call had
scheduled. Counts, limits, spend and times all come from the DB and template, not the model.

Every command changed the DB as expected: `/pause` wrote `paused`, `/resume` wrote `running`,
`/dnc test_user_abc` created a `do_not_contact=1 reason=owner_request` contact row, and free text
became an `owner_msgs` row with `direction=in kind=note`. Repeating a command update did not apply
it twice.

## `notify_owner` and `ask_owner`

A real wake called both tools. They queued local rows and returned immediately; the service then
delivered them to the owner:

```
Giáo sư LLM đã thức, đang chạy dry run. Lớp sắp khai giảng, được chưa?
Hôm nay chủ nhân muốn tao ưu tiên chủ đề gì? (giá token, vibe code, local LLM, AI thay dev...)

↩️ trả lời ngay tin này để tao nhận được
```

The question arrived with the reply hint. The owner then sent `/kill` without replying to it, so
**no live answer row exists**: the only rows for that question are the outbound `question` and the
later `/kill` receipt/notice. Answer correlation is covered by
`TestAskOwnerDoesNotCallNetworkAndReplyIsScoped` and the saved owner eval, not by this live run.

### Provider note: thinking content blocks

The first attempts used `claude-haiku-5-5` and failed before any Telegram traffic with
`event=llm.run.stopped reason=invalid_response`. A local reverse proxy showed the response contained
`content: ["thinking", "tool_use"]` with `stop_reason: "tool_use"`. The native adapter treats unknown
content blocks as invalid by design (`internal/llm/anthropic.go`, spec section 3: unknown blocks fail
closed), so the whole wake stopped with `provider_error` and the runtime exited. Retrying with
`claude-sonnet-5-5` let the owner tools execute; the wake still later hit a thinking block and
stopped, but not before `notify_owner` and `ask_owner` had queued. This is an LLM-provider contract
gap, not a Telegram failure; it is tracked in #31 and is not claimed as working provider support.

At the time of this run `LLM_API_KEY` (the DeepSeek key the template originally selected) was unset,
which is why the scratch copy used an Anthropic model. Commit 4fb9dd3 later moved
`templates/viral.agent.yaml` to OpenAI `gpt-6-luna` with `reasoning_effort: none` and
`OPENAI_API_KEY`; the Telegram re-run on that provider is still pending.

## Reproduce

```sh
set -a; . ./.env; set +a
go build -o /tmp/tw ./cmd/threadwalker
/tmp/tw check -agents /path/to/viral-copy -db /tmp/tw.db
/tmp/tw run -agents /path/to/viral-copy -db /tmp/tw.db -log-level debug
```

Scratch copy delta from `templates/viral.agent.yaml`: `telegram.enabled: true`, `digest_hour: 0`,
and the provider/model described above. All other fields, including `dry_run: true`, are unchanged.

## Evidence boundaries

The bot, chat, menu, outbox, command state and answer correlation are real. The template persona
wording is real. The model provider was swapped as noted, and the wake itself stopped on the
thinking-block contract; `notify_owner`/`ask_owner` delivery was therefore completed by the service
restarting against the same outbox, not by a clean completed wake. Threads was never contacted, the
48-hour soak is #18's remaining work, and `/kill` is only exercised against this scratch DB.

## Closing observations

The final live `/kill` was delivered:

```
tắt thì tắt. /resume không cứu được tao đâu.
đã bị tắt hẳn. tao xử lý theo sổ.
```

and the DB ended at `agents.status='killed'`. `/resume` was not sent after the kill, so terminality
is evidenced by the killed row plus the offline terminal-kill tests, not by a live resume attempt.
Exact rows at close (ids, kinds and directions only; no chat identifiers):

| id | kind | direction | delivery | detail |
|---|---|---|---|---|
| 1 | digest | out | sent | persona-voiced daily digest |
| 2 | note | in | received | free text `hello` |
| 3 | notification | out | sent | note acknowledgement |
| 9 | command | out | sent | `/status` summary |
| 11 | command | out | sent | `/pause` receipt |
| 13 | command | out | sent | `/resume` receipt |
| 15 | command | out | sent | `/dnc` receipt |
| 17 | command | out | sent | `/help` |
| 19 | note | in | received | free text `nhớ giữ giọng cà khịa nha` |
| 20 | notification | out | sent | note acknowledgement |
| 25 | notification | out | sent | `notify_owner` text |
| 26 | question | out | sent | `ask_owner` text; no answer row |
| 36 | command | out | sent | `/kill` receipt |
| 37 | notice | out | sent | killed notice |

Remaining follow-ups:

- Re-run the control samples with the current `templates/viral.agent.yaml` (`tôi`/`các con vợ`,
  OpenAI `gpt-6-luna`). The persona wording pasted above came from the template revision used for
  this run; the current template still contains "lớp" metaphors in its `telegram.messages`, which the
  owner rejected, so a copy-only fix belongs to the template owner.
- Have the owner reply to a delivered question so a live `owner_msgs` answer row is captured.
- #31 tracks the thinking-block provider contract that stopped the wakes.
