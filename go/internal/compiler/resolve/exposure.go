package resolve

import (
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func resolveModes(field ir.RawFieldDecl) ([]ir.FieldMode, []ir.Diagnostic) {
	modes, diagnostics := resolveBaseModes(field)
	if diagnostics != nil || !hasAttribute(field.GolemAttrs, "internal") {
		return modes, diagnostics
	}
	if hasAttribute(field.GolemAttrs, "pk") {
		return nil, []ir.Diagnostic{errorf("P1_GOLEM_TAG_INTERNAL_PRIMARY_KEY", field.Span, "field %s is a primary key and cannot be internal: the primary key identifies rows in every API surface", field.GoName)}
	}
	if hasAttribute(field.GolemAttrs, "hidden") {
		return nil, []ir.Diagnostic{errorf("P1_EXPOSURE_MODE_CONFLICT", field.Span, "field %s combines internal and hidden: hidden already removes the field from the Go API, internal keeps it", field.GoName)}
	}
	if len(modes) == 1 && modes[0] == ir.ModeVisible {
		return []ir.FieldMode{ir.ModeInternal}, nil
	}
	return append(modes, ir.ModeInternal), nil
}

func resolveBaseModes(field ir.RawFieldDecl) ([]ir.FieldMode, []ir.Diagnostic) {
	hidden := hasAttribute(field.GolemAttrs, "hidden")
	readOnly := hasAttribute(field.GolemAttrs, "readonly")
	writeOnly := hasAttribute(field.GolemAttrs, "writeonly")
	immutable := hasAttribute(field.GolemAttrs, "immutable")
	system := hasAttribute(field.GolemAttrs, "system")
	if system && readOnly {
		return nil, []ir.Diagnostic{errorf("P1_EXPOSURE_SYSTEM_READONLY", field.Span, "field %s combines system and readonly: readonly already means nobody writes, so naming system as the owner is meaningless", field.GoName)}
	}
	if system && hidden {
		return nil, []ir.Diagnostic{errorf("P1_EXPOSURE_MODE_CONFLICT", field.Span, "field %s has incompatible exposure modes", field.GoName)}
	}
	count := 0
	for _, enabled := range []bool{hidden, readOnly, writeOnly, immutable} {
		if enabled {
			count++
		}
	}
	if hidden && count > 1 || readOnly && count > 1 || immutable && count > 1 && !writeOnly || writeOnly && count > 2 {
		return nil, []ir.Diagnostic{errorf("P1_EXPOSURE_MODE_CONFLICT", field.Span, "field %s has incompatible exposure modes", field.GoName)}
	}
	if hidden {
		return []ir.FieldMode{ir.ModeHidden}, nil
	}
	if readOnly {
		return []ir.FieldMode{ir.ModeReadOnly}, nil
	}
	modes := []ir.FieldMode{}
	if writeOnly {
		modes = append(modes, ir.ModeWriteOnly)
	}
	if system {
		modes = append(modes, ir.ModeSystem)
	}
	if immutable {
		modes = append(modes, ir.ModeImmutable)
	}
	if len(modes) == 0 {
		return []ir.FieldMode{ir.ModeVisible}, nil
	}
	return modes, nil
}
