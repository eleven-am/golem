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

var canonicalSubjectPrefix = regexp.MustCompile(`^[A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)*$`)

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
	if len(output.URLs) == 0 || len(output.URLs) > maximumURLs {
		return normalizedConfig{}, events.Failure(events.CodeEventConfig)
	}
	serverList := ""
	for index, server := range output.URLs {
		if len(server) == 0 || len(server) > maximumURLBytes || strings.ContainsAny(server, ",\r\n\t ") || (!strings.HasPrefix(server, "nats://") && !strings.HasPrefix(server, "tls://")) {
			return normalizedConfig{}, events.Failure(events.CodeEventConfig)
		}
		if index != 0 {
			serverList += ","
		}
		serverList += server
	}
	if len(output.SubjectPrefix) == 0 || len(output.SubjectPrefix) > maximumSubjectPrefixBytes || !canonicalSubjectPrefix.MatchString(output.SubjectPrefix) {
		return normalizedConfig{}, events.Failure(events.CodeEventConfig)
	}
	if output.ConnectTimeout == 0 {
		output.ConnectTimeout = defaultConnectTimeout
	}
	if output.FlushTimeout == 0 {
		output.FlushTimeout = defaultFlushTimeout
	}
	if output.ReconnectWait == 0 {
		output.ReconnectWait = defaultReconnectWait
	}
	if output.MaxReconnects == 0 {
		output.MaxReconnects = defaultMaxReconnects
	}
	if output.ReconnectBufferBytes == 0 {
		output.ReconnectBufferBytes = defaultReconnectBuffer
	}
	if output.MaxInboundPayloadBytes == 0 {
		output.MaxInboundPayloadBytes = defaultMaxInboundPayload
	}
	if output.PendingMessages == 0 {
		output.PendingMessages = defaultPendingMessages
	}
	if output.PendingBytes == 0 {
		output.PendingBytes = defaultPendingBytes
	}
	if output.StreamBuffer == 0 {
		output.StreamBuffer = defaultStreamBuffer
	}
	if output.ConnectTimeout < 0 || output.ConnectTimeout > maximumTimeout || output.FlushTimeout < 0 || output.FlushTimeout > maximumTimeout || output.ReconnectWait < 0 || output.ReconnectWait > maximumReconnectWait || output.MaxReconnects < 0 || output.MaxReconnects > maximumReconnects || output.ReconnectBufferBytes < 0 || output.ReconnectBufferBytes > maximumReconnectBuffer || output.MaxInboundPayloadBytes < 0 || output.MaxInboundPayloadBytes > maximumInboundPayload || output.PendingMessages < 0 || output.PendingMessages > maximumPendingMessages || output.PendingBytes < 0 || output.PendingBytes > maximumPendingBytes || output.StreamBuffer < 0 || output.StreamBuffer > maximumStreamBuffer {
		return normalizedConfig{}, events.Failure(events.CodeEventConfig)
	}
	if output.PendingMessages < output.StreamBuffer || output.PendingBytes < output.MaxInboundPayloadBytes {
		return normalizedConfig{}, events.Failure(events.CodeEventConfig)
	}
	return normalizedConfig{Config: output, serverList: serverList}, nil
}
