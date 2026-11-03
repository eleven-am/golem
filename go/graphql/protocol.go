package graphql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

const protocolVersion = 1

const (
	codeProtocolMismatch = "GOLEM_PROTOCOL_MISMATCH"
	codeContractMismatch = "GOLEM_CONTRACT_MISMATCH"
)

var liveAckPayload = []byte(`{"data":null,"extensions":{"golem":{"live":true}}}`)

type handshakeRefusal string

const (
	refusedProtocol handshakeRefusal = "protocol"
	refusedContract handshakeRefusal = "contract"
)

type requestExtensions struct {
	Golem json.RawMessage `json:"golem"`
}

func (extensions *requestExtensions) block() json.RawMessage {
	if extensions == nil {
		return nil
	}
	return extensions.Golem
}

type liveHandshake struct {
	Protocol *int    `json:"protocol"`
	Contract *string `json:"contract"`
}

func (server *Server[P]) checkHandshake(block json.RawMessage) handshakeRefusal {
	if len(block) == 0 {
		return refusedProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(block))
	decoder.DisallowUnknownFields()
	var handshake liveHandshake
	if err := decoder.Decode(&handshake); err != nil {
		return refusedProtocol
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return refusedProtocol
	}
	if handshake.Protocol == nil || *handshake.Protocol != protocolVersion {
		return refusedProtocol
	}
	if handshake.Contract == nil || *handshake.Contract != server.contractFingerprint.String() {
		return refusedContract
	}
	return ""
}

func (server *Server[P]) handshakeFailure(extensions *requestExtensions, required bool) *Error {
	block := extensions.block()
	if block == nil && !required {
		return nil
	}
	refusal := server.checkHandshake(block)
	if refusal == "" {
		return nil
	}
	failure := refusal.failure()
	return &failure
}

func (refusal handshakeRefusal) closeReason() string { return "golem:" + string(refusal) }

func (refusal handshakeRefusal) failure() Error {
	if refusal == refusedContract {
		return publicError(codeContractMismatch, "the client was generated from a different contract than this server; regenerate the client")
	}
	return publicError(codeProtocolMismatch, fmt.Sprintf("extensions.golem must declare protocol %d and the contract fingerprint", protocolVersion))
}

func splitInitPayload(payload json.RawMessage) (block, rest json.RawMessage) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return nil, payload
	}
	block, present := fields["golem"]
	if !present {
		return nil, payload
	}
	delete(fields, "golem")
	rest, err := json.Marshal(fields)
	if err != nil {
		return nil, payload
	}
	return block, rest
}
