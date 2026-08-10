package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/p8verify"
)

func TestP8VerifyCommandRejectsInvalidInputWithClosedRedactedJSON(t *testing.T) {
	const canary = "postgresql://user:private-password@private-host/database"
	var output bytes.Buffer
	if code := execute(context.Background(), []string{"--timeout", canary}, &output); code != 1 {
		t.Fatalf("exit=%d", code)
	}
	var failure struct {
		FormatVersion uint16             `json:"formatVersion"`
		Command       string             `json:"command"`
		Status        string             `json:"status"`
		Code          p8verify.ErrorCode `json:"code"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&failure); err != nil || failure.FormatVersion != 1 || failure.Command != "p8verify" || failure.Status != "FAIL" || failure.Code != p8verify.CodeConfig {
		t.Fatalf("failure=%#v err=%v", failure, err)
	}
	if strings.Contains(output.String(), canary) || strings.Contains(output.String(), "private-password") || strings.Contains(output.String(), "private-host") {
		t.Fatalf("failure disclosed input: %s", output.String())
	}
}
