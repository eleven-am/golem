package graphql

import (
	"bytes"
	"context"
	"errors"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/commitreport"
	"github.com/vektah/gqlparser/v2/ast"
)

var errResultBytes = errors.New("GraphQL result exceeds its byte budget")

func resultBytesError(maximum int) error {
	return golem.RuntimeLimitError("graphqlResult", golem.ModelID{}, golem.LimitResultBytes, maximum, "GraphQL result exceeds the serialized data size limit", nil)
}

// renderResponse owns presentation for direct execution and every transport.
// The budget covers the rendered data value; errors and protocol envelopes
// remain available to explain a refusal even when no data fits.
func (server *Server[P]) renderResponse(ctx context.Context, request Request, operation Operation, prepared Response) Response {
	if server.executable != nil && prepared.Data != nil {
		return server.executePrepared(ctx, request, operation.Document, operation.Definition, operation.Variables, prepared)
	}
	response := server.selectionOrdered(prepared, operation)
	if data, ok := response.Data.(selectionOrderedData); ok {
		var buffer bytes.Buffer
		if err := data.encodeIntoWithLimit(&buffer, server.limits.MaxResultBytes); err != nil {
			return server.resultFailure(ctx, operation.Definition.Operation, prepared, err)
		}
		data.encoded = executableData(buffer.Bytes())
		response.Data = data
	}
	return response
}

func (server *Server[P]) resultFailure(ctx context.Context, operation ast.Operation, response Response, cause error) Response {
	if errors.Is(cause, errResultBytes) {
		cause = resultBytesError(server.limits.MaxResultBytes)
	}
	var failure Error
	if state, reported := commitreport.From(ctx).State(); operation == ast.Mutation && reported && state == "PARTIAL" {
		failure = presentCommittedResult(ctx, "GraphQL mutation", cause, nil, server.config.ReportInternalError)
	} else {
		failure = PresentError(ctx, cause, nil, server.config.ReportInternalError)
	}
	response.Data = nil
	response.Errors = append(append([]Error(nil), response.Errors...), failure)
	return response
}
