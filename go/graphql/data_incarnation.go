package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/eleven-am/golem/go/internal/dataincarnation"
)

func decodeDiscovery(extensions *requestExtensions) (bool, *Error) {
	if extensions == nil {
		return false, nil
	}
	handshake, refusal := parseLiveHandshake(extensions.block())
	if refusal != "" {
		return false, nil
	} // ordinary handshake admission reports it
	if len(handshake.Discover) == 0 {
		return false, nil
	}
	bad := publicError("BAD_REQUEST", "invalid data incarnation discovery")
	if len(handshake.DataIncarnation) != 0 {
		return true, &bad
	}
	var discovery struct {
		Nonce string `json:"nonce"`
	}
	decoder := json.NewDecoder(bytes.NewReader(handshake.Discover))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&discovery); err != nil {
		return true, &bad
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return true, &bad
	}
	if _, err := dataincarnation.Parse(discovery.Nonce); err != nil {
		return true, &bad
	}
	extensions.discoveryNonce = discovery.Nonce
	return true, nil
}

func (server *Server[P]) discoverDataIncarnation(ctx context.Context, principal P, extensions *requestExtensions) (response Response) {
	defer func() {
		if recover() != nil {
			response = Response{Errors: []Error{PresentError(ctx, fmt.Errorf("GraphQL discovery authentication panicked"), nil, server.config.ReportInternalError)}}
		}
	}()
	if !server.dataIncarnation.Valid() || extensions == nil || extensions.discoveryNonce == "" {
		return Response{Errors: []Error{publicError("BAD_REQUEST", "data incarnation discovery is unavailable")}}
	}
	handshake, refusal := parseLiveHandshake(extensions.block())
	if refusal == "" {
		refusal = server.checkStaticHandshake(handshake)
	}
	if refusal != "" {
		return Response{Errors: []Error{refusal.failure()}}
	}
	if err := server.executor.(PrincipalAuthenticationExecutor[P]).AuthenticatePrincipal(ctx, principal); err != nil {
		return Response{Errors: []Error{PresentError(ctx, err, nil, server.config.ReportInternalError)}}
	}
	return Response{Data: json.RawMessage("null"), Extensions: map[string]any{"golem": map[string]any{
		"protocol": protocolVersion, "contract": server.contractFingerprint.String(),
		"manifestVersion": server.clientManifestVersion, "capabilities": append([]string(nil), server.clientCapabilities...),
		"discover": map[string]any{"nonce": extensions.discoveryNonce}, "dataIncarnation": server.dataIncarnation.String(),
	}}}
}

// Own both map levels before merging commit evidence or protocol metadata.
func mergeGolemExtensions(response *Response, values map[string]any) {
	extensions := make(map[string]any, len(response.Extensions)+1)
	for key, value := range response.Extensions {
		extensions[key] = value
	}
	block := make(map[string]any)
	if old, ok := extensions["golem"].(map[string]any); ok {
		for key, value := range old {
			block[key] = value
		}
	}
	for key, value := range values {
		block[key] = value
	}
	extensions["golem"] = block
	response.Extensions = extensions
}

func (server *Server[P]) stampDataIncarnation(response Response) Response {
	if server.dataIncarnation.Valid() {
		mergeGolemExtensions(&response, map[string]any{"dataIncarnation": server.dataIncarnation.String()})
	}
	return response
}

func (server *Server[P]) operationAckPayload() json.RawMessage {
	if !server.dataIncarnation.Valid() {
		return liveAckPayload
	}
	value, _ := json.Marshal(map[string]any{"data": nil, "extensions": map[string]any{"golem": map[string]any{"live": true, "dataIncarnation": server.dataIncarnation.String()}}})
	return value
}

func (server *Server[P]) connectionAckPayload() json.RawMessage {
	if !server.dataIncarnation.Valid() {
		return nil
	}
	value, _ := json.Marshal(map[string]any{"golem": map[string]any{"dataIncarnation": server.dataIncarnation.String()}})
	return value
}
