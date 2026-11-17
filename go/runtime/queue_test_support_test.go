package runtime

import (
	"testing"

	"github.com/eleven-am/golem/go/queue"
)

type queueGatePayload struct {
	Title string `json:"title"`
}

func newQueueGatePending(t *testing.T, jobType queue.Type[queueGatePayload]) queue.Pending {
	t.Helper()
	pending, err := jobType.New(queueGatePayload{Title: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	return pending
}
