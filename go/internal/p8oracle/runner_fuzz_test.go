package p8oracle

import (
	"testing"
	"time"
)

func TestExternalFuzzBoundsExecutionsAndFailsLoudlyOnHang(t *testing.T) {
	arguments := externalFuzzArguments("FuzzTarget", 110)
	flags := map[string]string{}
	for index := 0; index+1 < len(arguments); index++ {
		flags[arguments[index]] = arguments[index+1]
	}
	if got := flags["-fuzz"]; got != "^FuzzTarget$" {
		t.Fatalf("fuzz target = %q", got)
	}
	if got := flags["-fuzztime"]; got != "110x" {
		t.Fatalf("fuzztime = %q, want an execution count so the fuzz coordinator holds no wall-clock deadline", got)
	}
	timeout, err := time.ParseDuration(flags["-timeout"])
	if err != nil {
		t.Fatalf("child go test needs an explicit -timeout because -fuzz disables the default: %v", err)
	}
	if want := externalFuzzSetupBudget + 110*externalFuzzExecutionBudget; timeout != want {
		t.Fatalf("timeout = %s, want %s", timeout, want)
	}
	if flags["-parallel"] != "1" || arguments[len(arguments)-1] != "." {
		t.Fatalf("unexpected arguments %q", arguments)
	}
}
