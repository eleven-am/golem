package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestTestCredentialWindowHeaders(t *testing.T) {
	fixed := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		headers   map[string]string
		unbounded bool
		deadline  func(time.Time) bool
	}{
		{"no headers keep the expiry", nil, false, func(got time.Time) bool { return got.Equal(fixed) }},
		{"unbounded", map[string]string{"X-Golem-Test-Unbounded": "1"}, true, func(got time.Time) bool { return got.IsZero() }},
		{"deadline", map[string]string{"X-Golem-Test-Deadline-Ms": "1500"}, false, func(got time.Time) bool {
			return got.After(time.Now().Add(time.Second)) && got.Before(time.Now().Add(2*time.Second))
		}},
		{"invalid deadline keeps the expiry", map[string]string{"X-Golem-Test-Deadline-Ms": "soon"}, false, func(got time.Time) bool { return got.Equal(fixed) }},
		{"non-positive deadline keeps the expiry", map[string]string{"X-Golem-Test-Deadline-Ms": "0"}, false, func(got time.Time) bool { return got.Equal(fixed) }},
	}
	for _, current := range cases {
		t.Run(current.name, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/graphql", nil)
			for name, value := range current.headers {
				request.Header.Set(name, value)
			}
			got, unbounded := testCredentialWindow(request, fixed)
			if unbounded != current.unbounded || !current.deadline(got) {
				t.Fatalf("deadline=%v unbounded=%v", got, unbounded)
			}
		})
	}
}
