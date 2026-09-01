// Package slog exports Golem observations through the standard library's
// structured logger using only the checked, closed telemetry vocabulary.
package slog

import (
	"context"
	"errors"
	standardslog "log/slog"

	"github.com/eleven-am/golem/go/observe"
	adapterinternal "github.com/eleven-am/golem/go/observe/internal/adapter"
)

type Config struct {
	Logger        *standardslog.Logger
	QueueCapacity int
}

type Adapter struct {
	dispatcher *observe.Dispatcher
}

func New(config Config) (*Adapter, error) {
	if config.Logger == nil {
		return nil, errors.New("GOLEM_OBSERVE_SLOG_CONFIG: logger is required")
	}
	dispatcher, err := observe.NewDispatcher(sink{logger: config.Logger}, observe.DispatcherConfig{QueueCapacity: config.QueueCapacity})
	if err != nil {
		return nil, err
	}
	return &Adapter{dispatcher: dispatcher}, nil
}

func (adapter *Adapter) ObserveGolem(ctx context.Context, value observe.Observation) {
	if adapter != nil {
		adapter.dispatcher.ObserveGolem(ctx, value)
	}
}

func (adapter *Adapter) Dropped() uint64 {
	if adapter == nil {
		return 0
	}
	return adapter.dispatcher.Dropped()
}

func (adapter *Adapter) Shutdown(ctx context.Context) error {
	if adapter == nil {
		return nil
	}
	return adapter.dispatcher.Shutdown(ctx)
}

type sink struct {
	logger *standardslog.Logger
}

func (target sink) ObserveGolem(ctx context.Context, value observe.Observation) {
	record := adapterinternal.Normalize(value)
	target.logger.LogAttrs(ctx, standardslog.LevelInfo, adapterinternal.SlogMessage,
		standardslog.String(adapterinternal.AttributeNames[0], record.Kind),
		standardslog.String(adapterinternal.AttributeNames[1], record.Phase),
		standardslog.String(adapterinternal.AttributeNames[2], record.Outcome),
		standardslog.String(adapterinternal.AttributeNames[3], record.Reason),
		standardslog.String(adapterinternal.AttributeNames[4], record.Provider),
		standardslog.String(adapterinternal.AttributeNames[5], record.Operation),
		standardslog.String(adapterinternal.AttributeNames[6], record.ModelID),
		standardslog.Int64(adapterinternal.AttributeNames[7], record.DurationNS),
		standardslog.Int64(adapterinternal.AttributeNames[8], record.StatementCount),
		standardslog.Int64(adapterinternal.AttributeNames[9], record.Attempt),
		standardslog.Int64(adapterinternal.AttributeNames[10], record.QueueDepth),
		standardslog.Int64(adapterinternal.AttributeNames[11], record.QueueLimit),
		standardslog.Int64(adapterinternal.AttributeNames[12], record.AggregateCount),
		standardslog.String(adapterinternal.AttributeNames[13], record.QueueType),
	)
}
