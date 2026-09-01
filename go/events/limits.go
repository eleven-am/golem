package events

import (
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
)

type Limits struct {
	ClaimRows                     int
	PublisherConcurrency          int
	LeaseDuration                 time.Duration
	PublishTimeout                time.Duration
	RetryBase                     time.Duration
	RetryCap                      time.Duration
	MaxEncodedEventBytes          int
	MaxEncodedBatchBytes          int
	SubscriberQueue               int
	HubInputQueue                 int
	EvaluationConcurrency         int
	MaxSubscriptionsPerConnection int
	ConnectionInitBytes           int
	ConnectionInitTimeout         time.Duration
	WebSocketKeepAlive            time.Duration
	WebSocketPongTimeout          time.Duration
	ShutdownGrace                 time.Duration
	RetentionDeleteRows           int
	RetentionAge                  time.Duration
	RetentionEvery                time.Duration
}

var defaultLimits = Limits{
	ClaimRows: 64, PublisherConcurrency: 8, LeaseDuration: 30 * time.Second,
	PublishTimeout: 15 * time.Second, RetryBase: 250 * time.Millisecond,
	RetryCap: 5 * time.Minute, MaxEncodedEventBytes: 2 << 20, MaxEncodedBatchBytes: 16 << 20,
	SubscriberQueue: 64, HubInputQueue: 256, EvaluationConcurrency: 32,
	MaxSubscriptionsPerConnection: 32, ConnectionInitBytes: 64 << 10,
	ConnectionInitTimeout: 10 * time.Second, WebSocketKeepAlive: 30 * time.Second, WebSocketPongTimeout: 10 * time.Second, ShutdownGrace: 15 * time.Second,
	RetentionDeleteRows: eventprovider.MaximumCausationFacts, RetentionAge: 30 * 24 * time.Hour, RetentionEvery: time.Minute,
}

var maximumLimits = Limits{
	ClaimRows: 1024, PublisherConcurrency: 128, LeaseDuration: 10 * time.Minute,
	PublishTimeout: 2 * time.Minute, RetryBase: time.Minute,
	RetryCap: 24 * time.Hour, MaxEncodedEventBytes: 16 << 20, MaxEncodedBatchBytes: 64 << 20,
	SubscriberQueue: 4096, HubInputQueue: 16384, EvaluationConcurrency: 256,
	MaxSubscriptionsPerConnection: 256, ConnectionInitBytes: 1 << 20,
	ConnectionInitTimeout: time.Minute, WebSocketKeepAlive: 5 * time.Minute, WebSocketPongTimeout: time.Minute, ShutdownGrace: 2 * time.Minute,
	RetentionDeleteRows: 4096, RetentionAge: 10 * 365 * 24 * time.Hour, RetentionEvery: 24 * time.Hour,
}

func DefaultLimits() Limits { return defaultLimits }
func MaximumLimits() Limits { return maximumLimits }

func NormalizeLimits(input Limits) (Limits, error) {
	output := input
	integers := []struct {
		name              string
		value             *int
		fallback, maximum int
	}{
		{"ClaimRows", &output.ClaimRows, defaultLimits.ClaimRows, maximumLimits.ClaimRows},
		{"PublisherConcurrency", &output.PublisherConcurrency, defaultLimits.PublisherConcurrency, maximumLimits.PublisherConcurrency},
		{"MaxEncodedEventBytes", &output.MaxEncodedEventBytes, defaultLimits.MaxEncodedEventBytes, maximumLimits.MaxEncodedEventBytes},
		{"MaxEncodedBatchBytes", &output.MaxEncodedBatchBytes, defaultLimits.MaxEncodedBatchBytes, maximumLimits.MaxEncodedBatchBytes},
		{"SubscriberQueue", &output.SubscriberQueue, defaultLimits.SubscriberQueue, maximumLimits.SubscriberQueue},
		{"HubInputQueue", &output.HubInputQueue, defaultLimits.HubInputQueue, maximumLimits.HubInputQueue},
		{"EvaluationConcurrency", &output.EvaluationConcurrency, defaultLimits.EvaluationConcurrency, maximumLimits.EvaluationConcurrency},
		{"MaxSubscriptionsPerConnection", &output.MaxSubscriptionsPerConnection, defaultLimits.MaxSubscriptionsPerConnection, maximumLimits.MaxSubscriptionsPerConnection},
		{"ConnectionInitBytes", &output.ConnectionInitBytes, defaultLimits.ConnectionInitBytes, maximumLimits.ConnectionInitBytes},
		{"RetentionDeleteRows", &output.RetentionDeleteRows, defaultLimits.RetentionDeleteRows, maximumLimits.RetentionDeleteRows},
	}
	for _, item := range integers {
		if *item.value == 0 {
			*item.value = item.fallback
		}
		if *item.value < 0 {
			return Limits{}, Failf(CodeEventConfig, "%s must not be negative, got %d", item.name, *item.value)
		}
		if *item.value > item.maximum {
			return Limits{}, Failf(CodeEventConfig, "%s must not exceed %d, got %d", item.name, item.maximum, *item.value)
		}
	}
	durations := []struct {
		name              string
		value             *time.Duration
		fallback, maximum time.Duration
	}{
		{"LeaseDuration", &output.LeaseDuration, defaultLimits.LeaseDuration, maximumLimits.LeaseDuration},
		{"PublishTimeout", &output.PublishTimeout, defaultLimits.PublishTimeout, maximumLimits.PublishTimeout},
		{"RetryBase", &output.RetryBase, defaultLimits.RetryBase, maximumLimits.RetryBase},
		{"RetryCap", &output.RetryCap, defaultLimits.RetryCap, maximumLimits.RetryCap},
		{"ConnectionInitTimeout", &output.ConnectionInitTimeout, defaultLimits.ConnectionInitTimeout, maximumLimits.ConnectionInitTimeout},
		{"WebSocketKeepAlive", &output.WebSocketKeepAlive, defaultLimits.WebSocketKeepAlive, maximumLimits.WebSocketKeepAlive},
		{"WebSocketPongTimeout", &output.WebSocketPongTimeout, defaultLimits.WebSocketPongTimeout, maximumLimits.WebSocketPongTimeout},
		{"ShutdownGrace", &output.ShutdownGrace, defaultLimits.ShutdownGrace, maximumLimits.ShutdownGrace},
		{"RetentionAge", &output.RetentionAge, defaultLimits.RetentionAge, maximumLimits.RetentionAge},
		{"RetentionEvery", &output.RetentionEvery, defaultLimits.RetentionEvery, maximumLimits.RetentionEvery},
	}
	for _, item := range durations {
		if *item.value == 0 {
			*item.value = item.fallback
		}
		if *item.value < 0 {
			return Limits{}, Failf(CodeEventConfig, "%s must not be negative, got %s", item.name, *item.value)
		}
		if *item.value > item.maximum {
			return Limits{}, Failf(CodeEventConfig, "%s must not exceed %s, got %s", item.name, item.maximum, *item.value)
		}
	}
	if output.RetryBase > output.RetryCap {
		return Limits{}, Failf(CodeEventConfig, "RetryBase (%s) must not exceed RetryCap (%s)", output.RetryBase, output.RetryCap)
	}
	if output.MaxEncodedBatchBytes < output.MaxEncodedEventBytes {
		return Limits{}, Failf(CodeEventConfig, "MaxEncodedBatchBytes (%d) must not be below MaxEncodedEventBytes (%d)", output.MaxEncodedBatchBytes, output.MaxEncodedEventBytes)
	}
	if output.RetentionAge < time.Hour {
		return Limits{}, Failf(CodeEventConfig, "RetentionAge must be at least 1h, got %s", output.RetentionAge)
	}
	if output.RetentionEvery < time.Minute {
		return Limits{}, Failf(CodeEventConfig, "RetentionEvery must be at least 1m, got %s", output.RetentionEvery)
	}
	if output.RetentionDeleteRows < eventprovider.MaximumCausationFacts {
		return Limits{}, Failf(CodeEventConfig, "RetentionDeleteRows must be at least %d, got %d", eventprovider.MaximumCausationFacts, output.RetentionDeleteRows)
	}
	if output.WebSocketPongTimeout >= output.WebSocketKeepAlive {
		return Limits{}, Failf(CodeEventConfig, "WebSocketPongTimeout (%s) must be below WebSocketKeepAlive (%s)", output.WebSocketPongTimeout, output.WebSocketKeepAlive)
	}
	return output, nil
}

type MemoryLimits struct{ Buffer int }

func normalizeMemoryLimits(input MemoryLimits) (MemoryLimits, error) {
	if input.Buffer == 0 {
		input.Buffer = defaultLimits.HubInputQueue
	}
	if input.Buffer < 0 {
		return MemoryLimits{}, Failf(CodeEventConfig, "Buffer must not be negative, got %d", input.Buffer)
	}
	if input.Buffer > maximumLimits.HubInputQueue {
		return MemoryLimits{}, Failf(CodeEventConfig, "Buffer must not exceed %d, got %d", maximumLimits.HubInputQueue, input.Buffer)
	}
	return input, nil
}
