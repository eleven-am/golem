package completion

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCompletionCommandFailureIsVersionedClosedJSON(t *testing.T) {
	const canary = "/private/credential-canary/postgresql://secret"
	for _, test := range []struct {
		command   string
		arguments []string
	}{
		{command: "p8docs", arguments: []string{"-module", canary}},
		{command: "p8compat", arguments: []string{"-timeout", "invalid-canary"}},
		{command: "p8docs", arguments: []string{"unexpected-canary"}},
		{command: "p8failure", arguments: []string{"-module", canary}},
	} {
		var output bytes.Buffer
		if exit := Execute(context.Background(), test.command, test.arguments, &output); exit != 1 {
			t.Fatalf("%s exit=%d output=%s", test.command, exit, output.Bytes())
		}
		var evidence FailureEvidence
		decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&evidence); err != nil || evidence.FormatVersion != 1 || evidence.Status != "FAIL" || evidence.Code == "" {
			t.Fatalf("%s failure=%#v err=%v", test.command, evidence, err)
		}
		if strings.Contains(output.String(), canary) || strings.Contains(output.String(), "secret") {
			t.Fatalf("%s disclosed command input: %s", test.command, output.Bytes())
		}
	}
}
