package resolve

import (
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

func resolveModes(field ir.RawFieldDecl) ([]ir.FieldMode, []ir.Diagnostic) {
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
