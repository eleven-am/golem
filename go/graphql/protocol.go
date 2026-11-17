package graphql

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/dataincarnation"
	"github.com/eleven-am/golem/go/internal/invalidationcontract"
)

const protocolVersion = 1

const (
	codeProtocolMismatch        = "GOLEM_PROTOCOL_MISMATCH"
	codeContractMismatch        = "GOLEM_CONTRACT_MISMATCH"
	codeDataIncarnationMismatch = "GOLEM_DATA_INCARNATION_MISMATCH"
	codeDeadlineRequired        = "GOLEM_PRINCIPAL_DEADLINE_REQUIRED"
)

const (
	closeCredentialExpired = 4401
	closeHandshakeRefused  = 4426
)

var liveAckPayload = []byte(`{"data":null,"extensions":{"golem":{"live":true}}}`)

type handshakeRefusal string

const (
	refusedIncarnation handshakeRefusal = "incarnation"
	refusedProtocol    handshakeRefusal = "protocol"
	refusedContract    handshakeRefusal = "contract"
	refusedDeadline    handshakeRefusal = codeDeadlineRequired
	refusedManifest    handshakeRefusal = "manifest"
	refusedCapability  handshakeRefusal = "capability"
)

type requestExtensions struct {
	discoveryNonce string
	Golem          json.RawMessage `json:"golem"`
}

func (extensions *requestExtensions) block() json.RawMessage {
	if extensions == nil {
		return nil
	}
	return extensions.Golem
}

type liveHandshake struct {
	DataIncarnation json.RawMessage `json:"dataIncarnation"`
	Discover        json.RawMessage `json:"discover"`
	Protocol        *int            `json:"protocol"`
	Contract        *string         `json:"contract"`
	ManifestVersion json.RawMessage `json:"manifestVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
}

func parseLiveHandshake(block json.RawMessage) (liveHandshake, handshakeRefusal) {
	var handshake liveHandshake
	if len(block) == 0 {
		return handshake, refusedProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(block))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&handshake); err != nil {
		return handshake, refusedProtocol
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return handshake, refusedProtocol
	}
	return handshake, ""
}

func (server *Server[P]) checkHandshake(block json.RawMessage) handshakeRefusal {
	handshake, refusal := parseLiveHandshake(block)
	if refusal != "" {
		return refusal
	}
	if len(handshake.Discover) != 0 {
		return refusedProtocol
	}
	if refusal := server.checkStaticHandshake(handshake); refusal != "" {
		return refusal
	}
	if !server.dataIncarnation.Valid() {
		if len(handshake.DataIncarnation) != 0 {
			return refusedProtocol
		}
		return ""
	}
	var stamp string
	if err := json.Unmarshal(handshake.DataIncarnation, &stamp); err != nil {
		return refusedIncarnation
	}
	value, err := dataincarnation.Parse(stamp)
	if err != nil || value != server.dataIncarnation {
		return refusedIncarnation
	}
	return ""
}

func (server *Server[P]) checkStaticHandshake(handshake liveHandshake) handshakeRefusal {
	if handshake.Protocol == nil || *handshake.Protocol != protocolVersion {
		return refusedProtocol
	}
	version := compilerir.BaseClientManifestVersion
	if len(handshake.ManifestVersion) != 0 {
		version = 0
		if err := json.Unmarshal(handshake.ManifestVersion, &version); err != nil {
			return refusedManifest
		}
	}
	required := server.clientManifestVersion
	if required == 0 {
		required = compilerir.BaseClientManifestVersion
		if executor, ok := server.executor.(ClientManifestExecutor); ok {
			required = executor.ClientManifestVersion()
		}
	}
	if version != required {
		return refusedManifest
	}
	var capabilities []string
	if len(handshake.Capabilities) != 0 {
		if err := json.Unmarshal(handshake.Capabilities, &capabilities); err != nil || capabilities == nil || len(capabilities) > 32 {
			return refusedCapability
		}
	}
	seen := map[string]bool{}
	for _, capability := range capabilities {
		if len(capability) == 0 || len(capability) > 128 || seen[capability] {
			return refusedCapability
		}
		seen[capability] = true
	}
	for _, capability := range server.requiredCapabilities() {
		if (capability != invalidationcontract.Capability && capability != dataincarnation.Capability) || !seen[capability] {
			return refusedCapability
		}
	}
	if handshake.Contract == nil || *handshake.Contract != server.contractFingerprint.String() {
		return refusedContract
	}
	return ""
}

func (server *Server[P]) requiredCapabilities() []string {
	if server.clientManifestVersion != 0 {
		return server.clientCapabilities
	}
	if source, ok := server.executor.(ClientCapabilitiesExecutor); ok {
		return source.RequiredClientCapabilities()
	}
	return nil
}

func (server *Server[P]) handshakeFailure(extensions *requestExtensions, required bool) *Error {
	block := extensions.block()
	if block == nil && !required && len(server.requiredCapabilities()) == 0 {
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
	if refusal == refusedIncarnation {
		return publicError(codeDataIncarnationMismatch, "the request data incarnation is missing, invalid or stale; discover the current identity before new work")
	}
	if refusal == refusedCapability {
		return publicError(codeProtocolMismatch, "the client must advertise this contract's required client capability; regenerate with a matching Golem client")
	}
	if refusal == refusedManifest {
		return publicError(codeProtocolMismatch, "the client manifest format is unsupported by this server; regenerate with a matching Golem client")
	}
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
