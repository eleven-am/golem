package nats

import (
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/events"
)

const (
	credentialUser = "operator"
	credentialPass = "hunter2"
	credentialHost = "broker.internal"
	credentialPort = "4222"
)

var credentialedURL = "nats://" + credentialUser + ":" + credentialPass + "@" + credentialHost + ":" + credentialPort

func TestOrder7ConfigErrorsNameTheViolatedFieldWithoutEchoingURLCredentials(t *testing.T) {
	for name, testCase := range map[string]struct {
		config Config
		names  string
	}{
		"absent URLs":            {Config{SubjectPrefix: "deployment"}, "URLs"},
		"too many URLs":          {Config{URLs: tooManyURLs(), SubjectPrefix: "deployment"}, "URLs"},
		"empty URL":              {Config{URLs: []string{credentialedURL, ""}, SubjectPrefix: "deployment"}, "URLs[1]"},
		"oversized URL":          {Config{URLs: []string{"nats://" + strings.Repeat("h", maximumURLBytes)}, SubjectPrefix: "deployment"}, "URLs[0]"},
		"injected URL list":      {Config{URLs: []string{credentialedURL + ",nats://other"}, SubjectPrefix: "deployment"}, "URLs[0]"},
		"unsupported scheme":     {Config{URLs: []string{strings.Replace(credentialedURL, "nats://", "http://", 1)}, SubjectPrefix: "deployment"}, "URLs[0]"},
		"absent prefix":          {Config{URLs: []string{credentialedURL}}, "SubjectPrefix"},
		"oversized prefix":       {Config{URLs: []string{credentialedURL}, SubjectPrefix: strings.Repeat("p", maximumSubjectPrefixBytes+1)}, "SubjectPrefix"},
		"wildcard prefix":        {Config{URLs: []string{credentialedURL}, SubjectPrefix: "golem.>"}, "SubjectPrefix"},
		"negative connect":       {Config{URLs: []string{credentialedURL}, SubjectPrefix: "deployment", ConnectTimeout: -1}, "ConnectTimeout"},
		"oversized flush":        {Config{URLs: []string{credentialedURL}, SubjectPrefix: "deployment", FlushTimeout: maximumTimeout + 1}, "FlushTimeout"},
		"oversized wait":         {Config{URLs: []string{credentialedURL}, SubjectPrefix: "deployment", ReconnectWait: maximumReconnectWait + 1}, "ReconnectWait"},
		"negative reconnects":    {Config{URLs: []string{credentialedURL}, SubjectPrefix: "deployment", MaxReconnects: -1}, "MaxReconnects"},
		"oversized buffer":       {Config{URLs: []string{credentialedURL}, SubjectPrefix: "deployment", ReconnectBufferBytes: maximumReconnectBuffer + 1}, "ReconnectBufferBytes"},
		"oversized payload":      {Config{URLs: []string{credentialedURL}, SubjectPrefix: "deployment", MaxInboundPayloadBytes: maximumInboundPayload + 1}, "MaxInboundPayloadBytes"},
		"oversized pending":      {Config{URLs: []string{credentialedURL}, SubjectPrefix: "deployment", PendingMessages: maximumPendingMessages + 1}, "PendingMessages"},
		"oversized pending byte": {Config{URLs: []string{credentialedURL}, SubjectPrefix: "deployment", PendingBytes: maximumPendingBytes + 1}, "PendingBytes"},
		"negative queue":         {Config{URLs: []string{credentialedURL}, SubjectPrefix: "deployment", StreamBuffer: -1}, "StreamBuffer"},
		"queue exceeds pending":  {Config{URLs: []string{credentialedURL}, SubjectPrefix: "deployment", PendingMessages: 8, StreamBuffer: 16}, "PendingMessages"},
		"payload exceeds bytes":  {Config{URLs: []string{credentialedURL}, SubjectPrefix: "deployment", PendingBytes: 1024, MaxInboundPayloadBytes: 2048}, "PendingBytes"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeConfig(testCase.config)
			if eventCode(err) != events.CodeEventConfig {
				t.Fatalf("code = %v", err)
			}
			if !strings.Contains(err.Error(), testCase.names) {
				t.Fatalf("error %q does not name %s", err, testCase.names)
			}
			assertNoURLCredentials(t, err)
		})
	}
}

func assertNoURLCredentials(t testing.TB, err error) {
	t.Helper()
	for _, secret := range []string{credentialUser, credentialPass, credentialHost, credentialPort, credentialedURL} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q echoed URL secret %q", err, secret)
		}
	}
}

func tooManyURLs() []string {
	urls := make([]string, maximumURLs+1)
	for index := range urls {
		urls[index] = credentialedURL
	}
	return urls
}
