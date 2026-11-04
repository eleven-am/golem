package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/examples/social/social"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/graphql"
)

type principalMode string

const (
	principalModeNormal       principalMode = ""
	principalModeUnresolvable principalMode = "unresolvable"
	principalModeForbidden    principalMode = "forbidden"
)

type testControls struct {
	mutex sync.RWMutex
	modes map[golem.UUID]principalMode
}

var activeTestControls = newTestControlsFromEnvironment(os.Getenv)

func newTestControlsFromEnvironment(getenv func(string) string) *testControls {
	if getenv("GOLEM_ALLOW_INSECURE_HEADER_AUTH") != "1" {
		return nil
	}
	return &testControls{modes: map[golem.UUID]principalMode{}}
}

func (controls *testControls) modeOf(user golem.UUID) principalMode {
	if controls == nil {
		return principalModeNormal
	}
	controls.mutex.RLock()
	defer controls.mutex.RUnlock()
	return controls.modes[user]
}

func (controls *testControls) setMode(user golem.UUID, mode principalMode) {
	controls.mutex.Lock()
	defer controls.mutex.Unlock()
	if mode == principalModeNormal {
		delete(controls.modes, user)
		return
	}
	controls.modes[user] = mode
}

func positiveMilliseconds(getenv func(string) string, name string) time.Duration {
	milliseconds, err := strconv.Atoi(strings.TrimSpace(getenv(name)))
	if err != nil || milliseconds <= 0 {
		return 0
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func hostEventLimits(getenv func(string) string) events.Limits {
	limits := events.Limits{RevalidateEvery: 5 * time.Minute}
	if getenv("GOLEM_ALLOW_INSECURE_HEADER_AUTH") != "1" {
		return limits
	}
	if every := positiveMilliseconds(getenv, "GOLEM_TEST_REVALIDATE_MS"); every > 0 {
		limits.RevalidateEvery = every
	}
	if queue, err := strconv.Atoi(strings.TrimSpace(getenv("GOLEM_TEST_SUBSCRIBER_QUEUE"))); err == nil && queue > 0 {
		limits.SubscriberQueue = queue
	}
	return limits
}

func hostSubscriptionRecheck(getenv func(string) string, controls *testControls) graphql.SubscriptionRecheck[social.Principal] {
	every := positiveMilliseconds(getenv, "GOLEM_TEST_RECHECK_MS")
	if controls == nil || every <= 0 {
		return graphql.SubscriptionRecheck[social.Principal]{}
	}
	return graphql.SubscriptionRecheck[social.Principal]{
		Interval: every,
		Check: func(_ context.Context, principal social.Principal, _ graphql.Operation) error {
			if principal.Development && controls.modeOf(principal.DevUserID) == principalModeForbidden {
				return errors.New("access revoked")
			}
			return nil
		},
	}
}

func registerTestControls(mux *http.ServeMux, controls *testControls, application *social.App[social.Principal]) {
	if controls == nil {
		return
	}
	mux.HandleFunc("/test/invalidate-policies", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		application.InvalidatePolicies()
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/test/principal", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		user, err := golem.ParseUUID(request.URL.Query().Get("user"))
		if err != nil {
			http.Error(writer, "invalid user", http.StatusBadRequest)
			return
		}
		mode := principalMode(request.URL.Query().Get("mode"))
		switch mode {
		case principalModeNormal, principalModeUnresolvable, principalModeForbidden:
			controls.setMode(user, mode)
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.Error(writer, "invalid mode", http.StatusBadRequest)
		}
	})
}
