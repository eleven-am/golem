// Package nats provides Golem's maintained Core NATS event transport.
//
// PostgreSQL remains the durable event source. This adapter provides live,
// at-least-once, cross-process fan-out and deliberately does not use JetStream.
package nats

import (
	"regexp"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/events"
)

const (
	defaultConnectTimeout        = 5 * time.Second
	defaultFlushTimeout          = 15 * time.Second
	defaultReconnectWait         = time.Second
	defaultMaxReconnects         = 60
	defaultReconnectBuffer       = 8 << 20
	defaultMaxInboundPayload     = 2 << 20
	defaultPendingMessages       = 64
	defaultPendingBytes          = 8 << 20
	defaultStreamBuffer          = 64
	maximumSubjectPrefixBytes    = 128
	maximumURLBytes              = 4096
	maximumURLs                  = 32
	maximumTimeout               = 2 * time.Minute
	maximumReconnectWait         = time.Minute
	maximumReconnects            = 10_000
	maximumReconnectBuffer       = 64 << 20
	maximumInboundPayload        = 16 << 20
	maximumPendingMessages       = 4096
	maximumPendingBytes          = 64 << 20
	maximumStreamBuffer          = 4096
	maximumReconnectObservations = 64
)

const canonicalSubjectPrefixPattern = `^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`

var canonicalSubjectPrefix = regexp.MustCompile(canonicalSubjectPrefixPattern)

// Config is a closed, bounded Core NATS client configuration. SubjectPrefix is
// required and must be unique to the authoritative database within the NATS
// account. URLs may contain credentials for deployments that choose URL
// authentication; they are never returned in public errors.
type Config struct {
	URLs                   []string
	SubjectPrefix          string
	Observer               events.Observer
	ConnectTimeout         time.Duration
	FlushTimeout           time.Duration
	ReconnectWait          time.Duration
	MaxReconnects          int
	ReconnectBufferBytes   int
	MaxInboundPayloadBytes int
	PendingMessages        int
	PendingBytes           int
	StreamBuffer           int
}

type normalizedConfig struct {
	Config
	serverList string
}

func normalizeConfig(input Config) (normalizedConfig, error) {
	output := input
	output.URLs = append([]string(nil), input.URLs...)
	if len(output.URLs) == 0 {
		return normalizedConfig{}, events.Failf(events.CodeEventConfig, "URLs must list at least one server")
	}
	if len(output.URLs) > maximumURLs {
		return normalizedConfig{}, events.Failf(events.CodeEventConfig, "URLs must list at most %d servers, got %d", maximumURLs, len(output.URLs))
	}
	serverList := ""
	for index, server := range output.URLs {
		if len(server) == 0 {
			return normalizedConfig{}, events.Failf(events.CodeEventConfig, "URLs[%d] must not be empty", index)
		}
		if len(server) > maximumURLBytes {
			return normalizedConfig{}, events.Failf(events.CodeEventConfig, "URLs[%d] must not exceed %d bytes", index, maximumURLBytes)
		}
		if strings.ContainsAny(server, ",\r\n\t ") {
			return normalizedConfig{}, events.Failf(events.CodeEventConfig, "URLs[%d] must not contain a comma or whitespace; list each server separately", index)
		}
		if !strings.HasPrefix(server, "nats://") && !strings.HasPrefix(server, "tls://") {
			return normalizedConfig{}, events.Failf(events.CodeEventConfig, "URLs[%d] must use the nats:// or tls:// scheme", index)
		}
		if index != 0 {
			serverList += ","
		}
		serverList += server
	}
	if len(output.SubjectPrefix) == 0 {
		return normalizedConfig{}, events.Failf(events.CodeEventConfig, "SubjectPrefix must not be empty")
	}
	if len(output.SubjectPrefix) > maximumSubjectPrefixBytes {
		return normalizedConfig{}, events.Failf(events.CodeEventConfig, "SubjectPrefix must not exceed %d bytes", maximumSubjectPrefixBytes)
	}
	if !canonicalSubjectPrefix.MatchString(output.SubjectPrefix) {
		return normalizedConfig{}, events.Failf(events.CodeEventConfig, "SubjectPrefix must match %s", canonicalSubjectPrefixPattern)
	}
	durations := []struct {
		name              string
		value             *time.Duration
		fallback, maximum time.Duration
	}{
		{"ConnectTimeout", &output.ConnectTimeout, defaultConnectTimeout, maximumTimeout},
		{"FlushTimeout", &output.FlushTimeout, defaultFlushTimeout, maximumTimeout},
		{"ReconnectWait", &output.ReconnectWait, defaultReconnectWait, maximumReconnectWait},
	}
	for _, item := range durations {
		if *item.value == 0 {
			*item.value = item.fallback
		}
		if *item.value < 0 {
			return normalizedConfig{}, events.Failf(events.CodeEventConfig, "%s must not be negative, got %s", item.name, *item.value)
		}
		if *item.value > item.maximum {
			return normalizedConfig{}, events.Failf(events.CodeEventConfig, "%s must not exceed %s, got %s", item.name, item.maximum, *item.value)
		}
	}
	integers := []struct {
		name              string
		value             *int
		fallback, maximum int
	}{
		{"MaxReconnects", &output.MaxReconnects, defaultMaxReconnects, maximumReconnects},
		{"ReconnectBufferBytes", &output.ReconnectBufferBytes, defaultReconnectBuffer, maximumReconnectBuffer},
		{"MaxInboundPayloadBytes", &output.MaxInboundPayloadBytes, defaultMaxInboundPayload, maximumInboundPayload},
		{"PendingMessages", &output.PendingMessages, defaultPendingMessages, maximumPendingMessages},
		{"PendingBytes", &output.PendingBytes, defaultPendingBytes, maximumPendingBytes},
		{"StreamBuffer", &output.StreamBuffer, defaultStreamBuffer, maximumStreamBuffer},
	}
	for _, item := range integers {
		if *item.value == 0 {
			*item.value = item.fallback
		}
		if *item.value < 0 {
			return normalizedConfig{}, events.Failf(events.CodeEventConfig, "%s must not be negative, got %d", item.name, *item.value)
		}
		if *item.value > item.maximum {
			return normalizedConfig{}, events.Failf(events.CodeEventConfig, "%s must not exceed %d, got %d", item.name, item.maximum, *item.value)
		}
	}
	if output.PendingMessages < output.StreamBuffer {
		return normalizedConfig{}, events.Failf(events.CodeEventConfig, "PendingMessages (%d) must be at least StreamBuffer (%d)", output.PendingMessages, output.StreamBuffer)
	}
	if output.PendingBytes < output.MaxInboundPayloadBytes {
		return normalizedConfig{}, events.Failf(events.CodeEventConfig, "PendingBytes (%d) must be at least MaxInboundPayloadBytes (%d)", output.PendingBytes, output.MaxInboundPayloadBytes)
	}
	return normalizedConfig{Config: output, serverList: serverList}, nil
}
