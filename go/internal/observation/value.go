// Package observation owns the private value behind the public immutable
// observation record. It deliberately contains no formatter or adapter.
package observation

import (
	"sync"
	"time"

	"github.com/eleven-am/golem/go/golem"
)

var (
	emitterMu sync.RWMutex
	emitter   func(any, Value)
)

type Value struct {
	KindValue           string
	PhaseValue          string
	OutcomeValue        string
	ReasonValue         string
	ProviderValue       golem.Provider
	ModelIDValue        golem.ModelID
	OperationValue      string
	DurationValue       time.Duration
	StatementCountValue int
	AttemptValue        int
	QueueDepthValue     int
	QueueLimitValue     int
	AggregateCountValue int64
}

// RegisterEmitter connects the private producer representation to the public
// immutable observation package without putting an internal type in any public
// signature. It is called once by observe.init.
func RegisterEmitter(value func(any, Value)) {
	emitterMu.Lock()
	defer emitterMu.Unlock()
	if emitter == nil {
		emitter = value
	}
}

func Emit(observer any, value Value) {
	emitterMu.RLock()
	invoke := emitter
	emitterMu.RUnlock()
	if invoke != nil {
		invoke(observer, value)
	}
}
