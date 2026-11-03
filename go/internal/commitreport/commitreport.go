package commitreport

import (
	"context"
	"sync/atomic"
)

type contextKey struct{}

type Report struct {
	committed atomic.Bool
	unknown   atomic.Bool
}

func Install(ctx context.Context) (context.Context, *Report) {
	report := &Report{}
	return context.WithValue(ctx, contextKey{}, report), report
}

func From(ctx context.Context) *Report {
	if ctx == nil {
		return nil
	}
	report, _ := ctx.Value(contextKey{}).(*Report)
	return report
}

func (report *Report) Committed() {
	if report != nil {
		report.committed.Store(true)
	}
}

func (report *Report) Unknown() {
	if report != nil {
		report.unknown.Store(true)
	}
}

func (report *Report) State() (string, bool) {
	switch {
	case report == nil:
		return "", false
	case report.unknown.Load():
		return "UNKNOWN", true
	case report.committed.Load():
		return "PARTIAL", true
	}
	return "", false
}
