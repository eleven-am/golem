package physical

import "github.com/eleven-am/golem/go/internal/compiler/ir"

// DataIncarnationFormatVersion is selected only by opted schemas. The ordinary
// authoring profile and all retained V3 history remain unchanged.
const DataIncarnationFormatVersion uint32 = 4
const SystemDataIncarnation SystemObjectKind = "data_incarnation"
const DataIncarnationObjectIDV1 ir.ObjectID = "28b1232170c17846c928756ef61c41ea"

func DataIncarnationSystemObjectV1() SystemObject {
	return SystemObject{ID: DataIncarnationObjectIDV1, Kind: SystemDataIncarnation, Version: 1, Name: "_golem_data_incarnation"}
}
func IsDataIncarnationSystemObjectV1(object SystemObject) bool {
	return object.ID == DataIncarnationObjectIDV1 && object.Kind == SystemDataIncarnation && object.Version == 1 && object.Name == "_golem_data_incarnation" && len(object.Attributes) == 0 && len(object.RequiredCapabilities) == 0
}
func HasDataIncarnation(system SystemSchema) bool {
	for _, object := range system.Objects {
		if IsDataIncarnationSystemObjectV1(object) {
			return true
		}
	}
	return false
}

var dataIncarnationV4StructFields = map[string][]string{
	"Version":               {"Major", "Minor", "Patch"},
	"DriverIdentity":        {"Module", "Adapter"},
	"CapabilityFact":        {"ID", "Version", "Verification"},
	"ProviderManifest":      {"Provider", "Driver", "MinimumVersion", "Capabilities"},
	"Namespace":             {"Name"},
	"ObjectRef":             {"Kind", "ModelID", "FieldID", "ObjectID"},
	"CapabilityRequirement": {"Capability", "Owner"},
	"SemanticSymbol":        {"Identity", "Kind", "Version", "Provider"},
	"StorageType":           {"Kind", "Precision", "Scale", "Length", "Symbol"},
	"Expression":            {"Kind", "Type", "Nullable", "Symbol", "Column", "Literal", "Operands"},
	"TypedLiteralIR":        {"Kind", "Canonical"},
	"PhysicalDefault":       {"Kind", "Literal", "Expression"},
	"GeneratedExpression":   {"Kind", "Expression"},
	"PhysicalColumn":        {"ID", "Name", "Ordinal", "Storage", "Nullable", "Default", "Generated", "Collation", "RequiredCapabilities"},
	"PhysicalKey":           {"ID", "Name", "Columns", "RequiredCapabilities"},
	"PhysicalForeignKey":    {"ID", "Name", "Columns", "ReferencedTable", "ReferencedColumns", "OnUpdate", "OnDelete", "Deferrable", "RequiredCapabilities"},
	"PhysicalCheck":         {"ID", "Name", "Expression", "RequiredCapabilities"},
	"IndexKey":              {"Column", "Expression", "Direction", "Nulls", "Collation", "OpClass"},
	"PhysicalIndex":         {"ID", "Name", "Unique", "Method", "Keys", "Include", "Predicate", "CreationMode", "RequiredCapabilities"},
	"PhysicalTable":         {"ID", "Name", "OptimisticConcurrency", "Columns", "PrimaryKey", "Uniques", "ForeignKeys", "Checks", "Indexes", "RequiredCapabilities"},
	"SemanticValue":         {"Kind", "Bool", "Integer", "String", "List"},
	"Attribute":             {"Name", "Value"},
	"Extension":             {"ID", "Provider", "Kind", "Version", "Owner", "Attributes", "RequiredCapabilities"},
	"SystemObject":          {"ID", "Kind", "Version", "Name", "Attributes", "RequiredCapabilities"},
	"SystemSchema":          {"Version", "Namespace", "Objects"},
	"UnmanagedObject":       {"Kind", "Name"},
	"PhysicalSchema":        {"Version", "CanonicalVersion", "Provider", "Namespace", "Tables", "Extensions", "System", "Unmanaged"},
}
