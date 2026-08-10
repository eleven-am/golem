package bindings

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"go/format"
	"go/token"
	"path/filepath"
	"sort"
	"strings"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
)

var operations = []struct {
	Name      string
	Operation HookOperation
}{
	{"FindOne", OperationFindOne}, {"FindFirst", OperationFindFirst}, {"FindMany", OperationFindMany},
	{"Create", OperationCreate}, {"Update", OperationUpdate}, {"Delete", OperationDelete},
	{"UpdateMany", OperationUpdateMany}, {"DeleteMany", OperationDeleteMany},
}

func emit(request Request, final bool) ([]File, error) {
	golemPath := request.GolemImportPath
	if golemPath == "" {
		golemPath = modelcodegen.DefaultGolemImportPath
	}
	specs := map[string]modelcodegen.PackageSpec{}
	for _, spec := range request.Packages {
		if spec.ImportPath == "" || !token.IsIdentifier(spec.PackageName) {
			return nil, fmt.Errorf("bindings codegen: invalid package specification %#v", spec)
		}
		if _, duplicate := specs[spec.ImportPath]; duplicate {
			return nil, fmt.Errorf("bindings codegen: duplicate package specification for %q", spec.ImportPath)
		}
		specs[spec.ImportPath] = spec
	}
	modelsByPackage := map[string][]ir.ModelDeclIR{}
	modelByID := map[ir.ModelID]ir.ModelDeclIR{}
	for _, model := range request.Compilation.Model.Models {
		if _, exists := specs[model.Go.PackagePath]; !exists {
			return nil, fmt.Errorf("bindings codegen: missing package specification for %q", model.Go.PackagePath)
		}
		modelsByPackage[model.Go.PackagePath] = append(modelsByPackage[model.Go.PackagePath], model)
		modelByID[model.ID] = model
	}
	entriesByPackage := map[string][]Entry{}
	for _, entry := range request.Entries {
		model, exists := modelByID[entry.ModelID]
		if !exists || model.Go.PackagePath != entry.PackagePath {
			return nil, fmt.Errorf("bindings codegen: entry %s.%s references unavailable model %s", entry.PackagePath, entry.Method, entry.ModelID)
		}
		entriesByPackage[entry.PackagePath] = append(entriesByPackage[entry.PackagePath], entry)
	}
	paths := make([]string, 0, len(modelsByPackage))
	for packagePath := range modelsByPackage {
		paths = append(paths, packagePath)
	}
	sort.Strings(paths)
	files := make([]File, 0, len(paths))
	for _, packagePath := range paths {
		models := modelsByPackage[packagePath]
		sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
		entries := entriesByPackage[packagePath]
		sortEntries(entries)
		file, err := emitPackage(specs[packagePath], models, entries, request.Compilation.Model.Schema.Actor, golemPath, request.GenerationDigest, request.GeneratorVersion, request.TemplateABIVersion, final)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func emitPackage(spec modelcodegen.PackageSpec, models []ir.ModelDeclIR, entries []Entry, actor ir.GoNamedTypeIR, golemPath, digest, generatorVersion, templateABIVersion string, final bool) (File, error) {
	imports := newImportSet(spec.ImportPath)
	golem := imports.qualify(golemPath, "golem")
	actorType := ""
	if final {
		actorType = actor.Name
		if actor.PackagePath != spec.ImportPath {
			actorType = imports.qualify(actor.PackagePath, "actorpkg") + "." + actor.Name
		}
		if actor.PackagePath == "" || !token.IsIdentifier(actor.Name) {
			return File{}, fmt.Errorf("bindings codegen: invalid actor identity %#v", actor)
		}
		if hasHook(entries) {
			imports.qualify("context", "context")
		}
	}
	for _, model := range models {
		decoded, err := hex.DecodeString(string(model.ID))
		if err != nil || len(decoded) != 16 {
			return File{}, fmt.Errorf("bindings codegen: model %s has a non-canonical 128-bit ID", model.ID)
		}
	}

	var body bytes.Buffer
	for _, model := range models {
		for _, operation := range operations {
			fmt.Fprintf(&body, "type %s%sRequest = %s.%sHookRequest[%s]\n", model.Go.Name, operation.Name, golem, operation.Name, model.Go.Name)
			fmt.Fprintf(&body, "type %s%sResult = %s.%sHookResult[%s]\n", model.Go.Name, operation.Name, golem, operation.Name, model.Go.Name)
		}
		body.WriteByte('\n')
	}
	if final {
		constructor := "GeneratedPackageBindings"
		generationArgument := ""
		if digest != "" {
			literal, literalErr := generationDigestLiteral(golem, digest)
			if literalErr != nil {
				return File{}, literalErr
			}
			constructor = "GeneratedStampedPackageBindings"
			generationArgument = literal + ", "
		}
		for _, entry := range entries {
			model := modelByID(models, entry.ModelID)
			if entry.Kind == BindingPolicy {
				fmt.Fprintf(&body, "func golemBuild%sPolicy(actor %s) (%s.FrozenPolicy, error) {\n", model.Go.Name, actorType, golem)
				fmt.Fprintf(&body, "\trules := %s.NewRules[%s]()\n", golem, model.Go.Name)
				fmt.Fprintf(&body, "\t%s{}.DefinePolicy(rules, actor)\n", model.Go.Name)
				fmt.Fprintf(&body, "\treturn rules.Freeze(%s)\n", modelIDLiteral(golem, model.ID))
				body.WriteString("}\n\n")
				continue
			}
			operationName := operationGoName(entry.Operation)
			bridge := "golemInvoke" + model.Go.Name + entry.Method
			if entry.Phase == PhaseBefore {
				fmt.Fprintf(&body, "func %s(ctx context.Context, request *%s%sRequest) error {\n", bridge, model.Go.Name, operationName)
				fmt.Fprintf(&body, "\treturn %s{}.%s(ctx, request)\n}\n\n", model.Go.Name, entry.Method)
			} else {
				fmt.Fprintf(&body, "func %s(ctx context.Context, result %s%sResult) error {\n", bridge, model.Go.Name, operationName)
				fmt.Fprintf(&body, "\treturn %s{}.%s(ctx, result)\n}\n\n", model.Go.Name, entry.Method)
			}
		}
		fmt.Fprintf(&body, "func GolemGeneratedBindings() %s.PackageBindings[%s] {\n", golem, actorType)
		fmt.Fprintf(&body, "\tpolicies := []%s.PolicyBinding[%s]{\n", golem, actorType)
		for _, entry := range entries {
			if entry.Kind != BindingPolicy {
				continue
			}
			model := modelByID(models, entry.ModelID)
			fmt.Fprintf(&body, "\t\t%s.GeneratedPolicyBinding[%s, %s](%s, golemBuild%sPolicy),\n", golem, actorType, model.Go.Name, modelIDLiteral(golem, model.ID), model.Go.Name)
		}
		body.WriteString("\t}\n")
		fmt.Fprintf(&body, "\thooks := []%s.HookBinding[%s]{\n", golem, actorType)
		for _, entry := range entries {
			if entry.Kind != BindingHook {
				continue
			}
			model := modelByID(models, entry.ModelID)
			constructor := "GeneratedAfterHookBinding"
			payload := model.Go.Name + operationGoName(entry.Operation) + "Result"
			if entry.Phase == PhaseBefore {
				constructor, payload = "GeneratedBeforeHookBinding", model.Go.Name+operationGoName(entry.Operation)+"Request"
			}
			if entry.Phase == PhaseAfterCommit {
				constructor = "GeneratedAfterCommitHookBinding"
			}
			fmt.Fprintf(&body, "\t\t%s.%s[%s, %s, %s](%s, %s.%s, golemInvoke%s%s),\n", golem, constructor, actorType, model.Go.Name, payload, modelIDLiteral(golem, model.ID), golem, hookOperationConstant(entry.Operation), model.Go.Name, entry.Method)
		}
		body.WriteString("\t}\n")
		fmt.Fprintf(&body, "\treturn %s.%s(%spolicies, hooks)\n}\n", golem, constructor, generationArgument)
	}

	var source bytes.Buffer
	source.WriteString("// Code generated by golem. DO NOT EDIT.\n")
	if digest != "" {
		fmt.Fprintf(&source, "// Golem generation digest: %s\n", digest)
		if generatorVersion != "" {
			fmt.Fprintf(&source, "// Golem generator version: %s\n", generatorVersion)
		}
		if templateABIVersion != "" {
			fmt.Fprintf(&source, "// Golem template ABI version: %s\n", templateABIVersion)
		}
	}
	fmt.Fprintf(&source, "\npackage %s\n\n", spec.PackageName)
	imports.write(&source)
	source.Write(body.Bytes())
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return File{}, fmt.Errorf("bindings codegen: format package %q: %w\n%s", spec.ImportPath, err, source.String())
	}
	filePath := Filename
	if spec.Directory != "" {
		filePath = filepath.Join(spec.Directory, Filename)
	}
	return File{ImportPath: spec.ImportPath, PackageName: spec.PackageName, Path: filePath, Source: formatted}, nil
}

func generationDigestLiteral(golem, value string) (string, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != value {
		return "", fmt.Errorf("bindings codegen: generation digest %q is not canonical SHA-256", value)
	}
	parts := make([]string, len(decoded))
	for index, b := range decoded {
		parts[index] = fmt.Sprintf("0x%02x", b)
	}
	return golem + ".SchemaDigest{" + strings.Join(parts, ", ") + "}", nil
}

func modelByID(models []ir.ModelDeclIR, id ir.ModelID) ir.ModelDeclIR {
	for _, model := range models {
		if model.ID == id {
			return model
		}
	}
	return ir.ModelDeclIR{}
}

func hasHook(entries []Entry) bool {
	for _, entry := range entries {
		if entry.Kind == BindingHook {
			return true
		}
	}
	return false
}

func operationGoName(operation HookOperation) string {
	for _, value := range operations {
		if value.Operation == operation {
			return value.Name
		}
	}
	return ""
}

func hookOperationConstant(operation HookOperation) string {
	return "Hook" + operationGoName(operation)
}

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ModelID != entries[j].ModelID {
			return entries[i].ModelID < entries[j].ModelID
		}
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		if entries[i].Operation != entries[j].Operation {
			return operationOrdinal(entries[i].Operation) < operationOrdinal(entries[j].Operation)
		}
		if entries[i].Phase != entries[j].Phase {
			return phaseOrdinal(entries[i].Phase) < phaseOrdinal(entries[j].Phase)
		}
		return entries[i].Method < entries[j].Method
	})
}

func operationOrdinal(operation HookOperation) int {
	for index, value := range operations {
		if value.Operation == operation {
			return index
		}
	}
	return len(operations)
}
func phaseOrdinal(phase HookPhase) int {
	switch phase {
	case PhaseBefore:
		return 0
	case PhaseAfter:
		return 1
	case PhaseAfterCommit:
		return 2
	}
	return 3
}

func modelIDLiteral(golem string, id ir.ModelID) string {
	decoded, _ := hex.DecodeString(string(id))
	parts := make([]string, len(decoded))
	for index, value := range decoded {
		parts[index] = fmt.Sprintf("0x%02x", value)
	}
	return golem + ".ModelID{" + strings.Join(parts, ", ") + "}"
}

type importSet struct {
	current string
	aliases map[string]string
	used    map[string]bool
}

func newImportSet(current string) *importSet {
	return &importSet{current: current, aliases: map[string]string{}, used: map[string]bool{}}
}
func (set *importSet) qualify(importPath, preferred string) string {
	if importPath == set.current {
		return ""
	}
	if alias := set.aliases[importPath]; alias != "" {
		return alias
	}
	alias := preferred
	for suffix := 2; set.used[alias]; suffix++ {
		alias = fmt.Sprintf("%s%d", preferred, suffix)
	}
	set.aliases[importPath], set.used[alias] = alias, true
	return alias
}
func (set *importSet) write(buffer *bytes.Buffer) {
	paths := make([]string, 0, len(set.aliases))
	for value := range set.aliases {
		paths = append(paths, value)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return
	}
	buffer.WriteString("import (\n")
	for _, importPath := range paths {
		fmt.Fprintf(buffer, "\t%s %q\n", set.aliases[importPath], importPath)
	}
	buffer.WriteString(")\n\n")
}
