package explain

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

type boundedWriter struct {
	limit int
	text  strings.Builder
	err   error
}

func (writer *boundedWriter) write(value string) {
	if writer.err != nil {
		return
	}
	if len(value) > writer.limit-writer.text.Len() {
		writer.err = unavailable()
		return
	}
	_, _ = writer.text.WriteString(value)
}

func (writer *boundedWriter) quoted(value string) {
	if writer.err != nil || !validString(value, true, false) {
		writer.err = unavailable()
		return
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		writer.err = unavailable()
		return
	}
	writer.write(string(encoded))
}

func (writer *boundedWriter) bytes() ([]byte, error) {
	if writer.err != nil {
		return nil, writer.err
	}
	return []byte(writer.text.String()), nil
}

// MarshalJSON encodes the one immutable report into the closed version-1
// machine document. It writes collection members incrementally into a hard
// bounded buffer and never returns a prefix on failure.
func MarshalJSON(report Report) ([]byte, error) {
	if report.formatVersion != reportFormatVersion || report.mode != ModeProspective && report.mode != ModeReviewed || report.status != StatusNoChanges && report.status != StatusReviewRequired {
		return nil, unavailable()
	}
	if err := validateReportStrings(report); err != nil {
		return nil, err
	}
	writer := &boundedWriter{limit: maxEncodedBytes}
	writer.write(`{"formatVersion":1,"mode":`)
	writer.quoted(string(report.mode))
	writer.write(`,"status":`)
	writer.quoted(string(report.status))
	writer.write(`,"providers":[`)
	for index, provider := range report.providers {
		if index != 0 {
			writer.write(",")
		}
		writeProviderJSON(writer, provider)
	}
	writer.write(`],"warnings":`)
	writeWarningsJSON(writer, report.warnings)
	writer.write(`,"guarantees":{"appliesChanges":false,"usesReviewedTypedPlan":true,"zeroDowntime":false,"durationEstimated":false}}`)
	return writer.bytes()
}

func writeProviderJSON(writer *boundedWriter, provider Provider) {
	writer.write(`{"provider":`)
	writer.quoted(string(provider.provider))
	writer.write(`,"initial":`)
	writer.write(strconv.FormatBool(provider.initial))
	writer.write(`,"beforePhysicalFingerprint":`)
	writer.quoted(string(provider.beforeFingerprint))
	writer.write(`,"afterPhysicalFingerprint":`)
	writer.quoted(string(provider.afterFingerprint))
	writer.write(`,"phases":[`)
	for index, phase := range provider.phases {
		if index != 0 {
			writer.write(",")
		}
		writePhaseJSON(writer, phase)
	}
	writer.write(`],"artifacts":[`)
	for index, artifact := range provider.artifacts {
		if index != 0 {
			writer.write(",")
		}
		writer.write(`{"path":`)
		writer.quoted(artifact.path)
		writer.write(`,"sha256":`)
		writer.quoted(string(artifact.sha256))
		writer.write("}")
	}
	writer.write(`],"warnings":`)
	writeWarningsJSON(writer, provider.warnings)
	writer.write(`,"operationCountsByRisk":[`)
	for index, count := range provider.riskCounts {
		if index != 0 {
			writer.write(",")
		}
		writer.write(`{"risk":`)
		writer.quoted(string(count.risk))
		writer.write(`,"count":`)
		writer.write(strconv.FormatUint(uint64(count.count), 10))
		writer.write("}")
	}
	writer.write("]}")
}

func writePhaseJSON(writer *boundedWriter, phase Phase) {
	writer.write(`{"ordinal":`)
	writer.write(strconv.FormatUint(uint64(phase.ordinal), 10))
	writer.write(`,"transactionMode":`)
	writer.quoted(string(phase.mode))
	writer.write(`,"beforeFingerprint":`)
	writer.quoted(string(phase.beforeFingerprint))
	writer.write(`,"afterFingerprint":`)
	writer.quoted(string(phase.afterFingerprint))
	writer.write(`,"operations":[`)
	for index, operation := range phase.operations {
		if index != 0 {
			writer.write(",")
		}
		writeOperationJSON(writer, operation)
	}
	writer.write(`],"warnings":`)
	writeWarningsJSON(writer, phase.warnings)
	writer.write("}")
}

func writeOperationJSON(writer *boundedWriter, operation Operation) {
	writer.write(`{"id":`)
	writer.quoted(string(operation.id))
	writer.write(`,"kind":`)
	writer.quoted(string(operation.kind))
	writer.write(`,"stage":`)
	writer.write(strconv.FormatUint(uint64(operation.stage), 10))
	writer.write(`,"identity":{"modelId":`)
	writer.quoted(string(operation.identity.modelID))
	writer.write(`,"fieldId":`)
	writer.quoted(string(operation.identity.fieldID))
	writer.write(`,"indexId":`)
	writer.quoted(string(operation.identity.indexID))
	writer.write(`,"relationId":`)
	writer.quoted(string(operation.identity.relationID))
	writer.write(`,"keyId":`)
	writer.quoted(string(operation.identity.keyID))
	writer.write(`,"checkId":`)
	writer.quoted(string(operation.identity.checkID))
	writer.write(`,"extensionId":`)
	writer.quoted(string(operation.identity.extensionID))
	writer.write("}")
	if operation.display != "" {
		writer.write(`,"display":`)
		writer.quoted(operation.display)
	}
	writer.write(`,"risk":`)
	writer.quoted(string(operation.risk))
	writer.write(`,"effect":`)
	writer.quoted(string(operation.effect))
	writer.write(`,"transactionMode":`)
	writer.quoted(string(operation.mode))
	writer.write(`,"beforeDigest":`)
	writer.quoted(string(operation.before))
	writer.write(`,"afterDigest":`)
	writer.quoted(string(operation.after))
	writer.write(`,"dependencies":[`)
	for index, dependency := range operation.dependencies {
		if index != 0 {
			writer.write(",")
		}
		writer.quoted(string(dependency))
	}
	writer.write(`],"capabilities":[`)
	for index, capability := range operation.capabilities {
		if index != 0 {
			writer.write(",")
		}
		writer.quoted(string(capability))
	}
	writer.write(`],"approval":{"required":`)
	writer.write(strconv.FormatBool(operation.approvalRequired))
	writer.write(`,"present":`)
	writer.write(strconv.FormatBool(operation.approvalPresent))
	writer.write("}")
	if operation.manual != nil {
		writer.write(`,"reviewedCompanion":{"path":`)
		writer.quoted(operation.manual.path)
		writer.write(`,"sha256":`)
		writer.quoted(string(operation.manual.sha256))
		writer.write(`,"postconditionDigest":`)
		writer.quoted(string(operation.manual.postcondition))
		writer.write("}")
	}
	writer.write(`,"warnings":`)
	writeWarningsJSON(writer, operation.warnings)
	writer.write("}")
}

func writeWarningsJSON(writer *boundedWriter, warnings []Warning) {
	writer.write("[")
	for index, warning := range warnings {
		if index != 0 {
			writer.write(",")
		}
		writer.quoted(string(warning))
	}
	writer.write("]")
}

// MarshalText renders the same immutable report as deterministic operator
// text. Every phrase is closed; no SQL or physical/provider diagnostic input
// exists in the report model.
func MarshalText(report Report) ([]byte, error) {
	if report.formatVersion != reportFormatVersion || report.mode != ModeProspective && report.mode != ModeReviewed || report.status != StatusNoChanges && report.status != StatusReviewRequired {
		return nil, unavailable()
	}
	if err := validateReportStrings(report); err != nil {
		return nil, err
	}
	writer := &boundedWriter{limit: maxEncodedBytes}
	writer.write("Migration plan: " + string(report.mode) + "\n")
	if report.status == StatusNoChanges {
		writer.write("Status: NO CHANGES\n")
		writer.write("No files or databases were modified.\n")
	} else {
		writer.write("Status: REVIEW REQUIRED\n")
	}
	for _, provider := range report.providers {
		writer.write("\n" + providerLabel(provider.provider) + " — ")
		if provider.initial {
			writer.write("initial")
		} else {
			writer.write("incremental")
		}
		writer.write(" — " + strconv.Itoa(len(provider.phases)) + " phases\n")
		writer.write("  physical fingerprint: " + string(provider.beforeFingerprint) + " -> " + string(provider.afterFingerprint) + "\n")
		for _, artifact := range provider.artifacts {
			writer.write("  reviewed artifact: " + artifact.path + " sha256=" + string(artifact.sha256) + "\n")
		}
		writeWarningsText(writer, "  ", provider.warnings)
		for _, phase := range provider.phases {
			writer.write("  Phase " + strconv.FormatUint(uint64(phase.ordinal), 10) + " — " + string(phase.mode) + "\n")
			writer.write("    fingerprint: " + string(phase.beforeFingerprint) + " -> " + string(phase.afterFingerprint) + "\n")
			writeWarningsText(writer, "    ", phase.warnings)
			for _, operation := range phase.operations {
				writer.write("    [" + string(operation.risk) + "] ")
				if operation.display != "" {
					writer.write(operation.display)
				} else {
					writer.write("operation " + string(operation.id))
				}
				writer.write("\n")
				writer.write("      operation: " + string(operation.id) + "\n")
				writer.write("      kind: " + string(operation.kind) + "\n")
				writer.write("      stage: " + strconv.FormatUint(uint64(operation.stage), 10) + "\n")
				writeIdentityText(writer, operation.identity)
				writer.write("      effect: " + effectText(operation.effect) + "\n")
				writer.write("      object digest: " + string(operation.before) + " -> " + string(operation.after) + "\n")
				if len(operation.dependencies) != 0 {
					writer.write("      depends on: ")
					for index, dependency := range operation.dependencies {
						if index != 0 {
							writer.write(", ")
						}
						writer.write(string(dependency))
					}
					writer.write("\n")
				}
				if len(operation.capabilities) != 0 {
					writer.write("      capabilities: ")
					for index, capability := range operation.capabilities {
						if index != 0 {
							writer.write(", ")
						}
						writer.write(string(capability))
					}
					writer.write("\n")
				}
				if operation.approvalRequired {
					if operation.approvalPresent {
						writer.write("      approval: present\n")
					} else {
						writer.write("      approval: required\n")
					}
				} else {
					writer.write("      approval: not required\n")
				}
				if operation.manual != nil {
					writer.write("      reviewed backfill: " + operation.manual.path + "\n")
					writer.write("      reviewed backfill checksum: " + string(operation.manual.sha256) + "\n")
					writer.write("      postcondition: " + string(operation.manual.postcondition) + "\n")
				}
				writeWarningsText(writer, "      ", operation.warnings)
			}
		}
	}
	writer.write("\nGuarantees: read-only; uses reviewed typed plan; no duration estimate; zero downtime is not guaranteed.\n")
	return writer.bytes()
}

func writeIdentityText(writer *boundedWriter, identity Identity) {
	for _, value := range []struct {
		label string
		id    string
	}{
		{"model", string(identity.modelID)}, {"field", string(identity.fieldID)},
		{"index", string(identity.indexID)}, {"relation", string(identity.relationID)},
		{"key", string(identity.keyID)}, {"check", string(identity.checkID)},
		{"extension", string(identity.extensionID)},
	} {
		if value.id != "" {
			writer.write("      " + value.label + " identity: " + value.id + "\n")
		}
	}
}

func writeWarningsText(writer *boundedWriter, indent string, warnings []Warning) {
	if len(warnings) == 0 {
		return
	}
	writer.write(indent + "warnings: ")
	for index, warning := range warnings {
		if index != 0 {
			writer.write(", ")
		}
		writer.write(string(warning))
	}
	writer.write("\n")
}

func providerLabel(provider ir.Provider) string {
	switch string(provider) {
	case "sqlite":
		return "SQLite"
	case "postgresql":
		return "PostgreSQL"
	default:
		return "Unknown provider"
	}
}

func effectText(effect Effect) string {
	switch effect {
	case EffectValuePreserving:
		return "existing values preserved"
	case EffectValueRewritten:
		return "existing values rewritten"
	case EffectValueDeleted:
		return "existing values deleted"
	case EffectSchemaOnly:
		return "schema only"
	case EffectManualDataTransform:
		return "manual data transform"
	default:
		return "unknown; manual review required"
	}
}
