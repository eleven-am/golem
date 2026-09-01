package events

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFailfCarriesDetailWithoutChangingClassification(t *testing.T) {
	bare := Failure(CodeEventConfig)
	if bare.Error() != string(CodeEventConfig) {
		t.Fatalf("bare failure = %q", bare.Error())
	}
	detailed := Failf(CodeEventConfig, "ClaimRows must be between 0 and %d, got %d", 1024, -1)
	want := string(CodeEventConfig) + ": ClaimRows must be between 0 and 1024, got -1"
	if detailed.Error() != want {
		t.Fatalf("detailed failure = %q want %q", detailed.Error(), want)
	}
	if !errors.Is(detailed, bare) || !errors.Is(bare, detailed) {
		t.Fatal("detail broke code-only identity")
	}
	if errors.Is(detailed, Failure(CodeEventCodec)) {
		t.Fatal("distinct codes compared equal")
	}
	if code, ok := CodeOf(fmt.Errorf("wrapped: %w", detailed)); !ok || code != CodeEventConfig {
		t.Fatalf("CodeOf(wrapped) = %q ok=%t", code, ok)
	}
	if code := (&Error{}).Code(); code != "" {
		t.Fatalf("zero error code = %q", code)
	}
}

func TestNormalizeLimitsNamesEveryViolatedBound(t *testing.T) {
	for name, limits := range map[string]Limits{
		"ClaimRows":                     {ClaimRows: -1},
		"PublisherConcurrency":          {PublisherConcurrency: -1},
		"MaxEncodedEventBytes":          {MaxEncodedEventBytes: maximumLimits.MaxEncodedEventBytes + 1},
		"SubscriberQueue":               {SubscriberQueue: maximumLimits.SubscriberQueue + 1},
		"HubInputQueue":                 {HubInputQueue: -1},
		"EvaluationConcurrency":         {EvaluationConcurrency: maximumLimits.EvaluationConcurrency + 1},
		"MaxSubscriptionsPerConnection": {MaxSubscriptionsPerConnection: -1},
		"ConnectionInitBytes":           {ConnectionInitBytes: maximumLimits.ConnectionInitBytes + 1},
		"RetentionDeleteRows":           {RetentionDeleteRows: -1},
		"LeaseDuration":                 {LeaseDuration: -1},
		"PublishTimeout":                {PublishTimeout: maximumLimits.PublishTimeout + 1},
		"RetryBase":                     {RetryBase: -1},
		"RetryCap":                      {RetryCap: maximumLimits.RetryCap + 1},
		"ConnectionInitTimeout":         {ConnectionInitTimeout: -1},
		"ShutdownGrace":                 {ShutdownGrace: maximumLimits.ShutdownGrace + 1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeLimits(limits)
			if errorCode(t, err) != CodeEventConfig {
				t.Fatalf("code = %v", err)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("error %q does not name %s", err, name)
			}
		})
	}
}

func TestNormalizeLimitsNamesTheRetryOrderingAndMemoryBuffer(t *testing.T) {
	_, err := NormalizeLimits(Limits{RetryBase: 2 * time.Second, RetryCap: time.Second})
	if errorCode(t, err) != CodeEventConfig {
		t.Fatalf("code = %v", err)
	}
	if !strings.Contains(err.Error(), "RetryBase") || !strings.Contains(err.Error(), "RetryCap") {
		t.Fatalf("retry ordering error %q names neither bound", err)
	}
	_, err = normalizeMemoryLimits(MemoryLimits{Buffer: -1})
	if errorCode(t, err) != CodeEventConfig {
		t.Fatalf("code = %v", err)
	}
	if !strings.Contains(err.Error(), "Buffer") {
		t.Fatalf("memory limits error %q does not name Buffer", err)
	}
}

func TestRetentionPolicyNamesTheViolatedBound(t *testing.T) {
	for name, testCase := range map[string]struct {
		olderThan time.Time
		maxRows   int
		names     string
	}{
		"zero cutoff":  {time.Time{}, 8, "OlderThan"},
		"zero rows":    {time.Unix(200, 0), 0, "MaxRows"},
		"negative":     {time.Unix(200, 0), -1, "MaxRows"},
		"beyond bound": {time.Unix(200, 0), maximumLimits.RetentionDeleteRows + 1, "MaxRows"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewRetentionPolicy(testCase.olderThan, testCase.maxRows)
			if errorCode(t, err) != CodeEventConfig {
				t.Fatalf("code = %v", err)
			}
			if !strings.Contains(err.Error(), testCase.names) {
				t.Fatalf("error %q does not name %s", err, testCase.names)
			}
		})
	}
	_, err := NewRetentionPolicy(time.Unix(200, 0), maximumLimits.RetentionDeleteRows+1)
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", maximumLimits.RetentionDeleteRows)) ||
		!strings.Contains(err.Error(), fmt.Sprintf("%d", maximumLimits.RetentionDeleteRows+1)) {
		t.Fatalf("error %q names neither the bound nor the offending value", err)
	}
}

func TestTransportCapabilitiesNameTheFieldWithoutEchoingTheIdentity(t *testing.T) {
	secret := "s3cr3t-tenant-token"
	oversized := secret + strings.Repeat("a", MaximumTransportIdentityBytes)
	for name, testCase := range map[string]struct {
		identity string
		scope    TransportScope
		names    string
	}{
		"empty identity":     {"", TransportScopeCrossProcess, "identity"},
		"oversized identity": {oversized, TransportScopeCrossProcess, "identity"},
		"noncanonical":       {"broker " + secret, TransportScopeCrossProcess, "identity"},
		"unknown scope":      {"broker.v1", TransportScope("scope-" + secret), "scope"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewTransportCapabilities(testCase.identity, testCase.scope, false)
			if errorCode(t, err) != CodeEventConfig {
				t.Fatalf("code = %v", err)
			}
			if !strings.Contains(err.Error(), testCase.names) {
				t.Fatalf("error %q does not name %s", err, testCase.names)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error %q echoed caller-supplied content", err)
			}
		})
	}
	_, err := NewTransportCapabilities(oversized, TransportScopeCrossProcess, false)
	if !strings.Contains(err.Error(), fmt.Sprintf("%d", MaximumTransportIdentityBytes)) ||
		!strings.Contains(err.Error(), fmt.Sprintf("%d", len(oversized))) {
		t.Fatalf("oversized identity error %q names neither the bound nor the length", err)
	}
	_, err = NewTransportCapabilities("broker.v1", TransportScope("internet"), false)
	if !strings.Contains(err.Error(), string(TransportScopeProcessLocal)) ||
		!strings.Contains(err.Error(), string(TransportScopeCrossProcess)) {
		t.Fatalf("scope error %q does not name the valid scopes", err)
	}
}
