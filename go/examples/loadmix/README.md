# loadmix

A load tool for the commit path and the event delivery path. It is a tool, not
a gate: it has no baseline, no thresholds and no release wiring.

- `loadmix/` is a generated golem app whose Article, ArticleAudio, ChatThread,
  ChatMessage and ReadingSession mirror Readable's column shapes, with
  Subscriptions on every model except ReadingSession, which writes no facts and
  is the control.
- `cmd/loadmix` is the driver: open-loop Poisson arrivals through the generated
  mutation API, one principal per simulated user, the event publisher and one
  Article and one ChatMessage subscription per user.

Run from this directory with `GOLEM_TEST_POSTGRES_DSN` set and `make go-build`
done at the repository root:

```
go run ./cmd/loadmix -kind all
go run ./cmd/loadmix -kind steady -users 100
go run ./cmd/loadmix -kind burst
go run ./cmd/loadmix -kind saturation -writers 16
go run ./cmd/loadmix -kind steady -users 10 -warmup 3s -measure 12s
```

`-kind all` runs steady at `-users`, burst at U=100 and saturation. `-json`
prints the full result. The exit code is 1 when any run fails.

A run fails only on correctness: a stream termination (recorded by code, and
the run stops at the first one), a foreign delivery, a duplicate or surplus
delivery, a shortfall between expected and delivered events after a bounded
drain, or a mutation error. Expected events are counted per owner subscription
from successful Article and ChatMessage writes; delivered events are counted by
(subscription, event ID).
