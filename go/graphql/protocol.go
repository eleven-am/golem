package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/eleven-am/golem/go/golem"
)

const protocolVersion = 1

const (
	codeProtocolMismatch = "GOLEM_PROTOCOL_MISMATCH"
	codeContractMismatch = "GOLEM_CONTRACT_MISMATCH"
	codeDeadlineRequired = "GOLEM_PRINCIPAL_DEADLINE_REQUIRED"
)

const (
	closeCredentialExpired = 4401
	closeHandshakeRefused  = 4426
)

var liveAckPayload = []byte(`{"data":null,"extensions":{"golem":{"live":true}}}`)

type handshakeRefusal string

const (
	refusedProtocol handshakeRefusal = "protocol"
	refusedContract handshakeRefusal = "contract"
	refusedDeadline handshakeRefusal = codeDeadlineRequired
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

func (server *Server[P]) principalDeadline(ctx context.Context, openedAt time.Time) (time.Time, *Error) {
	if deadline, present := ctx.Deadline(); present {
		return deadline, nil
	}
	if !golem.HasNoCredentialExpiry(ctx) {
		failure := publicError(codeDeadlineRequired, "the principal has no credential expiry; set the credential expiry as the context deadline, or mark an API-key principal with golem.WithoutCredentialExpiry")
		return time.Time{}, &failure
	}
	if every := server.eventLimits.RevalidateEvery; every > 0 {
		return openedAt.Add(every), nil
	}
	failure := publicError(codeDeadlineRequired, "the principal is marked without credential expiry but EventLimits.RevalidateEvery is not set; set RevalidateEvery to bound it")
	return time.Time{}, &failure
}
