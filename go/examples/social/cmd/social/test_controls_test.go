package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
)

func envFrom(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestTestControlsAreAbsentWithoutTheInsecureFlag(t *testing.T) {
	if newTestControlsFromEnvironment(envFrom(nil)) != nil {
		t.Fatal("controls exist without the insecure flag")
	}
	limits := hostEventLimits(envFrom(map[string]string{"GOLEM_TEST_REVALIDATE_MS": "1000", "GOLEM_TEST_SUBSCRIBER_QUEUE": "2"}))
	if limits.RevalidateEvery != 5*time.Minute || limits.SubscriberQueue != 0 {
		t.Fatalf("limits changed without the insecure flag: %+v", limits)
	}
	if hostSubscriptionRecheck(envFrom(map[string]string{"GOLEM_TEST_RECHECK_MS": "1000"}), nil).Check != nil {
		t.Fatal("recheck exists without controls")
	}
}

func TestTestControlsOverrideLimitsAndPrincipalModes(t *testing.T) {
	env := envFrom(map[string]string{"GOLEM_ALLOW_INSECURE_HEADER_AUTH": "1", "GOLEM_TEST_REVALIDATE_MS": "1000", "GOLEM_TEST_SUBSCRIBER_QUEUE": "2", "GOLEM_TEST_RECHECK_MS": "1000"})
	limits := hostEventLimits(env)
	if limits.RevalidateEvery != time.Second || limits.SubscriberQueue != 2 {
		t.Fatalf("limits not applied: %+v", limits)
	}
	controls := newTestControlsFromEnvironment(env)
	user := golem.UUID{}
	if controls.modeOf(user) != principalModeNormal {
		t.Fatal("default mode is not normal")
	}
	mux := http.NewServeMux()
	registerTestControls(mux, controls, nil)
	request := httptest.NewRequest(http.MethodPost, "/test/principal?user="+user.String()+"&mode=forbidden", nil)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent || controls.modeOf(user) != principalModeForbidden {
		t.Fatalf("mode not set: %d", recorder.Code)
	}
	bad := httptest.NewRecorder()
	mux.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/test/principal?user="+user.String()+"&mode=other", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown mode accepted: %d", bad.Code)
	}
}
