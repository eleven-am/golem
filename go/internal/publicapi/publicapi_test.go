package publicapi

import (
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

var publicPackages = []string{
	"embedding",
	"events",
	"events/cdctest",
	"events/nats",
	"events/transporttest",
	"golem",
	"golemtest",
	"graphql",
	"observe",
	"observe/otel",
	"observe/slog",
	"provider",
	"provider/postgresql",
	"provider/sqlite",
	"queryplan",
	"queue",
	"render",
	"runtime",
}

func TestPublicSurfaceMatchesItsRecord(t *testing.T) {
	root := moduleRoot(t)
	recorded, err := os.ReadFile(filepath.Join(root, "internal", "publicapi", "surface.txt"))
	if err != nil {
		t.Fatalf("the public surface record is unreadable: %v", err)
	}
	actual := strings.Join(exportedSurface(t, root), "\n") + "\n"
	if string(recorded) == actual {
		return
	}
	recordedLines := strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")
	actualLines := strings.Split(strings.TrimSuffix(actual, "\n"), "\n")
	added, removed := difference(actualLines, recordedLines), difference(recordedLines, actualLines)
	if err := os.WriteFile(filepath.Join(root, "internal", "publicapi", "surface.actual.txt"), []byte(actual), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("the public surface changed.\nremoved (breaking):\n  %s\nadded:\n  %s\n\nIf this is intended, replace surface.txt with surface.actual.txt and say why in the commit.",
		strings.Join(limit(removed), "\n  "), strings.Join(limit(added), "\n  "))
}

type recorder struct {
	module  string
	public  map[string]bool
	lines   map[string]bool
	reached map[string]*types.TypeName
	done    map[string]bool
}

type renderer struct {
	*recorder
	current string
}

func exportedSurface(t *testing.T, root string) []string {
	t.Helper()
	patterns := make([]string, len(publicPackages))
	for index, packagePath := range publicPackages {
		patterns[index] = "./" + packagePath
	}
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedModule,
		Dir:  root,
	}, patterns...)
	if err != nil {
		t.Fatal(err)
	}
	if packages.PrintErrors(loaded) > 0 {
		t.Fatal("the public packages did not type-check")
	}
	if len(loaded) != len(publicPackages) {
		t.Fatalf("loaded %d public packages, want %d", len(loaded), len(publicPackages))
	}
	state := &recorder{
		public:  make(map[string]bool, len(publicPackages)),
		lines:   make(map[string]bool),
		reached: make(map[string]*types.TypeName),
		done:    make(map[string]bool),
	}
	for _, packagePath := range publicPackages {
		state.public[packagePath] = true
	}
	for _, loadedPackage := range loaded {
		if loadedPackage.Module == nil {
			t.Fatalf("%s has no module", loadedPackage.PkgPath)
		}
		state.module = loadedPackage.Module.Path
	}
	for _, loadedPackage := range loaded {
		state.recordPackage(loadedPackage.Types)
	}
	for {
		pending := make([]string, 0)
		for key := range state.reached {
			if !state.done[key] {
				pending = append(pending, key)
			}
		}
		if len(pending) == 0 {
			break
		}
		sort.Strings(pending)
		for _, key := range pending {
			state.done[key] = true
			object := state.reached[key]
			scope := &renderer{recorder: state, current: object.Pkg().Path()}
			scope.recordType(object)
		}
	}
	surface := make([]string, 0, len(state.lines))
	for line := range state.lines {
		surface = append(surface, line)
	}
	sort.Strings(surface)
	return surface
}

func (state *recorder) relative(path string) string {
	if path == state.module {
		return "."
	}
	return strings.TrimPrefix(path, state.module+"/")
}

func (state *recorder) owned(path string) bool {
	return path == state.module || strings.HasPrefix(path, state.module+"/")
}

func (state *recorder) recordPackage(pkg *types.Package) {
	scope := &renderer{recorder: state, current: pkg.Path()}
	for _, name := range pkg.Scope().Names() {
		object := pkg.Scope().Lookup(name)
		if !object.Exported() {
			continue
		}
		prefix := state.relative(pkg.Path()) + "." + name
		switch typed := object.(type) {
		case *types.Func:
			signature := typed.Type().(*types.Signature)
			scope.emit(prefix + " func" + scope.signature(signature, signature.TypeParams()))
		case *types.Const:
			scope.emit(prefix + " const " + scope.render(typed.Type()) + " = " + typed.Val().ExactString())
		case *types.Var:
			scope.emit(prefix + " var " + scope.render(typed.Type()))
		case *types.TypeName:
			state.done[prefix] = true
			scope.recordType(typed)
		}
	}
}

func (scope *renderer) emit(line string) {
	scope.lines[line] = true
}

func (scope *renderer) recordType(object *types.TypeName) {
	packagePath := scope.relative(object.Pkg().Path())
	display := packagePath + "." + object.Name()
	if object.IsAlias() {
		parameters := ""
		if alias, ok := object.Type().(*types.Alias); ok {
			parameters = scope.typeParameters(alias.TypeParams())
		}
		scope.emit(display + parameters + " = " + scope.render(types.Unalias(object.Type())))
		return
	}
	named, ok := object.Type().(*types.Named)
	if !ok {
		scope.emit(display + " type " + scope.render(object.Type()))
		return
	}
	parameters := scope.typeParameters(named.TypeParams())
	switch underlying := named.Underlying().(type) {
	case *types.Struct:
		scope.emit(display + parameters + " type struct")
		scope.recordStruct(display, object, named, underlying)
	case *types.Interface:
		scope.emit(display + parameters + " type " + scope.interfaceHeader(underlying))
		scope.recordInterface(display, underlying)
		return
	default:
		scope.emit(display + parameters + " type " + scope.render(underlying))
	}
	if types.Comparable(named) {
		scope.emit(display + " comparable")
	}
	scope.recordMethods(packagePath, object.Name(), named)
}

func (scope *renderer) recordStruct(display string, object *types.TypeName, named *types.Named, structure *types.Struct) {
	allExported := true
	order := make([]string, 0, structure.NumFields())
	for index := 0; index < structure.NumFields(); index++ {
		field := structure.Field(index)
		if !field.Exported() {
			allExported = false
			continue
		}
		order = append(order, field.Name())
		line := display + "." + field.Name() + " field " + scope.render(field.Type())
		if field.Embedded() {
			line += " embedded"
		}
		if tag := structure.Tag(index); tag != "" {
			line += " tag " + strconv.Quote(tag)
		}
		scope.emit(line)
	}
	if allExported && object.Exported() {
		scope.emit(display + " unkeyed literal {" + strings.Join(order, ", ") + "}")
	}
	for _, name := range promotedFieldNames(structure) {
		found, path, _ := types.LookupFieldOrMethod(named, false, object.Pkg(), name)
		field, ok := found.(*types.Var)
		if !ok || !field.IsField() || len(path) < 2 {
			continue
		}
		scope.emit(display + "." + name + " promoted field " + scope.render(field.Type()))
	}
}

func promotedFieldNames(structure *types.Struct) []string {
	names := make(map[string]bool)
	visited := make(map[*types.Named]bool)
	var walk func(current *types.Struct, depth int)
	walk = func(current *types.Struct, depth int) {
		for index := 0; index < current.NumFields(); index++ {
			field := current.Field(index)
			if depth > 0 && field.Exported() {
				names[field.Name()] = true
			}
			if !field.Embedded() {
				continue
			}
			embedded := types.Unalias(field.Type())
			if pointer, ok := embedded.(*types.Pointer); ok {
				embedded = types.Unalias(pointer.Elem())
			}
			if named, ok := embedded.(*types.Named); ok {
				if visited[named] {
					continue
				}
				visited[named] = true
			}
			if inner, ok := embedded.Underlying().(*types.Struct); ok {
				walk(inner, depth+1)
			}
		}
	}
	walk(structure, 0)
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	return sorted
}

func (scope *renderer) recordMethods(packagePath, name string, named *types.Named) {
	for _, receiver := range []struct {
		label string
		typ   types.Type
	}{
		{"(" + name + ")", named},
		{"(*" + name + ")", types.NewPointer(named)},
	} {
		set := types.NewMethodSet(receiver.typ)
		for index := 0; index < set.Len(); index++ {
			method := set.At(index).Obj()
			if !method.Exported() {
				continue
			}
			signature := set.At(index).Type().(*types.Signature)
			scope.emit(packagePath + "." + receiver.label + "." + method.Name() + " func" + scope.signature(signature, nil))
		}
	}
}

func (scope *renderer) interfaceHeader(declared *types.Interface) string {
	names := make([]string, 0, declared.NumMethods())
	sealed := false
	for index := 0; index < declared.NumMethods(); index++ {
		method := declared.Method(index)
		if !method.Exported() {
			sealed = true
			continue
		}
		names = append(names, method.Name())
	}
	header := "interface {" + strings.Join(names, ", ") + "}"
	if sealed {
		header += " sealed"
	}
	return header
}

func (scope *renderer) recordInterface(display string, declared *types.Interface) {
	for index := 0; index < declared.NumMethods(); index++ {
		method := declared.Method(index)
		if !method.Exported() {
			continue
		}
		scope.emit(display + "." + method.Name() + " func" + scope.signature(method.Type().(*types.Signature), nil))
	}
	for _, element := range scope.typeSetElements(declared) {
		scope.emit(display + " element " + element)
	}
}

func (scope *renderer) typeSetElements(declared *types.Interface) []string {
	var elements []string
	for index := 0; index < declared.NumEmbeddeds(); index++ {
		embedded := declared.EmbeddedType(index)
		if inner, ok := embedded.Underlying().(*types.Interface); ok && inner.IsMethodSet() {
			continue
		}
		elements = append(elements, scope.render(embedded))
	}
	sort.Strings(elements)
	return elements
}

func (scope *renderer) typeParameters(list *types.TypeParamList) string {
	if list == nil || list.Len() == 0 {
		return ""
	}
	parts := make([]string, list.Len())
	for index := 0; index < list.Len(); index++ {
		parameter := list.At(index)
		parts[index] = parameter.Obj().Name() + " " + scope.render(parameter.Constraint())
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (scope *renderer) signature(signature *types.Signature, parameters *types.TypeParamList) string {
	params := signature.Params()
	inputs := make([]string, params.Len())
	for index := 0; index < params.Len(); index++ {
		parameter := params.At(index).Type()
		if signature.Variadic() && index == params.Len()-1 {
			inputs[index] = "..." + scope.render(parameter.(*types.Slice).Elem())
			continue
		}
		inputs[index] = scope.render(parameter)
	}
	rendered := scope.typeParameters(parameters) + "(" + strings.Join(inputs, ", ") + ")"
	results := signature.Results()
	switch results.Len() {
	case 0:
		return rendered
	case 1:
		return rendered + " " + scope.render(results.At(0).Type())
	}
	outputs := make([]string, results.Len())
	for index := 0; index < results.Len(); index++ {
		outputs[index] = scope.render(results.At(index).Type())
	}
	return rendered + " (" + strings.Join(outputs, ", ") + ")"
}

func (scope *renderer) render(typ types.Type) string {
	switch typed := typ.(type) {
	case *types.Alias:
		return scope.render(types.Unalias(typed))
	case *types.Basic:
		if typed.Kind() == types.UnsafePointer {
			return "unsafe.Pointer"
		}
		return types.Typ[typed.Kind()].Name()
	case *types.Pointer:
		return "*" + scope.render(typed.Elem())
	case *types.Slice:
		return "[]" + scope.render(typed.Elem())
	case *types.Array:
		return fmt.Sprintf("[%d]%s", typed.Len(), scope.render(typed.Elem()))
	case *types.Map:
		return "map[" + scope.render(typed.Key()) + "]" + scope.render(typed.Elem())
	case *types.Chan:
		element := scope.render(typed.Elem())
		switch typed.Dir() {
		case types.SendOnly:
			return "chan<- " + element
		case types.RecvOnly:
			return "<-chan " + element
		}
		if inner, ok := typed.Elem().(*types.Chan); ok && inner.Dir() == types.RecvOnly {
			return "chan (" + element + ")"
		}
		return "chan " + element
	case *types.Signature:
		return "func" + scope.signature(typed, typed.TypeParams())
	case *types.Struct:
		fields := make([]string, typed.NumFields())
		for index := 0; index < typed.NumFields(); index++ {
			field := typed.Field(index)
			rendered := scope.render(field.Type())
			if !field.Embedded() {
				rendered = scope.fieldName(field) + " " + rendered
			}
			if tag := typed.Tag(index); tag != "" {
				rendered += " " + strconv.Quote(tag)
			}
			fields[index] = rendered
		}
		return "struct{" + strings.Join(fields, "; ") + "}"
	case *types.Interface:
		return scope.renderInterface(typed)
	case *types.Union:
		terms := make([]string, typed.Len())
		for index := 0; index < typed.Len(); index++ {
			term := typed.Term(index)
			terms[index] = scope.render(term.Type())
			if term.Tilde() {
				terms[index] = "~" + terms[index]
			}
		}
		return strings.Join(terms, " | ")
	case *types.TypeParam:
		return typed.Obj().Name()
	case *types.Named:
		return scope.renderNamed(typed)
	}
	return typ.String()
}

func (scope *renderer) fieldName(field *types.Var) string {
	if field.Exported() || field.Pkg() == nil {
		return field.Name()
	}
	return scope.relative(field.Pkg().Path()) + "." + field.Name()
}

func (scope *renderer) renderInterface(declared *types.Interface) string {
	if declared.IsImplicit() && declared.NumEmbeddeds() == 1 {
		return scope.render(declared.EmbeddedType(0))
	}
	var elements []string
	for index := 0; index < declared.NumMethods(); index++ {
		method := declared.Method(index)
		elements = append(elements, scope.methodName(method)+scope.signature(method.Type().(*types.Signature), nil))
	}
	elements = append(elements, scope.typeSetElements(declared)...)
	if len(elements) == 0 {
		return "any"
	}
	return "interface{" + strings.Join(elements, "; ") + "}"
}

func (scope *renderer) methodName(method *types.Func) string {
	if method.Exported() || method.Pkg() == nil {
		return method.Name()
	}
	return scope.relative(method.Pkg().Path()) + "." + method.Name()
}

func (scope *renderer) renderNamed(named *types.Named) string {
	object := named.Obj()
	name := object.Name()
	if object.Pkg() != nil {
		path := object.Pkg().Path()
		if scope.owned(path) {
			relative := scope.relative(path)
			if !scope.public[relative] || !object.Exported() {
				origin := named.Origin().Obj()
				key := relative + "." + origin.Name()
				if _, seen := scope.reached[key]; !seen {
					scope.reached[key] = origin
				}
			}
			if path != scope.current {
				name = relative + "." + name
			}
		} else {
			name = path + "." + name
		}
	}
	arguments := named.TypeArgs()
	if arguments == nil || arguments.Len() == 0 {
		return name
	}
	parts := make([]string, arguments.Len())
	for index := 0; index < arguments.Len(); index++ {
		parts[index] = scope.render(arguments.At(index))
	}
	return name + "[" + strings.Join(parts, ", ") + "]"
}

func difference(from, against []string) []string {
	present := make(map[string]bool, len(against))
	for _, value := range against {
		present[value] = true
	}
	var result []string
	for _, value := range from {
		if !present[value] {
			result = append(result, value)
		}
	}
	return result
}

func limit(values []string) []string {
	if len(values) == 0 {
		return []string{"(none)"}
	}
	if len(values) > 20 {
		return append(values[:20], "... and more")
	}
	return values
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal(err)
	}
	return root
}
