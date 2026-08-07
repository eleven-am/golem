package events

import "time"

type Limits struct {
	ClaimRows                     int
	PublisherConcurrency          int
	LeaseDuration                 time.Duration
	PublishTimeout                time.Duration
	RetryBase                     time.Duration
	RetryCap                      time.Duration
	MaxEncodedEventBytes          int
	SubscriberQueue               int
	HubInputQueue                 int
	EvaluationConcurrency         int
	MaxSubscriptionsPerConnection int
	ConnectionInitBytes           int
	ConnectionInitTimeout         time.Duration
	ShutdownGrace                 time.Duration
	RetentionDeleteRows           int
}

var defaultLimits = Limits{
	ClaimRows: 64, PublisherConcurrency: 8, LeaseDuration: 30 * time.Second,
	PublishTimeout: 15 * time.Second, RetryBase: 250 * time.Millisecond,
	RetryCap: 5 * time.Minute, MaxEncodedEventBytes: 2 << 20,
	SubscriberQueue: 64, HubInputQueue: 256, EvaluationConcurrency: 32,
	MaxSubscriptionsPerConnection: 32, ConnectionInitBytes: 64 << 10,
	ConnectionInitTimeout: 10 * time.Second, ShutdownGrace: 15 * time.Second,
	RetentionDeleteRows: 256,
}

var maximumLimits = Limits{
	ClaimRows: 1024, PublisherConcurrency: 128, LeaseDuration: 10 * time.Minute,
	PublishTimeout: 2 * time.Minute, RetryBase: time.Minute,
	RetryCap: 24 * time.Hour, MaxEncodedEventBytes: 16 << 20,
	SubscriberQueue: 4096, HubInputQueue: 16384, EvaluationConcurrency: 256,
	MaxSubscriptionsPerConnection: 256, ConnectionInitBytes: 1 << 20,
	ConnectionInitTimeout: time.Minute, ShutdownGrace: 2 * time.Minute,
	RetentionDeleteRows: 4096,
}

func DefaultLimits() Limits { return defaultLimits }
func MaximumLimits() Limits { return maximumLimits }

func NormalizeLimits(input Limits) (Limits, error) {
	output := input
	integers := []struct {
		value             *int
		fallback, maximum int
	}{
		{&output.ClaimRows, defaultLimits.ClaimRows, maximumLimits.ClaimRows},
		{&output.PublisherConcurrency, defaultLimits.PublisherConcurrency, maximumLimits.PublisherConcurrency},
		{&output.MaxEncodedEventBytes, defaultLimits.MaxEncodedEventBytes, maximumLimits.MaxEncodedEventBytes},
		{&output.SubscriberQueue, defaultLimits.SubscriberQueue, maximumLimits.SubscriberQueue},
		{&output.HubInputQueue, defaultLimits.HubInputQueue, maximumLimits.HubInputQueue},
		{&output.EvaluationConcurrency, defaultLimits.EvaluationConcurrency, maximumLimits.EvaluationConcurrency},
		{&output.MaxSubscriptionsPerConnection, defaultLimits.MaxSubscriptionsPerConnection, maximumLimits.MaxSubscriptionsPerConnection},
		{&output.ConnectionInitBytes, defaultLimits.ConnectionInitBytes, maximumLimits.ConnectionInitBytes},
		{&output.RetentionDeleteRows, defaultLimits.RetentionDeleteRows, maximumLimits.RetentionDeleteRows},
	}
	for _, item := range integers {
		if *item.value == 0 {
			*item.value = item.fallback
		}
		if *item.value < 0 || *item.value > item.maximum {
			return Limits{}, Failure(CodeEventConfig)
		}
	}
	durations := []struct {
		value             *time.Duration
		fallback, maximum time.Duration
	}{
		{&output.LeaseDuration, defaultLimits.LeaseDuration, maximumLimits.LeaseDuration},
		{&output.PublishTimeout, defaultLimits.PublishTimeout, maximumLimits.PublishTimeout},
		{&output.RetryBase, defaultLimits.RetryBase, maximumLimits.RetryBase},
		{&output.RetryCap, defaultLimits.RetryCap, maximumLimits.RetryCap},
		{&output.ConnectionInitTimeout, defaultLimits.ConnectionInitTimeout, maximumLimits.ConnectionInitTimeout},
		{&output.ShutdownGrace, defaultLimits.ShutdownGrace, maximumLimits.ShutdownGrace},
	}
	for _, item := range durations {
		if *item.value == 0 {
			*item.value = item.fallback
		}
		if *item.value < 0 || *item.value > item.maximum {
			return Limits{}, Failure(CodeEventConfig)
		}
	}
	if output.RetryBase > output.RetryCap {
		return Limits{}, Failure(CodeEventConfig)
	}
	return output, nil
}

type MemoryLimits struct{ Buffer int }

func normalizeMemoryLimits(input MemoryLimits) (MemoryLimits, error) {
	if input.Buffer == 0 {
		input.Buffer = defaultLimits.HubInputQueue
	}
	if input.Buffer < 0 || input.Buffer > maximumLimits.HubInputQueue {
		return MemoryLimits{}, Failure(CodeEventConfig)
	}
	return input, nil
}
