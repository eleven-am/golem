package completion

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"time"
)

type FailureEvidence struct {
	FormatVersion uint16 `json:"formatVersion"`
	Command       string `json:"command"`
	Status        string `json:"status"`
	Code          Code   `json:"code"`
}

func Execute(ctx context.Context, command string, arguments []string, output io.Writer) int {
	defaultTimeout := 15 * time.Minute
	if command == "p8docs" || command == "p8failure" {
		defaultTimeout = 30 * time.Minute
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	module := flags.String("module", ".", "Go module root")
	timeout := flags.Duration("timeout", defaultTimeout, "complete command timeout")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || !closedCommand.MatchString(command) {
		return encodeFailure(output, command, CodeInvalidConfig)
	}
	root, err := filepath.Abs(*module)
	if err != nil {
		return encodeFailure(output, command, CodeInvalidConfig)
	}
	var spec Spec
	switch command {
	case "p8docs":
		spec = DocumentationSpec(root, *timeout)
	case "p8compat":
		spec = CompatibilitySpec(root, *timeout)
	case "p8failure":
		spec = FailureSpec(root, *timeout)
	default:
		return encodeFailure(output, command, CodeInvalidConfig)
	}
	evidence, runErr := Run(ctx, spec)
	if runErr != nil {
		var failure *Error
		if !errors.As(runErr, &failure) {
			return encodeFailure(output, command, CodeTestFailure)
		}
		return encodeFailure(output, command, failure.Code)
	}
	if err := json.NewEncoder(output).Encode(evidence); err != nil {
		return 2
	}
	return 0
}

func encodeFailure(output io.Writer, command string, code Code) int {
	failure := FailureEvidence{FormatVersion: FormatVersion, Command: command, Status: "FAIL", Code: code}
	if err := json.NewEncoder(output).Encode(failure); err != nil {
		return 2
	}
	return 1
}
