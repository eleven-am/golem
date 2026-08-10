package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/eleven-am/golem/go/internal/p8verify"
)

func main() {
	os.Exit(execute(context.Background(), os.Args[1:], os.Stdout))
}

func execute(ctx context.Context, arguments []string, output io.Writer) int {
	flags := flag.NewFlagSet("p8verify", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	module := flags.String("module", ".", "Go module root")
	timeout := flags.Duration("timeout", 30*time.Minute, "local candidate audit timeout")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || *timeout <= 0 {
		return encodeFailure(output, p8verify.CodeConfig)
	}
	root, err := filepath.Abs(*module)
	if err != nil {
		return encodeFailure(output, p8verify.CodeConfig)
	}
	report, err := p8verify.RunLocalAudit(ctx, root, *timeout)
	if err != nil {
		var closed *p8verify.Error
		if !errors.As(err, &closed) {
			return encodeFailure(output, p8verify.CodeGate)
		}
		return encodeFailure(output, closed.Code)
	}
	if err := json.NewEncoder(output).Encode(report); err != nil {
		return 2
	}
	return 0
}

func encodeFailure(output io.Writer, code p8verify.ErrorCode) int {
	if err := json.NewEncoder(output).Encode(map[string]any{
		"formatVersion": p8verify.FormatVersion,
		"command":       "p8verify",
		"status":        "FAIL",
		"code":          code,
	}); err != nil {
		return 2
	}
	return 1
}
