package main

import (
	"context"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/internal/p7verify"
)

func TestP7CrashCommandMissingRequiredPostgreSQLProfilesIsFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := p7verify.RunCrashMatrix(ctx, p7verify.CrashConfig{
		Executable: "unused-because-profile-validation-precedes-children",
		TempRoot:   t.TempDir(),
		Env: []string{
			p7verify.CrashPostgresDSN + "=",
			p7verify.CrashPostgresLinguisticDSN + "=",
		},
	})
	if err == nil {
		t.Fatal("missing required profiles returned success")
	}
}
