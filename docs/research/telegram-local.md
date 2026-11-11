# Telegram local verification

Issue #7 implements owner controls with temporary SQLite, injected time and a local `httptest`
Bot API. These are synthetic functional results. No VPS, real Telegram account, Threads session,
paid model or elapsed live soak was used; real-bot qualification remains in #18.

The wire contract was checked against Telegram's official
[getUpdates](https://core.telegram.org/bots/api#getupdates) and
[sendMessage](https://core.telegram.org/bots/api#sendmessage) documentation on 2026-11-11.
Updates are acknowledged by advancing the offset; the transport sends JSON and uses
`reply_parameters` for replies. The documented send API offers no client idempotency key, so
claiming before HTTP and never retrying uncertain sends is a deliberate harness policy.

## Reproduce

Run from the repository root with the configured Go toolchain:

```sh
go vet ./... && go test ./...
go test -race ./...
go test ./internal/telegram -run TestFakeBotPauseResumeWakeAndRedactedTrace -count=1
go run ./cmd/threadwalker eval -suite internal/eval/testdata/owner-v1.json -seed 7 -repeat 2 -out /tmp/owner-eval.json
go run ./cmd/threadwalker run -once -offline
```

To capture the complete synthetic integration trace explicitly:

```sh
THREADWALKER_OWNER_TRACE=/tmp/owner-events.jsonl go test ./internal/telegram -run TestFakeBotPauseResumeWakeAndRedactedTrace -count=1
```

The saved trace, shared v1 report and `checks.txt` live in [`testdata/telegram`](testdata/telegram).
The final rebased source passed full vet/test/race, owner eval 16/16, guard eval 58/58,
runtime eval 16/16 and an actual offline CLI smoke; the report identifies its source revision.
The report records the implementation revision, suite/input/configuration fingerprints, actual
executed tool names, persisted normalized wake usage and separately labelled wall-clock time.

## Evidence boundaries

| Behavior | Named evidence |
|---|---|
| Owner-only `/pause`, `/resume`, `/kill`, `/dnc`, `/status`; terminal kill; invalid/multi-agent requests | `TestOwnerOnlyCommandState`, `TestMultiAgentCommandsFailSafelyAndDNCIsPermanent`, `TestInvalidContactCommandDoesNotBlockFollowingOwnerControls`, `owner_only_controls` |
| Queued question returns before HTTP; only an authorized correlated answer reaches the following wake | `TestAskOwnerDoesNotCallNetworkAndReplyIsScoped`, `TestFakeBotPauseResumeWakeAndRedactedTrace`, `owner_async_question_next_wake` |
| Mandatory guard before private owner tool effects | `TestOwnerToolsUseGuardAndReturnQueuedID`, owner question eval through production runtime |
| Fake HTTP `/pause` prevents the next scheduled wake; `/resume` restores it | `TestFakeBotPauseResumeWakeAndRedactedTrace`, `TestSchedulerOwnerPauseBetweenSelectionAndWake` |
| Every notice once per event, UTC daily digest and latest daily metric sample | `TestNoticesAndDigestOncePerEventWithContent`, `owner_notices_digest_dedupe` |
| Durable update/delivery/reply dedupe; uncertainty never replays | `TestOwnerOutboxCorrelationSurvivesRestart`, `TestOwnerUnknownAnswersAndUncertainDeliveryAreNeverReplayed`, `owner_delivery_failure_no_replay` |
| Concurrent owner commands and questions; failed storage does not consume an update | `TestOwnerQueueConcurrentWithControls`, `TestOwnerControlPersistenceFailureDoesNotConsumeUpdate` |
| Redacted logs, safe failures and cancellation | `TestFakeBotPauseResumeWakeAndRedactedTrace`, `TestServiceFailuresHaveRedactedLogsAndNeverReplay`, `TestServiceDefaultWaitPreservesCancellation` |
| Bot grouping, enabled-only lifecycle, offline credential avoidance | `TestServicesGroupBotsAndRejectConflictingOwners`, `TestRunOnceOfflineUsesNoCredentialsOrExternalServices` |

Commands and automatic browser stops act independently of the model guard. Model owner tools
remain private guarded builtins. Each bot has one poller and one configured owner; conflicting
owner chats fail configuration. Only known agent IDs are selected. Poll offsets survive changes
to the bot's configured agent group; delivered questions also carry a bot/owner identity digest so
message-ID reuse by another bot cannot attach an unrelated answer.

The owner service serializes the shared Store's SQLite connection pool. It holds no database
connection while polling or sending. A command and its receipt commit together; repeated updates
cannot apply the same control again. Invalid contact input is ignored without blocking later
owner commands. An interrupted send is retained as `sending`, a failed send as `failed`; neither
is retried automatically. This can lose delivery after an ambiguous result and does not guarantee
exactly-once delivery by Telegram.

Action-limit notices preserve #6's wake stop and next-day retry policy; they do not permanently
pause the agent. The `BrowserStopped` adapter seam pauses on code-owned logged-out/captcha
events, while actual browser detection belongs to #8. Missing metrics are labelled unavailable.
Playbook change counts include entries created or decided today; broader learning behavior belongs
to #10. None of these results establish real-bot delivery, real-account survival or live engagement.
