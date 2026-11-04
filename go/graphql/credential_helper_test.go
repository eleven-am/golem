package graphql

import (
	"context"
	"net/http"
	"time"
)

func withCredentialDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), time.Hour)
		defer cancel()
		next.ServeHTTP(writer, request.WithContext(ctx))
	})
}

type testDeadlineContext struct{ context.Context }

func (testDeadlineContext) Deadline() (time.Time, bool) { return time.Now().Add(time.Hour), true }

func withTestDeadline(ctx context.Context) context.Context { return testDeadlineContext{ctx} }
