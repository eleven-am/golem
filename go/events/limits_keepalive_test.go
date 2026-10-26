package events

import (
	"strings"
	"testing"
	"time"
)

func TestKeepAliveAcceptsTheWebSocketKeepAliveAlias(t *testing.T) {
	for name, testCase := range map[string]struct {
		input Limits
		want  time.Duration
	}{
		"default":            {Limits{}, defaultLimits.KeepAlive},
		"keep alive":         {Limits{KeepAlive: 7 * time.Second}, 7 * time.Second},
		"deprecated alias":   {Limits{WebSocketKeepAlive: 9 * time.Second}, 9 * time.Second},
		"both equal":         {Limits{KeepAlive: 7 * time.Second, WebSocketKeepAlive: 7 * time.Second}, 7 * time.Second},
		"keep alive at most": {Limits{KeepAlive: maximumLimits.KeepAlive}, maximumLimits.KeepAlive},
	} {
		t.Run(name, func(t *testing.T) {
			normalized, err := NormalizeLimits(testCase.input)
			if err != nil {
				t.Fatal(err)
			}
			if normalized.KeepAlive != testCase.want || normalized.WebSocketKeepAlive != testCase.want {
				t.Fatalf("KeepAlive=%s WebSocketKeepAlive=%s, want both %s", normalized.KeepAlive, normalized.WebSocketKeepAlive, testCase.want)
			}
		})
	}
	if defaultLimits.KeepAlive != 30*time.Second || DefaultLimits().WebSocketKeepAlive != defaultLimits.KeepAlive || MaximumLimits().WebSocketKeepAlive != maximumLimits.KeepAlive {
		t.Fatalf("default=%s alias default=%s alias maximum=%s", defaultLimits.KeepAlive, DefaultLimits().WebSocketKeepAlive, MaximumLimits().WebSocketKeepAlive)
	}
}

func TestKeepAliveRefusalNamesTheFieldTheCallerSet(t *testing.T) {
	for name, testCase := range map[string]struct {
		limits Limits
		other  string
	}{
		"KeepAlive":          {Limits{KeepAlive: maximumLimits.KeepAlive + 1}, "WebSocketKeepAlive"},
		"WebSocketKeepAlive": {Limits{WebSocketKeepAlive: -1}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NormalizeLimits(testCase.limits)
			if errorCode(t, err) != CodeEventConfig {
				t.Fatalf("code = %v", err)
			}
			if !strings.Contains(err.Error(), name+" must not") || testCase.other != "" && strings.Contains(err.Error(), testCase.other) {
				t.Fatalf("error %q does not name %s alone", err, name)
			}
		})
	}
	for _, limits := range []Limits{
		{KeepAlive: 7 * time.Second, WebSocketKeepAlive: 9 * time.Second},
		{KeepAlive: time.Second, WebSocketKeepAlive: -1},
	} {
		_, err := NormalizeLimits(limits)
		if errorCode(t, err) != CodeEventConfig {
			t.Fatalf("conflicting keep-alive fields %+v: %v", limits, err)
		}
		if !strings.Contains(err.Error(), "KeepAlive (") || !strings.Contains(err.Error(), "WebSocketKeepAlive (") {
			t.Fatalf("conflict error %q does not name both fields", err)
		}
	}
}
