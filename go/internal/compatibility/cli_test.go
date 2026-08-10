package compatibility

import "testing"

func TestCLIJSONSemanticDiffProtectsMachineOutput(t *testing.T) {
	base := CLIInventory{FormatVersion: CLIInventoryFormatVersion, Documents: []CLIDocument{{
		Name: "version", FormatVersion: 1,
		Fields: []CLIField{{Name: "formatVersion", Type: "uint16", Required: true}, {Name: "version", Type: "string", Required: true}},
	}}}
	additive := base
	additive.Documents = []CLIDocument{{Name: "version", FormatVersion: 1, Fields: []CLIField{
		{Name: "commit", Type: "string", Required: true},
		{Name: "formatVersion", Type: "uint16", Required: true},
		{Name: "version", Type: "string", Required: true},
	}}}
	changed := base
	changed.Documents = []CLIDocument{{Name: "version", FormatVersion: 1, Fields: []CLIField{
		{Name: "formatVersion", Type: "uint32", Required: true}, {Name: "version", Type: "string", Required: true},
	}}}
	bumped := base
	bumped.Documents = []CLIDocument{{Name: "version", FormatVersion: 2, Fields: base.Documents[0].Fields}}
	if got := CompareCLI(base, base); got != LayerUnchanged {
		t.Fatalf("unchanged CLI classification = %q", got)
	}
	if got := CompareCLI(base, additive); got != LayerAdditive {
		t.Fatalf("additive CLI classification = %q", got)
	}
	if got := CompareCLI(base, changed); got != LayerBreaking {
		t.Fatalf("changed CLI type classification = %q", got)
	}
	if got := CompareCLI(base, bumped); got != LayerBreaking {
		t.Fatalf("changed CLI document version classification = %q", got)
	}
}
