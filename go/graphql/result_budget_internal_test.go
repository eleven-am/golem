package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/internal/commitreport"
)

func TestCanonicalResultByteBudgetMatchesCanonicalStringEscaping(t *testing.T) {
	random := rand.New(rand.NewSource(1))
	rawValues := [][]byte{
		[]byte(` { "value" : "\u0041\ud83d\ude00" } `),
		[]byte(`{"value":"<>&é"}`),
		[]byte(`{"value":"\ud800"}`),
		[]byte(`{"value":""}`),
	}
	for i := 0; i < 64; i++ {
		value := make([]byte, 128)
		if _, err := random.Read(value); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(map[string]any{"value": string(value)})
		if err != nil {
			t.Fatal(err)
		}
		rawValues = append(rawValues, raw)
	}
	for _, raw := range rawValues {
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(decoded)
		if err != nil {
			t.Fatal(err)
		}
		got, err := canonicalExecutableDataWithLimit(raw, len(want))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("exact canonical limit failed: raw=%q got=%q want=%q err=%v", raw, got, want, err)
		}
		if _, err := canonicalExecutableDataWithLimit(raw, len(want)-1); !errors.Is(err, errResultBytes) {
			t.Fatalf("escaped bytes bypassed the limit: raw=%q err=%v", raw, err)
		}
	}
}

func TestNativeResultByteBudgetRefusesLongStringsBeforeCopying(t *testing.T) {
	var output bytes.Buffer
	writer := selectionWriter{output: &output, limit: 16}
	if err := writer.scalar(strings.Repeat("x", 4096)); !errors.Is(err, errResultBytes) || output.Len() != 0 {
		t.Fatalf("oversized string copied into the response buffer: bytes=%d err=%v", output.Len(), err)
	}
}

type resultCommitExecutor struct{ state string }

func (executor resultCommitExecutor) ListBounds() ListBounds { return NewListBounds(nil) }

func (executor resultCommitExecutor) Execute(ctx context.Context, _ int, _ Operation) Response {
	switch executor.state {
	case "PARTIAL":
		commitreport.From(ctx).Committed()
	case "UNKNOWN":
		commitreport.From(ctx).Unknown()
	}
	return Response{Data: map[string]any{"write": strings.Repeat("x", 64)}}
}

func TestResultByteBudgetPreservesCommitCertainty(t *testing.T) {
	for _, state := range []string{"", "PARTIAL", "UNKNOWN"} {
		t.Run("state="+state, func(t *testing.T) {
			reported := 0
			server, err := NewServer(`type Query{viewer:Int!} type Mutation{write:String!}`, Config[int]{
				Limits: Limits{MaxResultBytes: 8}, PrincipalFromContext: func(context.Context) (int, bool) { return 1, true },
				ReportInternalError: func(context.Context, error) { reported++ },
			}, resultCommitExecutor{state: state})
			if err != nil {
				t.Fatal(err)
			}
			response := server.Execute(context.Background(), 1, Request{Query: `mutation{write}`})
			if response.Data != nil || len(response.Errors) != 1 || response.Errors[0].Extensions["limit"] != "result_bytes" {
				t.Fatalf("oversized mutation result escaped: %#v", response)
			}
			failure := response.Errors[0]
			if state == "PARTIAL" {
				if failure.Extensions["code"] != "COMMITTED_RESULT_UNAVAILABLE" || failure.Extensions["committed"] != true || reported != 1 {
					t.Fatalf("confirmed commit was lost: %#v reports=%d", response, reported)
				}
			} else if failure.Extensions["code"] != "LIMIT_EXCEEDED" || failure.Extensions["committed"] != nil || reported != 0 {
				t.Fatalf("unconfirmed commit was presented as confirmed: %#v reports=%d", response, reported)
			}
			if state != "" && response.Extensions["golem"].(map[string]any)["commit"] != state {
				t.Fatalf("commit certainty changed: %#v", response)
			}
		})
	}
}
