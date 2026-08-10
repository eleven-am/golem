package compatibility

import (
	"context"
	"encoding/json"
	"go/token"
	"go/types"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

const APIInventoryFormatVersion uint16 = 1

type APIInventory struct {
	FormatVersion uint16       `json:"formatVersion"`
	Packages      []APIPackage `json:"packages"`
}

type APIPackage struct {
	Path         string           `json:"path"`
	Declarations []APIDeclaration `json:"declarations"`
}

type APIDeclaration struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	TypeKind  string   `json:"typeKind"`
	Signature string   `json:"signature"`
	Methods   []string `json:"methods"`
}

type APIRequest struct {
	Directory string
	Patterns  []string
	Env       []string
	// IncludeFile selects declaration source files. Nil includes the complete
	// package. It is used to freeze generated-only ABI without confusing it
	// with handwritten declarations in the same application package.
	IncludeFile func(string) bool
}

type LayerChange string

const (
	LayerUnchanged LayerChange = "unchanged"
	LayerAdditive  LayerChange = "additive"
	LayerBreaking  LayerChange = "breaking"
)

// CompareAPI classifies source compatibility without using a digest as a
// semantic oracle. Digests bind exact bytes; this inventory comparison decides
// whether those bytes changed additively or break an existing consumer.
func CompareAPI(previous, current APIInventory) LayerChange {
	if !canonicalAPI(previous) || !canonicalAPI(current) {
		return LayerBreaking
	}
	change := LayerUnchanged
	currentPackages := make(map[string]APIPackage, len(current.Packages))
	for _, pkg := range current.Packages {
		currentPackages[pkg.Path] = pkg
	}
	previousPackages := make(map[string]bool, len(previous.Packages))
	for _, beforePackage := range previous.Packages {
		previousPackages[beforePackage.Path] = true
		afterPackage, exists := currentPackages[beforePackage.Path]
		if !exists {
			return LayerBreaking
		}
		classified := compareAPIPackage(beforePackage, afterPackage)
		if classified == LayerBreaking {
			return LayerBreaking
		}
		if classified == LayerAdditive {
			change = LayerAdditive
		}
	}
	for _, pkg := range current.Packages {
		if !previousPackages[pkg.Path] {
			change = LayerAdditive
		}
	}
	return change
}

func compareAPIPackage(previous, current APIPackage) LayerChange {
	change := LayerUnchanged
	currentDeclarations := make(map[string]APIDeclaration, len(current.Declarations))
	for _, declaration := range current.Declarations {
		currentDeclarations[declaration.Name] = declaration
	}
	previousDeclarations := make(map[string]bool, len(previous.Declarations))
	for _, before := range previous.Declarations {
		previousDeclarations[before.Name] = true
		after, exists := currentDeclarations[before.Name]
		if !exists || before.Kind != after.Kind || before.Signature != after.Signature {
			return LayerBreaking
		}
		methods := compareMethods(before.Methods, after.Methods)
		if methods == LayerBreaking || methods == LayerAdditive && before.TypeKind == "interface" {
			return LayerBreaking
		}
		if methods == LayerAdditive {
			change = LayerAdditive
		}
	}
	for _, declaration := range current.Declarations {
		if !previousDeclarations[declaration.Name] {
			change = LayerAdditive
		}
	}
	return change
}

func compareMethods(previous, current []string) LayerChange {
	currentSet := make(map[string]bool, len(current))
	for _, method := range current {
		currentSet[method] = true
	}
	previousSet := make(map[string]bool, len(previous))
	for _, method := range previous {
		previousSet[method] = true
		if !currentSet[method] {
			return LayerBreaking
		}
	}
	for _, method := range current {
		if !previousSet[method] {
			return LayerAdditive
		}
	}
	return LayerUnchanged
}

func BuildAPIInventory(ctx context.Context, request APIRequest) (APIInventory, error) {
	if ctx == nil || request.Directory == "" || len(request.Patterns) == 0 {
		return APIInventory{}, fail(ReasonInvalidManifest)
	}
	loaded, err := packages.Load(&packages.Config{
		Context: ctx,
		Dir:     request.Directory,
		Env:     request.Env,
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax,
	}, request.Patterns...)
	if err != nil {
		return APIInventory{}, errorsNoDetails("load API inventory")
	}
	for _, pkg := range loaded {
		if len(pkg.Errors) != 0 {
			return APIInventory{}, errorsNoDetails("load API inventory")
		}
	}
	result := APIInventory{FormatVersion: APIInventoryFormatVersion, Packages: make([]APIPackage, 0, len(loaded))}
	for _, pkg := range loaded {
		if pkg.Types == nil || pkg.Fset == nil {
			return APIInventory{}, errorsNoDetails("load API inventory")
		}
		inventory := APIPackage{Path: pkg.PkgPath, Declarations: []APIDeclaration{}}
		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			object := scope.Lookup(name)
			if object == nil || !object.Exported() || request.IncludeFile != nil && !request.IncludeFile(positionFilename(pkg.Fset, object.Pos())) {
				continue
			}
			declaration, ok := apiDeclaration(object)
			if ok {
				inventory.Declarations = append(inventory.Declarations, declaration)
			}
		}
		sort.Slice(inventory.Declarations, func(i, j int) bool {
			return inventory.Declarations[i].Name < inventory.Declarations[j].Name
		})
		result.Packages = append(result.Packages, inventory)
	}
	sort.Slice(result.Packages, func(i, j int) bool { return result.Packages[i].Path < result.Packages[j].Path })
	for index := 1; index < len(result.Packages); index++ {
		if result.Packages[index-1].Path == result.Packages[index].Path {
			return APIInventory{}, errorsNoDetails("duplicate API package")
		}
	}
	return result, nil
}

func EncodeAPIInventory(value APIInventory) ([]byte, error) {
	if value.FormatVersion != APIInventoryFormatVersion || !canonicalAPI(value) {
		return nil, fail(ReasonInvalidManifest)
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, fail(ReasonInvalidEncoding)
	}
	return append(encoded, '\n'), nil
}

func ParseAPIInventory(encoded []byte) (APIInventory, error) {
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	var value APIInventory
	if err := decoder.Decode(&value); err != nil {
		return APIInventory{}, fail(ReasonInvalidEncoding)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return APIInventory{}, fail(ReasonInvalidEncoding)
	}
	canonical, err := EncodeAPIInventory(value)
	if err != nil {
		return APIInventory{}, err
	}
	if string(canonical) != string(encoded) {
		return APIInventory{}, fail(ReasonNoncanonical)
	}
	return value, nil
}

func apiDeclaration(object types.Object) (APIDeclaration, bool) {
	qualifier := func(pkg *types.Package) string {
		if pkg == nil {
			return ""
		}
		return pkg.Path()
	}
	result := APIDeclaration{Name: object.Name(), Methods: []string{}}
	switch value := object.(type) {
	case *types.Const:
		result.Kind = "const"
		result.Signature = types.TypeString(value.Type(), qualifier) + "=" + value.Val().ExactString()
	case *types.Var:
		result.Kind = "var"
		result.Signature = types.TypeString(value.Type(), qualifier)
	case *types.Func:
		result.Kind = "func"
		result.Signature = types.TypeString(value.Type(), qualifier)
	case *types.TypeName:
		if value.IsAlias() {
			result.Kind = "alias"
			result.TypeKind = publicTypeKind(value.Type().Underlying())
			result.Signature = types.TypeString(value.Type(), qualifier)
			return result, true
		}
		result.Kind = "type"
		named, ok := value.Type().(*types.Named)
		if !ok {
			result.Signature = publicUnderlying(value.Type().Underlying(), qualifier)
			return result, true
		}
		result.TypeKind = publicTypeKind(named.Underlying())
		result.Signature = typeParameters(named.TypeParams(), qualifier) + publicUnderlying(named.Underlying(), qualifier)
		result.Methods = exportedMethods(named, qualifier)
	default:
		return APIDeclaration{}, false
	}
	return result, true
}

func publicTypeKind(value types.Type) string {
	switch value.(type) {
	case *types.Interface:
		return "interface"
	case *types.Struct:
		return "struct"
	default:
		return "other"
	}
}

func publicUnderlying(value types.Type, qualifier types.Qualifier) string {
	switch value := value.(type) {
	case *types.Struct:
		fields := make([]string, 0, value.NumFields())
		for index := 0; index < value.NumFields(); index++ {
			field := value.Field(index)
			if !field.Exported() {
				continue
			}
			entry := field.Name() + " " + types.TypeString(field.Type(), qualifier)
			if field.Embedded() {
				entry = "embedded " + entry
			}
			if tag := value.Tag(index); tag != "" {
				entry += " `" + tag + "`"
			}
			fields = append(fields, entry)
		}
		return "struct{" + strings.Join(fields, ";") + "}"
	case *types.Interface:
		value.Complete()
		methods := make([]string, 0, value.NumMethods())
		sealed := false
		for index := 0; index < value.NumMethods(); index++ {
			method := value.Method(index)
			if !method.Exported() {
				sealed = true
				continue
			}
			methods = append(methods, method.Name()+types.TypeString(method.Type(), qualifier))
		}
		sort.Strings(methods)
		if sealed {
			methods = append(methods, "<sealed>")
		}
		return "interface{" + strings.Join(methods, ";") + "}"
	default:
		return types.TypeString(value, qualifier)
	}
}

func typeParameters(parameters *types.TypeParamList, qualifier types.Qualifier) string {
	if parameters == nil || parameters.Len() == 0 {
		return ""
	}
	values := make([]string, parameters.Len())
	for index := range values {
		parameter := parameters.At(index)
		values[index] = parameter.Obj().Name() + " " + types.TypeString(parameter.Constraint(), qualifier)
	}
	return "[" + strings.Join(values, ",") + "]"
}

func exportedMethods(named *types.Named, qualifier types.Qualifier) []string {
	seen := make(map[string]bool)
	var result []string
	for _, candidate := range []struct {
		prefix string
		value  types.Type
	}{{prefix: "value:", value: named}, {prefix: "pointer:", value: types.NewPointer(named)}} {
		set := types.NewMethodSet(candidate.value)
		for index := 0; index < set.Len(); index++ {
			method, ok := set.At(index).Obj().(*types.Func)
			if !ok || !method.Exported() {
				continue
			}
			entry := candidate.prefix + method.Name() + types.TypeString(method.Type(), qualifier)
			if !seen[entry] {
				seen[entry] = true
				result = append(result, entry)
			}
		}
	}
	sort.Strings(result)
	return result
}

func canonicalAPI(value APIInventory) bool {
	if len(value.Packages) == 0 {
		return false
	}
	for packageIndex, pkg := range value.Packages {
		if pkg.Path == "" || packageIndex > 0 && value.Packages[packageIndex-1].Path >= pkg.Path {
			return false
		}
		for declarationIndex, declaration := range pkg.Declarations {
			if declaration.Name == "" || declaration.Kind == "" || declaration.Signature == "" ||
				(declaration.Kind == "type" || declaration.Kind == "alias") != (declaration.TypeKind != "") ||
				declaration.TypeKind != "" && declaration.TypeKind != "interface" && declaration.TypeKind != "struct" && declaration.TypeKind != "other" ||
				declarationIndex > 0 && pkg.Declarations[declarationIndex-1].Name >= declaration.Name {
				return false
			}
			for methodIndex, method := range declaration.Methods {
				if method == "" || methodIndex > 0 && declaration.Methods[methodIndex-1] >= method {
					return false
				}
			}
		}
	}
	return true
}

func positionFilename(files *token.FileSet, position token.Pos) string {
	if !position.IsValid() {
		return ""
	}
	return filepath.Clean(files.PositionFor(position, false).Filename)
}

type closedError string

func (failure closedError) Error() string { return string(failure) }

func errorsNoDetails(message string) error { return closedError(message) }
