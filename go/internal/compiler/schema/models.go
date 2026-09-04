package schema

import (
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/compiler/load"
	"github.com/eleven-am/golem/go/internal/compiler/tags"
)

var (
	blankAllowed = tags.Allowed{
		"model": true, "id": true, "table": true, "graphql": true, "renameFrom": true,
		"primary": true, "unique": true, "index": true,
	}
	fieldAllowed = tags.Allowed{
		"id": true, "type": true, "pk": true, "unique": true, "default": true,
		"updated": true, "readonly": true, "writeonly": true, "immutable": true, "system": true,
		"hidden": true, "graphql": true, "renameFrom": true,
		"relation": true, "name": true, "fields": true, "references": true,
		"through": true, "source": true, "target": true,
	}
)

func (c *compiler) extractRegisteredModels(root ir.RawSchemaDecl) {
	modelPackages := map[string]*load.Package{}
	for _, ref := range root.Models {
		pkg := c.loadPackage(ref.PackagePath)
		if pkg == nil {
			continue
		}
		model, declaration := c.extractModel(pkg, ref.GoName)
		if model != nil {
			c.models = append(c.models, *model)
			c.extractMethods(pkg, ref.GoName)
			modelPackages[pkg.ImportPath] = pkg
		}
		if declaration == nil {
			c.diagnostics = append(c.diagnostics, Diagnostic{Code: "P1_MODEL_MISSING", Severity: SeverityError, Message: fmt.Sprintf("registered model %s.%s is not declared", ref.PackagePath, ref.GoName), Span: ref.Span})
		}
	}
	for _, pkg := range modelPackages {
		c.extractEnums(pkg)
	}
	sort.Slice(c.models, func(i, j int) bool {
		if c.models[i].PackagePath != c.models[j].PackagePath {
			return c.models[i].PackagePath < c.models[j].PackagePath
		}
		return c.models[i].GoName < c.models[j].GoName
	})
	sort.Slice(c.methods, func(i, j int) bool {
		left, right := c.methods[i], c.methods[j]
		if left.ReceiverPackage != right.ReceiverPackage {
			return left.ReceiverPackage < right.ReceiverPackage
		}
		if left.ReceiverGoName != right.ReceiverGoName {
			return left.ReceiverGoName < right.ReceiverGoName
		}
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		if left.Span.RelativeFile != right.Span.RelativeFile {
			return left.Span.RelativeFile < right.Span.RelativeFile
		}
		return left.Span.StartLine < right.Span.StartLine
	})
	sort.Slice(c.enums, func(i, j int) bool {
		if c.enums[i].PackagePath != c.enums[j].PackagePath {
			return c.enums[i].PackagePath < c.enums[j].PackagePath
		}
		return c.enums[i].GoName < c.enums[j].GoName
	})
}

func (c *compiler) extractModel(pkg *load.Package, name string) (*ir.RawModelDecl, *ast.TypeSpec) {
	var found *ast.TypeSpec
	var foundFile *ast.File
	for _, file := range pkg.Files {
		for _, declaration := range file.AST.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, spec := range general.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != name {
					continue
				}
				if found != nil {
					c.error(pkg, "P1_MODEL_DUPLICATE_TYPE", fmt.Sprintf("model type %s is declared more than once", name), typeSpec)
					continue
				}
				found = typeSpec
				foundFile = file.AST
			}
		}
	}
	if found == nil {
		return nil, nil
	}
	if !ast.IsExported(name) || found.TypeParams != nil {
		c.error(pkg, "P1_MODEL_TYPE", "registered model must be an exported non-generic named struct", found)
		return nil, found
	}
	structure, ok := found.Type.(*ast.StructType)
	if !ok {
		c.error(pkg, "P1_MODEL_TYPE", "registered model must be a named struct", found)
		return nil, found
	}

	model := &ir.RawModelDecl{PackagePath: pkg.ImportPath, GoName: name, Span: sourceSpan(pkg, found.Pos(), found.End())}
	aliases := importAliases(foundFile)
	markerCount := 0
	columns := map[string]token.Pos{}
	for _, field := range structure.Fields.List {
		if len(field.Names) != 1 {
			c.error(pkg, "P1_MODEL_FIELD_SHAPE", "model fields must declare exactly one name", field)
			continue
		}
		fieldName := field.Names[0].Name
		blank := fieldName == "_"
		dbValue, hasDB, golemValue, hasGolem, validTag := c.decodeStructTags(pkg, field)
		if !validTag {
			continue
		}
		if blank {
			if hasDB {
				c.error(pkg, "P1_MODEL_BLANK_DB", "blank model declarations cannot have a db tag", field)
			}
			if !hasGolem {
				c.error(pkg, "P1_MODEL_BLANK_TAG", "blank model field must declare a model marker or common directive", field)
				continue
			}
			parsed, errs := tags.ParseGolem(golemValue, blankAllowed)
			c.appendTagErrors(pkg, field, errs)
			if len(errs) != 0 {
				continue
			}
			if hasAttribute(parsed, "model") {
				markerCount++
				c.validateModelMarker(pkg, field, parsed)
				model.Marker = rawAttributes(pkg, field, parsed)
				continue
			}
			for _, attr := range parsed {
				directive, err := tags.ParseDirective(attr)
				if err != nil {
					c.appendTagErrors(pkg, field, []*tags.Error{err})
					continue
				}
				model.Directives = append(model.Directives, ir.RawDirectiveDecl{
					Kind: directive.Kind, Name: directive.Name, Components: directive.Components,
					Attributes: rawAttributes(pkg, field, []tags.Attribute{attr}), Span: sourceSpan(pkg, field.Pos(), field.End()),
				})
			}
			continue
		}

		if !ast.IsExported(fieldName) {
			c.error(pkg, "P1_MODEL_FIELD_UNEXPORTED", fmt.Sprintf("model field %s must be exported", fieldName), field)
			continue
		}
		var parsed []tags.Attribute
		if hasGolem {
			var errs []*tags.Error
			parsed, errs = tags.ParseGolem(golemValue, fieldAllowed)
			c.appendTagErrors(pkg, field, errs)
			if len(errs) != 0 {
				continue
			}
			c.validateFieldAttributes(pkg, field, parsed)
		}
		relation := hasAttribute(parsed, "relation")
		if !hasDB {
			c.error(pkg, "P1_MODEL_FIELD_DB_MISSING", fmt.Sprintf("model field %s requires a db tag", fieldName), field)
			continue
		}
		parsedDB, dbErr := tags.ParseDB(dbValue)
		if dbErr != nil {
			c.appendTagErrors(pkg, field, []*tags.Error{dbErr})
			continue
		}
		if relation != parsedDB.Ignored {
			if relation {
				c.error(pkg, "P1_RELATION_DB_TAG", "relation fields must use db:\"-\"", field)
			} else {
				c.error(pkg, "P1_COLUMN_DB_TAG", "only relation fields may use db:\"-\"", field)
			}
			continue
		}
		if !parsedDB.Ignored {
			if previous, duplicate := columns[parsedDB.Name]; duplicate {
				_ = previous
				c.error(pkg, "P1_COLUMN_DUPLICATE", fmt.Sprintf("column %s is declared more than once", parsedDB.Name), field)
				continue
			}
			columns[parsedDB.Name] = field.Pos()
		}
		copyDB := dbValue
		goType, validType := c.rawGoType(pkg, aliases, field.Type)
		if !validType {
			c.error(pkg, "P1_GO_TYPE_SHAPE", fmt.Sprintf("field %s uses an unsupported Go type shape", fieldName), field.Type)
			continue
		}
		model.Fields = append(model.Fields, ir.RawFieldDecl{
			GoName: fieldName, TypeSyntax: nodeText(pkg, field.Type), GoType: goType, DBTag: &copyDB,
			GolemAttrs: rawAttributes(pkg, field, parsed), IsBlank: false, Span: sourceSpan(pkg, field.Pos(), field.End()),
		})
	}
	if markerCount == 0 {
		c.error(pkg, "P1_MODEL_MARKER_MISSING", fmt.Sprintf("model %s requires one blank golem model marker", name), found)
	} else if markerCount > 1 {
		c.error(pkg, "P1_MODEL_MARKER_DUPLICATE", fmt.Sprintf("model %s has multiple model markers", name), found)
	}
	return model, found
}

func (c *compiler) rawGoType(pkg *load.Package, aliases map[string]string, expression ast.Expr) (ir.RawGoTypeRef, bool) {
	ref := ir.RawGoTypeRef{Span: sourceSpan(pkg, expression.Pos(), expression.End())}
	switch value := expression.(type) {
	case *ast.Ident:
		ref.GoName = value.Name
		if isBuiltinTypeName(value.Name) {
			ref.Kind = ir.RawGoTypeBuiltin
		} else {
			ref.Kind = ir.RawGoTypeNamed
			ref.PackagePath = pkg.ImportPath
		}
		return ref, true
	case *ast.SelectorExpr:
		qualifier, ok := value.X.(*ast.Ident)
		if !ok || aliases[qualifier.Name] == "" {
			return ir.RawGoTypeRef{}, false
		}
		ref.Kind = ir.RawGoTypeNamed
		ref.PackagePath = aliases[qualifier.Name]
		ref.GoName = value.Sel.Name
		return ref, true
	case *ast.StarExpr:
		argument, ok := c.rawGoType(pkg, aliases, value.X)
		if !ok {
			return ir.RawGoTypeRef{}, false
		}
		ref.Kind = ir.RawGoTypePointer
		ref.Args = []ir.RawGoTypeRef{argument}
		return ref, true
	case *ast.ArrayType:
		if value.Len != nil {
			return ir.RawGoTypeRef{}, false
		}
		argument, ok := c.rawGoType(pkg, aliases, value.Elt)
		if !ok {
			return ir.RawGoTypeRef{}, false
		}
		ref.Kind = ir.RawGoTypeSlice
		ref.Args = []ir.RawGoTypeRef{argument}
		return ref, true
	case *ast.IndexExpr:
		base, ok := c.rawGoType(pkg, aliases, value.X)
		if !ok || base.Kind != ir.RawGoTypeNamed {
			return ir.RawGoTypeRef{}, false
		}
		argument, ok := c.rawGoType(pkg, aliases, value.Index)
		if !ok {
			return ir.RawGoTypeRef{}, false
		}
		base.Kind = ir.RawGoTypeInstantiation
		base.Args = []ir.RawGoTypeRef{argument}
		base.Span = ref.Span
		return base, true
	case *ast.IndexListExpr:
		base, ok := c.rawGoType(pkg, aliases, value.X)
		if !ok || base.Kind != ir.RawGoTypeNamed || len(value.Indices) == 0 {
			return ir.RawGoTypeRef{}, false
		}
		base.Kind = ir.RawGoTypeInstantiation
		base.Args = make([]ir.RawGoTypeRef, 0, len(value.Indices))
		for _, index := range value.Indices {
			argument, ok := c.rawGoType(pkg, aliases, index)
			if !ok {
				return ir.RawGoTypeRef{}, false
			}
			base.Args = append(base.Args, argument)
		}
		base.Span = ref.Span
		return base, true
	default:
		return ir.RawGoTypeRef{}, false
	}
}

func isBuiltinTypeName(name string) bool {
	switch name {
	case "bool", "byte", "complex128", "complex64", "error", "float32", "float64",
		"int", "int16", "int32", "int64", "int8", "rune", "string", "uint", "uint16",
		"uint32", "uint64", "uint8", "uintptr", "any":
		return true
	default:
		return false
	}
}

func (c *compiler) decodeStructTags(pkg *load.Package, field *ast.Field) (db string, hasDB bool, golem string, hasGolem bool, valid bool) {
	if field.Tag == nil {
		return "", false, "", false, true
	}
	decoded, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		c.error(pkg, "P1_STRUCT_TAG_SYNTAX", "invalid Go struct tag literal", field.Tag)
		return "", false, "", false, false
	}
	structTag := reflect.StructTag(decoded)
	db, hasDB = structTag.Lookup("db")
	golem, hasGolem = structTag.Lookup("golem")
	return db, hasDB, golem, hasGolem, true
}

func (c *compiler) validateModelMarker(pkg *load.Package, field *ast.Field, attrs []tags.Attribute) {
	for _, attr := range attrs {
		switch attr.Name {
		case "model":
			c.requireFlag(pkg, field, attr)
		case "id", "renameFrom":
			c.requireStableID(pkg, field, attr)
		case "table", "graphql":
			c.requireIdentifier(pkg, field, attr)
		default:
			c.error(pkg, "P1_MODEL_MARKER_ATTRIBUTE", fmt.Sprintf("%s is not valid on a model marker", attr.Name), field)
		}
	}
	if !hasAttribute(attrs, "table") {
		c.error(pkg, "P1_MODEL_TABLE_MISSING", "model marker requires table=<identifier>", field)
	}
}

func (c *compiler) validateFieldAttributes(pkg *load.Package, field *ast.Field, attrs []tags.Attribute) {
	for _, attr := range attrs {
		switch attr.Name {
		case "pk", "unique", "updated", "readonly", "writeonly", "immutable", "hidden":
			c.requireFlag(pkg, field, attr)
		case "system":
			c.requireFlag(pkg, field, attr)
			if hasAttribute(attrs, "relation") {
				c.error(pkg, "P1_GOLEM_TAG_SYSTEM_RELATION", "system is a scalar field mode and cannot be declared on a relation", field)
			}
		case "id", "renameFrom":
			c.requireStableID(pkg, field, attr)
		case "graphql", "name", "through", "source", "target":
			c.requireIdentifier(pkg, field, attr)
		case "fields", "references":
			if !attr.HasValue || !identifierList(attr.Value) {
				c.error(pkg, "P1_GOLEM_TAG_IDENTIFIER_LIST", fmt.Sprintf("%s requires a comma-separated identifier list", attr.Name), field)
			}
		case "relation":
			if !attr.HasValue || !validRelationKind(attr.Value) {
				c.error(pkg, "P1_GOLEM_TAG_RELATION", "relation must be belongs_to, has_one, has_many, or many_to_many", field)
			}
		case "type", "default":
			if !attr.HasValue {
				c.error(pkg, "P1_GOLEM_TAG_MISSING_VALUE", fmt.Sprintf("%s requires a value", attr.Name), field)
			}
		}
	}
}

func (c *compiler) requireFlag(pkg *load.Package, field *ast.Field, attr tags.Attribute) {
	if attr.HasValue {
		c.error(pkg, "P1_GOLEM_TAG_FLAG_VALUE", fmt.Sprintf("%s is a flag and cannot have a value", attr.Name), field)
	}
}

func (c *compiler) requireStableID(pkg *load.Package, field *ast.Field, attr tags.Attribute) {
	if !attr.HasValue || !tags.IsStableID(attr.Value) {
		c.error(pkg, "P1_GOLEM_TAG_STABLE_ID", fmt.Sprintf("%s requires a valid stable ID", attr.Name), field)
	}
}

func (c *compiler) requireIdentifier(pkg *load.Package, field *ast.Field, attr tags.Attribute) {
	if !attr.HasValue || !tags.IsIdentifier(attr.Value) {
		c.error(pkg, "P1_GOLEM_TAG_IDENTIFIER", fmt.Sprintf("%s requires an unquoted identifier", attr.Name), field)
	}
}

func (c *compiler) appendTagErrors(pkg *load.Package, field *ast.Field, errs []*tags.Error) {
	for _, err := range errs {
		c.error(pkg, err.Code, err.Text, field)
	}
}

func (c *compiler) extractMethods(pkg *load.Package, receiverName string) {
	for _, file := range pkg.Files {
		for _, declaration := range file.AST.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || len(function.Recv.List) != 1 || receiverTypeName(function.Recv.List[0].Type) != receiverName {
				continue
			}
			c.methods = append(c.methods, ir.RawMethodDecl{
				ReceiverPackage: pkg.ImportPath, ReceiverGoName: receiverName, Name: function.Name.Name,
				Signature: nodeText(pkg, function.Type), BodySyntax: nodeText(pkg, function.Body), Span: sourceSpan(pkg, function.Pos(), function.End()),
			})
		}
	}
}

func receiverTypeName(expression ast.Expr) string {
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	identifier, _ := expression.(*ast.Ident)
	if identifier == nil {
		return ""
	}
	return identifier.Name
}

func rawAttributes(pkg *load.Package, field *ast.Field, attrs []tags.Attribute) []ir.RawAttribute {
	result := make([]ir.RawAttribute, 0, len(attrs))
	for _, attr := range attrs {
		var value *string
		if attr.HasValue {
			copyValue := attr.Value
			value = &copyValue
		}
		result = append(result, ir.RawAttribute{Name: attr.Name, RawValue: value, Span: sourceSpan(pkg, field.Pos(), field.End())})
	}
	return result
}

func hasAttribute(attrs []tags.Attribute, name string) bool {
	for _, attr := range attrs {
		if attr.Name == name {
			return true
		}
	}
	return false
}

func identifierList(value string) bool {
	parts := strings.Split(value, ",")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !tags.IsIdentifier(strings.TrimSpace(part)) {
			return false
		}
	}
	return true
}

func validRelationKind(value string) bool {
	switch value {
	case "belongs_to", "has_one", "has_many", "many_to_many":
		return true
	default:
		return false
	}
}
