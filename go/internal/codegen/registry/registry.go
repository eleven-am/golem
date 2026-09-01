// Package registry emits the generated application-level binding registry that
// composes every model package accessor in canonical import-path order.
package registry

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"go/format"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	modelcodegen "github.com/eleven-am/golem/go/internal/codegen/model"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
)

const Filename = "zz_golem_registry.gen.go"

type Request struct {
	AppPackage         modelcodegen.PackageSpec
	ModelPackages      []modelcodegen.PackageSpec
	Actor              ir.GoNamedTypeIR
	GolemImportPath    string
	RuntimeImportPath  string
	GenerationDigest   string
	GeneratorVersion   string
	TemplateABIVersion string
	Schema             SchemaInput
}

// SchemaInput remains generator-internal. Generated application code receives
// only opaque canonical byte blobs and public fixed-width digest values.
type SchemaInput struct {
	Model               ir.ModelIR
	Contract            ir.ContractIR
	ModelFingerprint    ir.Fingerprint
	ContractFingerprint ir.Fingerprint
	Providers           []ProviderInput
}

type ProviderInput struct {
	Schema            physical.PhysicalSchema
	Fingerprint       ir.Fingerprint
	SystemFingerprint ir.Fingerprint
	MigrationManifest []byte
}

type File struct {
	ImportPath, PackageName, Path string
	Source                        []byte
}

func Emit(request Request) (File, error) {
	if request.GolemImportPath == "" {
		request.GolemImportPath = modelcodegen.DefaultGolemImportPath
	}
	if request.AppPackage.ImportPath == "" || !token.IsIdentifier(request.AppPackage.PackageName) {
		return File{}, fmt.Errorf("registry codegen: invalid application package specification")
	}
	if request.Actor.PackagePath == "" || !token.IsIdentifier(request.Actor.Name) {
		return File{}, fmt.Errorf("registry codegen: invalid actor identity")
	}
	bundle, err := prepareSchemaBundle(request)
	if err != nil {
		return File{}, err
	}
	semantic, err := semanticcontract.IndexesByModel(request.Schema.Model)
	if err != nil {
		return File{}, fmt.Errorf("registry codegen: %w", err)
	}
	if err := validateSemanticMethodNames(semantic); err != nil {
		return File{}, err
	}
	packages := append([]modelcodegen.PackageSpec(nil), request.ModelPackages...)
	sort.Slice(packages, func(i, j int) bool { return packages[i].ImportPath < packages[j].ImportPath })
	for index, spec := range packages {
		if spec.ImportPath == "" || !token.IsIdentifier(spec.PackageName) {
			return File{}, fmt.Errorf("registry codegen: invalid model package specification %#v", spec)
		}
		if index > 0 && packages[index-1].ImportPath == spec.ImportPath {
			return File{}, fmt.Errorf("registry codegen: duplicate model package %q", spec.ImportPath)
		}
	}
	imports := newImports(request.AppPackage.ImportPath)
	golem := imports.qualify(request.GolemImportPath, "golem")
	actorAlias := imports.qualify(request.Actor.PackagePath, "actorpkg")
	actorType := request.Actor.Name
	if actorAlias != "" {
		actorType = actorAlias + "." + request.Actor.Name
	}
	aliases := make([]string, len(packages))
	for index, spec := range packages {
		aliases[index] = imports.qualify(spec.ImportPath, "models")
	}
	runtimePath := request.RuntimeImportPath
	if runtimePath == "" {
		runtimePath = strings.TrimSuffix(request.GolemImportPath, "/golem") + "/runtime"
	}
	golemRuntime := imports.qualify(runtimePath, "golemruntime")
	eventsPath := strings.TrimSuffix(request.GolemImportPath, "/golem") + "/events"
	eventsAlias := imports.qualify(eventsPath, "events")
	observePath := strings.TrimSuffix(request.GolemImportPath, "/golem") + "/observe"
	observeAlias := imports.qualify(observePath, "observe")
	providerPath := strings.TrimSuffix(request.GolemImportPath, "/golem") + "/provider"
	providerAlias := imports.qualify(providerPath, "provider")
	embeddingPath := strings.TrimSuffix(request.GolemImportPath, "/golem") + "/embedding"
	embeddingAlias := imports.qualify(embeddingPath, "embedding")
	queuePath := strings.TrimSuffix(request.GolemImportPath, "/golem") + "/queue"
	queueAlias := imports.qualify(queuePath, "queue")
	contextAlias := imports.qualify("context", "context")
	fmtAlias := imports.qualify("fmt", "fmt")
	models := append([]ir.ModelDeclIR(nil), request.Schema.Model.Models...)
	sort.Slice(models, func(i, j int) bool {
		if models[i].Go.PackagePath != models[j].Go.PackagePath {
			return models[i].Go.PackagePath < models[j].Go.PackagePath
		}
		return models[i].ID < models[j].ID
	})
	modelAliases := make(map[string]string)
	for _, model := range models {
		modelAliases[model.Go.PackagePath] = imports.qualify(model.Go.PackagePath, "models")
	}
	queryplanAlias := ""
	if len(models) != 0 {
		queryplanPath := strings.TrimSuffix(request.GolemImportPath, "/golem") + "/queryplan"
		queryplanAlias = imports.qualify(queryplanPath, "queryplan")
	}
	var source bytes.Buffer
	source.WriteString("// Code generated by golem. DO NOT EDIT.\n")
	if request.GenerationDigest != "" {
		fmt.Fprintf(&source, "// Golem generation digest: %s\n", request.GenerationDigest)
		if request.GeneratorVersion != "" {
			fmt.Fprintf(&source, "// Golem generator version: %s\n", request.GeneratorVersion)
		}
		if request.TemplateABIVersion != "" {
			fmt.Fprintf(&source, "// Golem template ABI version: %s\n", request.TemplateABIVersion)
		}
	}
	fmt.Fprintf(&source, "\npackage %s\n\n", request.AppPackage.PackageName)
	imports.write(&source)
	fmt.Fprintf(&source, "func golemGeneratedGenerationDigest() %s.SchemaDigest {\n", golem)
	fmt.Fprintf(&source, "\treturn %s\n}\n", digestLiteral(golem, bundle.generationDigest))
	fmt.Fprintf(&source, "\nfunc GolemGeneratedApplicationBindings() (%s.ApplicationBindings[%s], error) {\n", golem, actorType)
	fmt.Fprintf(&source, "\treturn %s.GeneratedApplicationBindings(golemGeneratedGenerationDigest(),\n", golem)
	for _, alias := range aliases {
		if alias == "" {
			source.WriteString("\t\tGolemGeneratedBindings(),\n")
		} else {
			fmt.Fprintf(&source, "\t\t%s.GolemGeneratedBindings(),\n", alias)
		}
	}
	source.WriteString("\t)\n}\n")
	fmt.Fprintf(&source, "\nfunc GolemGeneratedSchemaBundle() %s.SchemaBundle {\n", golem)
	fmt.Fprintf(&source, "\treturn %s.GeneratedSchemaBundle(\n", golem)
	source.WriteString("\t\tgolemGeneratedGenerationDigest(),\n")
	fmt.Fprintf(&source, "\t\t%q,\n\t\t%q,\n", request.GeneratorVersion, request.TemplateABIVersion)
	fmt.Fprintf(&source, "\t\t%s,\n", documentLiteral(golem, bundle.model))
	fmt.Fprintf(&source, "\t\t%s,\n", documentLiteral(golem, bundle.contract))
	for _, provider := range bundle.providers {
		if len(provider.migrationManifest) == 0 {
			fmt.Fprintf(&source, "\t\t%s.GeneratedProviderSchemaDocument(%s, %s, %s),\n", golem, providerLiteral(golem, provider.provider), digestLiteral(golem, provider.systemFingerprint), documentLiteral(golem, provider.document))
		} else {
			fmt.Fprintf(&source, "\t\t%s.GeneratedProviderSchemaDocumentWithMigration(%s, %s, %s, %s.GeneratedMigrationManifestDocument(golemGeneratedGenerationDigest(), %s, []byte(%q))),\n", golem, providerLiteral(golem, provider.provider), digestLiteral(golem, provider.systemFingerprint), documentLiteral(golem, provider.document), golem, providerLiteral(golem, provider.provider), string(provider.migrationManifest))
		}
	}
	source.WriteString("\t)\n}\n")
	fmt.Fprintf(&source, "\nfunc GolemGeneratedApplicationDescriptors() (%s.ApplicationDescriptors, error) {\n", golem)
	fmt.Fprintf(&source, "\treturn %s.GeneratedApplicationDescriptors(golemGeneratedGenerationDigest(),\n", golem)
	for _, alias := range aliases {
		if alias == "" {
			source.WriteString("\t\tGolemGeneratedDescriptors(),\n")
		} else {
			fmt.Fprintf(&source, "\t\t%s.GolemGeneratedDescriptors(),\n", alias)
		}
	}
	source.WriteString("\t)\n}\n")
	subscribedPackages := subscriptionPackages(request.Schema.Model, request.Schema.Contract)
	if len(subscribedPackages) != 0 {
		fmt.Fprintf(&source, "\nfunc GolemGeneratedApplicationEventRegistry() (%s.EventRegistry, error) {\n", golem)
		fmt.Fprintf(&source, "\treturn %s.GeneratedEventRegistry(golemGeneratedGenerationDigest(),\n", golem)
		for index, spec := range packages {
			if !subscribedPackages[spec.ImportPath] {
				continue
			}
			if aliases[index] == "" {
				source.WriteString("\t\tGolemGeneratedEventModels(),\n")
			} else {
				fmt.Fprintf(&source, "\t\t%s.GolemGeneratedEventModels(),\n", aliases[index])
			}
		}
		source.WriteString("\t)\n}\n")
		fmt.Fprintf(&source, "\nfunc GolemGeneratedApplicationEventFactories() (%s.EventFactoryRegistry, error) {\n", golemRuntime)
		fmt.Fprintf(&source, "\treturn %s.GeneratedEventFactoryRegistry(golemGeneratedGenerationDigest(),\n", golemRuntime)
		for index, spec := range packages {
			if !subscribedPackages[spec.ImportPath] {
				continue
			}
			if aliases[index] == "" {
				source.WriteString("\t\tGolemGeneratedEventFactories(),\n")
			} else {
				fmt.Fprintf(&source, "\t\t%s.GolemGeneratedEventFactories(),\n", aliases[index])
			}
		}
		source.WriteString("\t)\n}\n")
	}
	emitRuntimeSurface(&source, actorType, contextAlias, fmtAlias, providerAlias, embeddingAlias, queueAlias, golem, golemRuntime, queryplanAlias, eventsAlias, observeAlias, models, modelAliases, contractModels(request.Schema.Contract), semantic)
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return File{}, fmt.Errorf("registry codegen: format: %w\n%s", err, source.String())
	}
	path := Filename
	if request.AppPackage.Directory != "" {
		path = filepath.Join(request.AppPackage.Directory, Filename)
	}
	return File{ImportPath: request.AppPackage.ImportPath, PackageName: request.AppPackage.PackageName, Path: path, Source: formatted}, nil
}

func emitRuntimeSurface(source *bytes.Buffer, actorType, contextAlias, fmtAlias, providerAlias, embeddingAlias, queueAlias, golemAlias, runtimeAlias, queryplanAlias, eventsAlias, observeAlias string, models []ir.ModelDeclIR, aliases map[string]string, contracts map[ir.ModelID]ir.ModelContractIR, semantic map[ir.ModelID][]semanticcontract.Index) {
	fmt.Fprintf(source, "\ntype Config[P any] struct {\n")
	fmt.Fprintf(source, "\tDatabase *%s.Database\n", providerAlias)
	fmt.Fprintf(source, "\tEmbeddings %s.Registry\n", embeddingAlias)
	fmt.Fprintf(source, "\tQueue *%s.QueueConfig\n", runtimeAlias)
	fmt.Fprintf(source, "\tReadLimits %s.ReadLimits\n\tMutationLimits %s.MutationLimits\n\tAnalyticsLimits %s.AnalyticsLimits\n", runtimeAlias, runtimeAlias, runtimeAlias)
	fmt.Fprintf(source, "\tEventLimits %s.Limits\n\tEventTransport %s.EventTransport\n\tObserver %s.Observer\n", eventsAlias, eventsAlias, observeAlias)
	fmt.Fprintf(source, "\tCDCAdapters []%s.CDCAdapter\n\tReportEventOperator %s.OperatorAudit\n", eventsAlias, eventsAlias)
	fmt.Fprintf(source, "\tHistoricalEventBundles []%s.SchemaBundle\n", golemAlias)
	fmt.Fprintf(source, "\tAfterCommitError func(%s.Context, %s.AfterCommitFailure)\n", contextAlias, golemAlias)
	source.WriteString("\tAuditPrincipal func(P) string\n")
	fmt.Fprintf(source, "\tReportScopedQuery func(%s.Context, %s.ScopedAuditRecord)\n", contextAlias, golemAlias)
	fmt.Fprintf(source, "\tResolvePrincipal func(%s.Context, P) (%s, error)\n", contextAlias, actorType)
	source.WriteString("\tSnapshotPrincipal func(P) (P, error)\n")
	fmt.Fprintf(source, "\tSnapshotActor func(%s) (%s, error)\n}\n", actorType, actorType)
	fmt.Fprintf(source, "\ntype App[P any] struct { runtime *%s.App[P, %s]; observer %s.Observer; provider %s.Provider }\n", runtimeAlias, actorType, observeAlias, golemAlias)
	fmt.Fprintf(source, "type Caller[P any] struct {\n\truntime *%s.Caller[P, %s]\n", runtimeAlias, actorType)
	for _, model := range models {
		fmt.Fprintf(source, "\t%s Caller%sClient[P]\n", pluralName(model.LogicalName), model.Go.Name)
	}
	source.WriteString("}\n")
	fmt.Fprintf(source, "type System[P any] struct {\n\truntime %s.System[P, %s]\n", runtimeAlias, actorType)
	for _, model := range models {
		fmt.Fprintf(source, "\t%s System%sClient[P]\n", pluralName(model.LogicalName), model.Go.Name)
	}
	source.WriteString("}\n")
	fmt.Fprintf(source, "type CallerTx[P any] struct {\n\truntime *%s.CallerTx[P, %s]\n", runtimeAlias, actorType)
	for _, model := range models {
		fmt.Fprintf(source, "\t%s CallerTx%sClient[P]\n", pluralName(model.LogicalName), model.Go.Name)
	}
	source.WriteString("}\n")
	fmt.Fprintf(source, "type SystemTx[P any] struct {\n\truntime *%s.SystemTx[P, %s]\n", runtimeAlias, actorType)
	for _, model := range models {
		fmt.Fprintf(source, "\t%s SystemTx%sClient[P]\n", pluralName(model.LogicalName), model.Go.Name)
	}
	source.WriteString("}\n")
	for _, model := range models {
		contract := contracts[model.ID]
		modelType := model.Go.Name
		descriptor := "GolemGenerated" + model.Go.Name + "Descriptor"
		createInput := model.Go.Name + "CreateInput"
		updateInput := model.Go.Name + "UpdateInput"
		updateManyInput := model.Go.Name + "UpdateManyInput"
		if alias := aliases[model.Go.PackagePath]; alias != "" {
			modelType, descriptor = alias+"."+modelType, alias+"."+descriptor
			createInput, updateInput = alias+"."+createInput, alias+"."+updateInput
			updateManyInput = alias + "." + updateManyInput
		}
		versioned := model.OptimisticConcurrency != nil
		fmt.Fprintf(source, "\ntype Caller%sClient[P any] struct { runtime *%s.Caller[P, %s] }\n", model.Go.Name, runtimeAlias, actorType)
		fmt.Fprintf(source, "func (client Caller%sClient[P]) FindMany(ctx %s.Context, options ...%s.ReadOption[%s]) ([]%s.Row[%s], error) { return %s.CallerFindMany(ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor)
		fmt.Fprintf(source, "func (client Caller%sClient[P]) FindFirst(ctx %s.Context, options ...%s.ReadOption[%s]) (%s.Row[%s], bool, error) { return %s.CallerFindFirst(ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor)
		fmt.Fprintf(source, "func (client Caller%sClient[P]) FindUnique(ctx %s.Context, selector %s.UniqueSelectorValue[%s], options ...%s.ReadOption[%s]) (%s.Row[%s], error) { return %s.CallerFindUnique(ctx, client.runtime, %s, selector, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor)
		fmt.Fprintf(source, "func (client Caller%sClient[P]) Count(ctx %s.Context, options ...%s.ReadOption[%s]) (int64, error) { return %s.CallerCount(ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, runtimeAlias, descriptor)
		emitQueryPlanClientMethods(source, model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, queryplanAlias, descriptor, contractHasRelationDimensions(contract), contract.ScopedReads)
		for _, index := range semantic[model.ID] {
			fmt.Fprintf(source, "func (client Caller%sClient[P]) %s(ctx %s.Context, query string, take int, where ...%s.Predicate[%s]) ([]%s.SemanticResult[%s], error) { return %s.CallerSearch(ctx, client.runtime, %s, %q, query, take, where...) }\n", model.Go.Name, semanticSearchMethodName(index.Name), contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor, index.Name)
		}
		if contract.Subscriptions {
			eventType := model.Go.Name + "Event"
			if alias := aliases[model.Go.PackagePath]; alias != "" {
				eventType = alias + "." + eventType
			}
			fmt.Fprintf(source, "func (client Caller%sClient[P]) Events(ctx %s.Context, options ...%s.EventOption[%s]) (%s.EventStream[%s], error) { return %s.CallerEvents[P, %s, %s, %s](ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, eventType, runtimeAlias, actorType, modelType, eventType, descriptor)
		}
		emitAnalyticsClientMethods(source, "Caller", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor, contractHasRelationDimensions(contract))
		if contract.ScopedReads {
			emitScopedClientMethod(source, "Caller", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor)
		}
		emitMutationClientMethods(source, "Caller", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor, createInput, updateInput, updateManyInput, versioned)
		fmt.Fprintf(source, "\ntype System%sClient[P any] struct { runtime %s.System[P, %s] }\n", model.Go.Name, runtimeAlias, actorType)
		fmt.Fprintf(source, "func (client System%sClient[P]) FindMany(ctx %s.Context, options ...%s.ReadOption[%s]) ([]%s.Row[%s], error) { return %s.SystemFindMany(ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor)
		fmt.Fprintf(source, "func (client System%sClient[P]) FindFirst(ctx %s.Context, options ...%s.ReadOption[%s]) (%s.Row[%s], bool, error) { return %s.SystemFindFirst(ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor)
		fmt.Fprintf(source, "func (client System%sClient[P]) FindUnique(ctx %s.Context, selector %s.UniqueSelectorValue[%s], options ...%s.ReadOption[%s]) (%s.Row[%s], error) { return %s.SystemFindUnique(ctx, client.runtime, %s, selector, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor)
		fmt.Fprintf(source, "func (client System%sClient[P]) Count(ctx %s.Context, options ...%s.ReadOption[%s]) (int64, error) { return %s.SystemCount(ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, runtimeAlias, descriptor)
		for _, index := range semantic[model.ID] {
			fmt.Fprintf(source, "func (client System%sClient[P]) %s(ctx %s.Context, query string, take int, where ...%s.Predicate[%s]) ([]%s.SemanticResult[%s], error) { return %s.SystemSearch(ctx, client.runtime, %s, %q, query, take, where...) }\n", model.Go.Name, semanticSearchMethodName(index.Name), contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor, index.Name)
		}
		emitAnalyticsClientMethods(source, "System", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor, contractHasRelationDimensions(contract))
		if contract.ScopedReads {
			emitScopedClientMethod(source, "System", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor)
		}
		emitMutationClientMethods(source, "System", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor, createInput, updateInput, updateManyInput, versioned)
		fmt.Fprintf(source, "\ntype CallerTx%sClient[P any] struct { runtime *%s.CallerTx[P, %s] }\n", model.Go.Name, runtimeAlias, actorType)
		fmt.Fprintf(source, "func (client CallerTx%sClient[P]) FindMany(ctx %s.Context, options ...%s.ReadOption[%s]) ([]%s.Row[%s], error) { return %s.CallerTxFindMany(ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor)
		fmt.Fprintf(source, "func (client CallerTx%sClient[P]) FindFirst(ctx %s.Context, options ...%s.ReadOption[%s]) (%s.Row[%s], bool, error) { return %s.CallerTxFindFirst(ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor)
		fmt.Fprintf(source, "func (client CallerTx%sClient[P]) FindUnique(ctx %s.Context, selector %s.UniqueSelectorValue[%s], options ...%s.ReadOption[%s]) (%s.Row[%s], error) { return %s.CallerTxFindUnique(ctx, client.runtime, %s, selector, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor)
		fmt.Fprintf(source, "func (client CallerTx%sClient[P]) Count(ctx %s.Context, options ...%s.ReadOption[%s]) (int64, error) { return %s.CallerTxCount(ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, runtimeAlias, descriptor)
		emitAnalyticsClientMethods(source, "CallerTx", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor, contractHasRelationDimensions(contract))
		if contract.ScopedReads {
			emitScopedClientMethod(source, "CallerTx", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor)
		}
		emitMutationClientMethods(source, "CallerTx", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor, createInput, updateInput, updateManyInput, versioned)
		fmt.Fprintf(source, "\ntype SystemTx%sClient[P any] struct { runtime *%s.SystemTx[P, %s] }\n", model.Go.Name, runtimeAlias, actorType)
		fmt.Fprintf(source, "func (client SystemTx%sClient[P]) FindMany(ctx %s.Context, options ...%s.ReadOption[%s]) ([]%s.Row[%s], error) { return %s.SystemTxFindMany(ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor)
		fmt.Fprintf(source, "func (client SystemTx%sClient[P]) FindFirst(ctx %s.Context, options ...%s.ReadOption[%s]) (%s.Row[%s], bool, error) { return %s.SystemTxFindFirst(ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor)
		fmt.Fprintf(source, "func (client SystemTx%sClient[P]) FindUnique(ctx %s.Context, selector %s.UniqueSelectorValue[%s], options ...%s.ReadOption[%s]) (%s.Row[%s], error) { return %s.SystemTxFindUnique(ctx, client.runtime, %s, selector, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, golemAlias, modelType, golemAlias, modelType, runtimeAlias, descriptor)
		fmt.Fprintf(source, "func (client SystemTx%sClient[P]) Count(ctx %s.Context, options ...%s.ReadOption[%s]) (int64, error) { return %s.SystemTxCount(ctx, client.runtime, %s, options...) }\n", model.Go.Name, contextAlias, golemAlias, modelType, runtimeAlias, descriptor)
		emitAnalyticsClientMethods(source, "SystemTx", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor, contractHasRelationDimensions(contract))
		if contract.ScopedReads {
			emitScopedClientMethod(source, "SystemTx", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor)
		}
		emitMutationClientMethods(source, "SystemTx", model.Go.Name, modelType, contextAlias, golemAlias, runtimeAlias, descriptor, createInput, updateInput, updateManyInput, versioned)
	}
	fmt.Fprintf(source, "\nfunc Open[P any](ctx %s.Context, config Config[P]) (*App[P], error) {\n", contextAlias)
	fmt.Fprintf(source, "\treturn golemOpen(ctx, config, %s.Config[P, %s]{Database: config.Database})\n}\n", runtimeAlias, actorType)
	fmt.Fprintf(source, "\nfunc golemOpen[P any](ctx %s.Context, config Config[P], engineConfig %s.Config[P, %s]) (*App[P], error) {\n", contextAlias, runtimeAlias, actorType)
	source.WriteString("\tbindings, err := GolemGeneratedApplicationBindings()\n\tif err != nil { return nil, err }\n\tdescriptors, err := GolemGeneratedApplicationDescriptors()\n\tif err != nil { return nil, err }\n")
	if hasSubscriptions(contracts) {
		fmt.Fprintf(source, "\teventRegistry, err := GolemGeneratedApplicationEventRegistry()\n\tif err != nil { return nil, err }\n\teventFactories, err := GolemGeneratedApplicationEventFactories()\n\tif err != nil { return nil, err }\n")
	} else {
		fmt.Fprintf(source, "\teventRegistry := %s.EventRegistry{}\n\teventFactories := %s.EventFactoryRegistry{}\n", golemAlias, runtimeAlias)
	}
	source.WriteString("\tengineConfig.Bundle = GolemGeneratedSchemaBundle()\n\tengineConfig.Bindings = bindings\n\tengineConfig.Descriptors = descriptors\n")
	source.WriteString("\tengineConfig.Embeddings = config.Embeddings\n")
	source.WriteString("\tengineConfig.Queue = config.Queue\n")
	source.WriteString("\tengineConfig.ReadLimits = config.ReadLimits\n\tengineConfig.MutationLimits = config.MutationLimits\n\tengineConfig.AnalyticsLimits = config.AnalyticsLimits\n")
	source.WriteString("\tengineConfig.EventRegistry = eventRegistry\n\tengineConfig.EventFactories = eventFactories\n\tengineConfig.EventLimits = config.EventLimits\n\tengineConfig.EventTransport = config.EventTransport\n\tengineConfig.Observer = config.Observer\n")
	source.WriteString("\tengineConfig.CDCAdapters = config.CDCAdapters\n\tengineConfig.ReportEventOperator = config.ReportEventOperator\n\tengineConfig.HistoricalEventBundles = config.HistoricalEventBundles\n")
	source.WriteString("\tengineConfig.AfterCommitError = config.AfterCommitError\n\tengineConfig.AuditPrincipal = config.AuditPrincipal\n\tengineConfig.ReportScopedQuery = config.ReportScopedQuery\n")
	source.WriteString("\tengineConfig.ResolvePrincipal = config.ResolvePrincipal\n\tengineConfig.SnapshotPrincipal = config.SnapshotPrincipal\n\tengineConfig.SnapshotActor = config.SnapshotActor\n")
	fmt.Fprintf(source, "\tengine, err := %s.Open(ctx, engineConfig)\n", runtimeAlias)
	source.WriteString("\tif err != nil { return nil, err }\n\treturn &App[P]{runtime: engine, observer: config.Observer, provider: config.Database.Provider()}, nil\n}\n")
	fmt.Fprintf(source, "\nfunc (app *App[P]) RunEventPublisher(ctx %s.Context) error { if app == nil { return %s.Failure(%s.CodeEventConfig) }; return app.runtime.RunEventPublisher(ctx) }\n", contextAlias, eventsAlias, eventsAlias)
	fmt.Fprintf(source, "func (app *App[P]) RefreshSemanticIndexes(ctx %s.Context) error { if app == nil { return %s.Errorf(\"P9_SEMANTIC_RUNTIME: application is required\") }; return app.runtime.RefreshSemanticIndexes(ctx) }\n", contextAlias, fmtAlias)
	fmt.Fprintf(source, "func (app *App[P]) RunQueueWorker(ctx %s.Context) error { if app == nil { return %s.Fail(%s.CodeConfigInvalid, \"application is required\") }; return app.runtime.RunQueueWorker(ctx) }\n", contextAlias, queueAlias, queueAlias)
	fmt.Fprintf(source, "func (app *App[P]) Enqueue(ctx %s.Context, pending %s.Pending) (%s.JobID, error) { if app == nil { return \"\", %s.Fail(%s.CodeConfigInvalid, \"application is required\") }; return app.runtime.Enqueue(ctx, pending) }\n", contextAlias, queueAlias, queueAlias, queueAlias, queueAlias)
	fmt.Fprintf(source, "func (app *App[P]) QueueOperator() %s.Operator { if app == nil { return nil }; return app.runtime.QueueOperator() }\n", queueAlias)
	fmt.Fprintf(source, "func (app *App[P]) EventCapabilities() %s.Capabilities { if app == nil { return %s.Capabilities{} }; return app.runtime.EventCapabilities() }\n", eventsAlias, eventsAlias)
	fmt.Fprintf(source, "func (app *App[P]) EventOperator() %s.Operator { if app == nil { return nil }; return app.runtime.EventOperator() }\n", eventsAlias)
	fmt.Fprintf(source, "func (app *App[P]) EventLimits() %s.Limits { if app == nil { return %s.Limits{} }; return app.runtime.EventLimits() }\n", eventsAlias, eventsAlias)
	fmt.Fprintf(source, "\nfunc (app *App[P]) ForPrincipal(ctx %s.Context, principal P) (*Caller[P], error) {\n\tinner, err := app.runtime.ForPrincipal(ctx, principal)\n\tif err != nil { return nil, err }\n\tresult := &Caller[P]{runtime: inner}\n", contextAlias)
	for _, model := range models {
		fmt.Fprintf(source, "\tresult.%s = Caller%sClient[P]{runtime: inner}\n", pluralName(model.LogicalName), model.Go.Name)
	}
	source.WriteString("\treturn result, nil\n}\n")
	source.WriteString("\nfunc (app *App[P]) System() System[P] {\n\tinner := app.runtime.System()\n\tresult := System[P]{runtime: inner}\n")
	for _, model := range models {
		fmt.Fprintf(source, "\tresult.%s = System%sClient[P]{runtime: inner}\n", pluralName(model.LogicalName), model.Go.Name)
	}
	source.WriteString("\treturn result\n}\n")
	fmt.Fprintf(source, "\nfunc (caller *Caller[P]) Transaction(ctx %s.Context, callback func(*CallerTx[P]) error) error {\n", contextAlias)
	fmt.Fprintf(source, "\tif caller == nil { return %s.CallerTransaction(ctx, (*%s.Caller[P, %s])(nil), nil) }\n", runtimeAlias, runtimeAlias, actorType)
	fmt.Fprintf(source, "\tif callback == nil { return %s.CallerTransaction(ctx, caller.runtime, nil) }\n", runtimeAlias)
	fmt.Fprintf(source, "\treturn %s.CallerTransaction(ctx, caller.runtime, func(inner *%s.CallerTx[P, %s]) error {\n", runtimeAlias, runtimeAlias, actorType)
	source.WriteString("\t\tresult := &CallerTx[P]{runtime: inner}\n")
	for _, model := range models {
		fmt.Fprintf(source, "\t\tresult.%s = CallerTx%sClient[P]{runtime: inner}\n", pluralName(model.LogicalName), model.Go.Name)
	}
	source.WriteString("\t\treturn callback(result)\n\t})\n}\n")
	fmt.Fprintf(source, "func (transaction *CallerTx[P]) Enqueue(ctx %s.Context, pending %s.Pending) (%s.JobID, error) { if transaction == nil { return %s.CallerTxEnqueue(ctx, (*%s.CallerTx[P, %s])(nil), pending) }; return %s.CallerTxEnqueue(ctx, transaction.runtime, pending) }\n", contextAlias, queueAlias, queueAlias, runtimeAlias, runtimeAlias, actorType, runtimeAlias)
	fmt.Fprintf(source, "\nfunc (system System[P]) Transaction(ctx %s.Context, callback func(*SystemTx[P]) error) error {\n", contextAlias)
	fmt.Fprintf(source, "\tif callback == nil { return %s.SystemTransaction(ctx, system.runtime, nil) }\n", runtimeAlias)
	fmt.Fprintf(source, "\treturn %s.SystemTransaction(ctx, system.runtime, func(inner *%s.SystemTx[P, %s]) error {\n", runtimeAlias, runtimeAlias, actorType)
	source.WriteString("\t\tresult := &SystemTx[P]{runtime: inner}\n")
	for _, model := range models {
		fmt.Fprintf(source, "\t\tresult.%s = SystemTx%sClient[P]{runtime: inner}\n", pluralName(model.LogicalName), model.Go.Name)
	}
	source.WriteString("\t\treturn callback(result)\n\t})\n}\n")
	fmt.Fprintf(source, "func (transaction *SystemTx[P]) Enqueue(ctx %s.Context, pending %s.Pending) (%s.JobID, error) { if transaction == nil { return %s.SystemTxEnqueue(ctx, (*%s.SystemTx[P, %s])(nil), pending) }; return %s.SystemTxEnqueue(ctx, transaction.runtime, pending) }\n", contextAlias, queueAlias, queueAlias, runtimeAlias, runtimeAlias, actorType, runtimeAlias)
}

func emitQueryPlanClientMethods(source *bytes.Buffer, goName, modelType, contextAlias, golemAlias, runtimeAlias, queryplanAlias, descriptor string, relation, scoped bool) {
	fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainFindMany(ctx %s.Context, options ...%s.ReadOption[%s]) (%s.Report, error) { return %s.CallerExplainFindMany(ctx, client.runtime, %s, options...) }\n", goName, contextAlias, golemAlias, modelType, queryplanAlias, runtimeAlias, descriptor)
	fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainFindFirst(ctx %s.Context, options ...%s.ReadOption[%s]) (%s.Report, error) { return %s.CallerExplainFindFirst(ctx, client.runtime, %s, options...) }\n", goName, contextAlias, golemAlias, modelType, queryplanAlias, runtimeAlias, descriptor)
	fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainFindUnique(ctx %s.Context, selector %s.UniqueSelectorValue[%s], options ...%s.ReadOption[%s]) (%s.Report, error) { return %s.CallerExplainFindUnique(ctx, client.runtime, %s, selector, options...) }\n", goName, contextAlias, golemAlias, modelType, golemAlias, modelType, queryplanAlias, runtimeAlias, descriptor)
	fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainCount(ctx %s.Context, options ...%s.ReadOption[%s]) (%s.Report, error) { return %s.CallerExplainCount(ctx, client.runtime, %s, options...) }\n", goName, contextAlias, golemAlias, modelType, queryplanAlias, runtimeAlias, descriptor)
	fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainAggregate(ctx %s.Context, request %s.AggregateRequest[%s]) (%s.Report, error) { return %s.CallerExplainAggregate(ctx, client.runtime, %s, request) }\n", goName, contextAlias, golemAlias, modelType, queryplanAlias, runtimeAlias, descriptor)
	fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainGroupBy(ctx %s.Context, request %s.GroupRequest[%s]) (%s.Report, error) { return %s.CallerExplainGroupBy(ctx, client.runtime, %s, request) }\n", goName, contextAlias, golemAlias, modelType, queryplanAlias, runtimeAlias, descriptor)
	if relation {
		fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainRelationGroupBy(ctx %s.Context, request %s.RelationGroupRequest[%s]) (%s.Report, error) { return %s.CallerExplainRelationGroupBy(ctx, client.runtime, %s, request) }\n", goName, contextAlias, golemAlias, modelType, queryplanAlias, runtimeAlias, descriptor)
	}
	if scoped {
		fmt.Fprintf(source, "func (client Caller%sClient[P]) ExplainScoped(ctx %s.Context, request %s.ScopedQuery[%s]) (%s.Report, error) { return %s.CallerExplainScoped(ctx, client.runtime, %s, request) }\n", goName, contextAlias, golemAlias, modelType, queryplanAlias, runtimeAlias, descriptor)
	}
}

func emitMutationClientMethods(source *bytes.Buffer, prefix, goName, modelType, contextAlias, golemAlias, runtimeAlias, descriptor, createInput, updateInput, updateManyInput string, versioned bool) {
	fmt.Fprintf(source, "func (client %s%sClient[P]) Create(ctx %s.Context, input %s, projection ...%s.Projection[%s]) (%s.Row[%s], error) { return %s.%sCreate(ctx, client.runtime, %s, input, projection...) }\n", prefix, goName, contextAlias, createInput, golemAlias, modelType, golemAlias, modelType, runtimeAlias, prefix, descriptor)
	if versioned {
		fmt.Fprintf(source, "func (client %s%sClient[P]) Update(ctx %s.Context, target %s.MutationTarget[%s], expected %s.ExistingVersion, input %s, projection ...%s.Projection[%s]) (%s.Row[%s], error) { return %s.%sUpdateVersioned(ctx, client.runtime, %s, target, expected, input, projection...) }\n", prefix, goName, contextAlias, golemAlias, modelType, golemAlias, updateInput, golemAlias, modelType, golemAlias, modelType, runtimeAlias, prefix, descriptor)
		fmt.Fprintf(source, "func (client %s%sClient[P]) Upsert(ctx %s.Context, target %s.MutationTarget[%s], expected %s.ConcurrencyExpectation, create %s, update %s, projection ...%s.Projection[%s]) (%s.Row[%s], error) { return %s.%sUpsertVersioned(ctx, client.runtime, %s, target, expected, create, update, projection...) }\n", prefix, goName, contextAlias, golemAlias, modelType, golemAlias, createInput, updateInput, golemAlias, modelType, golemAlias, modelType, runtimeAlias, prefix, descriptor)
		fmt.Fprintf(source, "func (client %s%sClient[P]) Delete(ctx %s.Context, target %s.MutationTarget[%s], expected %s.ExistingVersion, projection ...%s.Projection[%s]) (%s.Row[%s], error) { return %s.%sDeleteVersioned(ctx, client.runtime, %s, target, expected, projection...) }\n", prefix, goName, contextAlias, golemAlias, modelType, golemAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, prefix, descriptor)
		return
	}
	fmt.Fprintf(source, "func (client %s%sClient[P]) Update(ctx %s.Context, target %s.MutationTarget[%s], input %s, projection ...%s.Projection[%s]) (%s.Row[%s], error) { return %s.%sUpdate(ctx, client.runtime, %s, target, input, projection...) }\n", prefix, goName, contextAlias, golemAlias, modelType, updateInput, golemAlias, modelType, golemAlias, modelType, runtimeAlias, prefix, descriptor)
	fmt.Fprintf(source, "func (client %s%sClient[P]) Upsert(ctx %s.Context, target %s.MutationTarget[%s], create %s, update %s, projection ...%s.Projection[%s]) (%s.Row[%s], error) { return %s.%sUpsert(ctx, client.runtime, %s, target, create, update, projection...) }\n", prefix, goName, contextAlias, golemAlias, modelType, createInput, updateInput, golemAlias, modelType, golemAlias, modelType, runtimeAlias, prefix, descriptor)
	fmt.Fprintf(source, "func (client %s%sClient[P]) Delete(ctx %s.Context, target %s.MutationTarget[%s], projection ...%s.Projection[%s]) (%s.Row[%s], error) { return %s.%sDelete(ctx, client.runtime, %s, target, projection...) }\n", prefix, goName, contextAlias, golemAlias, modelType, golemAlias, modelType, golemAlias, modelType, runtimeAlias, prefix, descriptor)
	fmt.Fprintf(source, "func (client %s%sClient[P]) UpdateMany(ctx %s.Context, where %s.Predicate[%s], input %s) (int64, error) { return %s.%sUpdateMany(ctx, client.runtime, %s, where, input) }\n", prefix, goName, contextAlias, golemAlias, modelType, updateManyInput, runtimeAlias, prefix, descriptor)
	fmt.Fprintf(source, "func (client %s%sClient[P]) DeleteMany(ctx %s.Context, where %s.Predicate[%s]) (int64, error) { return %s.%sDeleteMany(ctx, client.runtime, %s, where) }\n", prefix, goName, contextAlias, golemAlias, modelType, runtimeAlias, prefix, descriptor)
}

func hasSubscriptions(contracts map[ir.ModelID]ir.ModelContractIR) bool {
	for _, contract := range contracts {
		if contract.Subscriptions {
			return true
		}
	}
	return false
}

func contractModels(contract ir.ContractIR) map[ir.ModelID]ir.ModelContractIR {
	result := make(map[ir.ModelID]ir.ModelContractIR, len(contract.Models))
	for _, model := range contract.Models {
		result[model.ModelID] = model
	}
	return result
}

func subscriptionPackages(model ir.ModelIR, contract ir.ContractIR) map[string]bool {
	enabled := make(map[ir.ModelID]bool)
	for _, entry := range contract.Models {
		if entry.Subscriptions {
			enabled[entry.ModelID] = true
		}
	}
	result := make(map[string]bool)
	for _, entry := range model.Models {
		if enabled[entry.ID] {
			result[entry.Go.PackagePath] = true
		}
	}
	return result
}
func contractHasRelationDimensions(contract ir.ModelContractIR) bool {
	return contract.Aggregation != nil && len(contract.Aggregation.RelationDimensions) > 0
}
func emitAnalyticsClientMethods(source *bytes.Buffer, prefix, goName, modelType, contextAlias, golemAlias, runtimeAlias, descriptor string, relation bool) {
	fmt.Fprintf(source, "func (client %s%sClient[P]) Aggregate(ctx %s.Context, request %s.AggregateRequest[%s]) (%s.AggregateResult[%s], error) { return %s.%sAggregate(ctx, client.runtime, %s, request) }\n", prefix, goName, contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, prefix, descriptor)
	fmt.Fprintf(source, "func (client %s%sClient[P]) GroupBy(ctx %s.Context, request %s.GroupRequest[%s]) ([]%s.GroupRow[%s], error) { return %s.%sGroupBy(ctx, client.runtime, %s, request) }\n", prefix, goName, contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, prefix, descriptor)
	if relation {
		fmt.Fprintf(source, "func (client %s%sClient[P]) RelationGroupBy(ctx %s.Context, request %s.RelationGroupRequest[%s]) ([]%s.RelationGroupRow[%s], error) { return %s.%sRelationGroupBy(ctx, client.runtime, %s, request) }\n", prefix, goName, contextAlias, golemAlias, modelType, golemAlias, modelType, runtimeAlias, prefix, descriptor)
	}
}

func emitScopedClientMethod(source *bytes.Buffer, prefix, goName, modelType, contextAlias, golemAlias, runtimeAlias, descriptor string) {
	fmt.Fprintf(source, "func (client %s%sClient[P]) Scoped(ctx %s.Context, query %s.ScopedQuery[%s]) ([]%s.ScopedRow, error) { return %s.%sScoped(ctx, client.runtime, %s, query) }\n", prefix, goName, contextAlias, golemAlias, modelType, golemAlias, runtimeAlias, prefix, descriptor)
}

func pluralName(name string) string {
	if strings.HasSuffix(name, "y") && len(name) > 1 && !strings.ContainsRune("aeiouAEIOU", rune(name[len(name)-2])) {
		return name[:len(name)-1] + "ies"
	}
	for _, suffix := range []string{"s", "x", "z", "ch", "sh"} {
		if strings.HasSuffix(strings.ToLower(name), suffix) {
			return name + "es"
		}
	}
	return name + "s"
}

func validateSemanticMethodNames(indexes map[ir.ModelID][]semanticcontract.Index) error {
	for modelID := range indexes {
		methods := make(map[string]string)
		for _, index := range indexes[modelID] {
			if _, ok := semanticcontract.ExportedIndexName(index.Name); !ok {
				return fmt.Errorf("registry codegen: semantic index name %q cannot form a Go method", index.Name)
			}
			method := semanticSearchMethodName(index.Name)
			if previous, collision := methods[method]; collision && previous != index.Name {
				return fmt.Errorf("registry codegen: semantic index names %q and %q collide in Go", previous, index.Name)
			}
			methods[method] = index.Name
		}
	}
	return nil
}

func semanticSearchMethodName(name string) string {
	exported, _ := semanticcontract.ExportedIndexName(name)
	return "Search" + exported
}

type preparedDocument struct {
	formatVersion, canonicalVersion uint32
	fingerprint                     [32]byte
	payload                         []byte
}

type preparedProvider struct {
	provider          ir.Provider
	systemFingerprint [32]byte
	document          preparedDocument
	migrationManifest []byte
}

type preparedBundle struct {
	generationDigest [32]byte
	model, contract  preparedDocument
	providers        []preparedProvider
}

func prepareSchemaBundle(request Request) (preparedBundle, error) {
	if request.GeneratorVersion == "" || request.TemplateABIVersion == "" {
		return preparedBundle{}, fmt.Errorf("registry codegen: schema bundle requires generator and template ABI versions")
	}
	generationDigest, err := parseDigest(request.GenerationDigest)
	if err != nil {
		return preparedBundle{}, fmt.Errorf("registry codegen: generation digest: %w", err)
	}
	modelBytes, err := ir.CanonicalModel(request.Schema.Model)
	if err != nil {
		return preparedBundle{}, fmt.Errorf("registry codegen: canonical model bundle: %w", err)
	}
	actualModelFingerprint, err := ir.ModelFingerprint(request.Schema.Model)
	if err != nil {
		return preparedBundle{}, err
	}
	if actualModelFingerprint != request.Schema.ModelFingerprint {
		return preparedBundle{}, fmt.Errorf("registry codegen: model blob/fingerprint mismatch")
	}
	modelFingerprint, err := parseDigest(string(request.Schema.ModelFingerprint))
	if err != nil {
		return preparedBundle{}, fmt.Errorf("registry codegen: model fingerprint: %w", err)
	}
	contractBytes, err := ir.CanonicalContract(request.Schema.Contract)
	if err != nil {
		return preparedBundle{}, fmt.Errorf("registry codegen: canonical contract bundle: %w", err)
	}
	actualContractFingerprint, err := ir.ContractFingerprint(request.Schema.Contract)
	if err != nil {
		return preparedBundle{}, err
	}
	if actualContractFingerprint != request.Schema.ContractFingerprint {
		return preparedBundle{}, fmt.Errorf("registry codegen: contract blob/fingerprint mismatch")
	}
	contractFingerprint, err := parseDigest(string(request.Schema.ContractFingerprint))
	if err != nil {
		return preparedBundle{}, fmt.Errorf("registry codegen: contract fingerprint: %w", err)
	}
	result := preparedBundle{
		generationDigest: generationDigest,
		model:            preparedDocument{formatVersion: uint32(request.Schema.Model.FormatVersion), canonicalVersion: uint32(ir.CanonicalFormatVersion), fingerprint: modelFingerprint, payload: modelBytes},
		contract:         preparedDocument{formatVersion: uint32(request.Schema.Contract.FormatVersion), canonicalVersion: uint32(ir.CanonicalFormatVersion), fingerprint: contractFingerprint, payload: contractBytes},
	}
	providers := append([]ProviderInput(nil), request.Schema.Providers...)
	sort.Slice(providers, func(i, j int) bool {
		return providers[i].Schema.Provider.Provider < providers[j].Schema.Provider.Provider
	})
	declared := make(map[ir.Provider]bool, len(request.Schema.Model.Providers))
	for _, provider := range request.Schema.Model.Providers {
		if declared[provider] {
			return preparedBundle{}, fmt.Errorf("registry codegen: model declares provider %s more than once", provider)
		}
		declared[provider] = true
	}
	seen := make(map[ir.Provider]bool, len(providers))
	for _, provider := range providers {
		normalized, normalizeErr := physical.Normalize(provider.Schema)
		if normalizeErr != nil {
			return preparedBundle{}, fmt.Errorf("registry codegen: normalize provider bundle: %w", normalizeErr)
		}
		identity := normalized.Provider.Provider
		if identity == "" || seen[identity] {
			return preparedBundle{}, fmt.Errorf("registry codegen: empty or duplicate provider bundle %q", identity)
		}
		if !declared[identity] {
			return preparedBundle{}, fmt.Errorf("registry codegen: provider bundle %s is not declared by ModelIR", identity)
		}
		seen[identity] = true
		actual, fingerprintErr := physical.PhysicalFingerprint(normalized)
		if fingerprintErr != nil {
			return preparedBundle{}, fingerprintErr
		}
		if actual.String() != string(provider.Fingerprint) {
			return preparedBundle{}, fmt.Errorf("registry codegen: provider %s blob/fingerprint mismatch", identity)
		}
		fingerprint, parseErr := parseDigest(string(provider.Fingerprint))
		if parseErr != nil {
			return preparedBundle{}, fmt.Errorf("registry codegen: provider %s fingerprint: %w", identity, parseErr)
		}
		actualSystem, systemErr := physical.SystemFingerprint(normalized.Provider, normalized.System)
		if systemErr != nil {
			return preparedBundle{}, fmt.Errorf("registry codegen: fingerprint provider %s system schema: %w", identity, systemErr)
		}
		if actualSystem.String() != string(provider.SystemFingerprint) {
			return preparedBundle{}, fmt.Errorf("registry codegen: provider %s system blob/fingerprint mismatch", identity)
		}
		systemFingerprint, systemParseErr := parseDigest(string(provider.SystemFingerprint))
		if systemParseErr != nil {
			return preparedBundle{}, fmt.Errorf("registry codegen: provider %s system fingerprint: %w", identity, systemParseErr)
		}
		payload, encodeErr := physical.CanonicalEncode(normalized)
		if encodeErr != nil {
			return preparedBundle{}, fmt.Errorf("registry codegen: encode provider %s bundle: %w", identity, encodeErr)
		}
		migrationBytes := append([]byte(nil), provider.MigrationManifest...)
		if len(migrationBytes) != 0 {
			manifest, migrationErr := migration.ParseManifest(migrationBytes)
			if migrationErr != nil {
				return preparedBundle{}, fmt.Errorf("registry codegen: decode provider %s migration manifest: %w", identity, migrationErr)
			}
			if migrationErr = migration.VerifyEmbeddedManifest(manifest); migrationErr != nil {
				return preparedBundle{}, fmt.Errorf("registry codegen: verify provider %s migration manifest: %w", identity, migrationErr)
			}
			if len(manifest.Entries) == 0 || manifest.Provider.Provider != identity || !reflect.DeepEqual(manifest.Provider, normalized.Provider) || manifest.Entries[len(manifest.Entries)-1].AfterPhysical != migration.Digest(actual.String()) {
				return preparedBundle{}, fmt.Errorf("registry codegen: provider %s migration manifest does not match its generated schema", identity)
			}
		}
		result.providers = append(result.providers, preparedProvider{provider: identity, systemFingerprint: systemFingerprint, document: preparedDocument{formatVersion: normalized.Version, canonicalVersion: normalized.CanonicalVersion, fingerprint: fingerprint, payload: payload}, migrationManifest: migrationBytes})
	}
	if len(seen) != len(declared) {
		var missing []string
		for provider := range declared {
			if !seen[provider] {
				missing = append(missing, string(provider))
			}
		}
		sort.Strings(missing)
		return preparedBundle{}, fmt.Errorf("registry codegen: missing declared provider bundles: %s", strings.Join(missing, ", "))
	}
	return result, nil
}

func parseDigest(value string) ([32]byte, error) {
	var result [32]byte
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) || hex.EncodeToString(decoded) != value {
		return result, fmt.Errorf("%q is not a canonical SHA-256 digest", value)
	}
	copy(result[:], decoded)
	return result, nil
}

func digestLiteral(golem string, digest [32]byte) string {
	values := make([]string, len(digest))
	for index, value := range digest {
		values[index] = fmt.Sprintf("0x%02x", value)
	}
	return golem + ".SchemaDigest{" + strings.Join(values, ", ") + "}"
}

func documentLiteral(golem string, document preparedDocument) string {
	return fmt.Sprintf("%s.GeneratedSchemaDocument(%d, %d, %s, []byte(%q))", golem, document.formatVersion, document.canonicalVersion, digestLiteral(golem, document.fingerprint), string(document.payload))
}

func providerLiteral(golem string, provider ir.Provider) string {
	switch provider {
	case ir.SQLite:
		return golem + ".SQLite"
	case ir.PostgreSQL:
		return golem + ".PostgreSQL"
	default:
		return fmt.Sprintf("%s.Provider(%q)", golem, provider)
	}
}

type importSet struct {
	current string
	aliases map[string]string
	used    map[string]bool
}

func newImports(current string) *importSet {
	return &importSet{current: current, aliases: map[string]string{}, used: map[string]bool{}}
}
func (set *importSet) qualify(path, preferred string) string {
	if path == set.current {
		return ""
	}
	if alias := set.aliases[path]; alias != "" {
		return alias
	}
	alias := preferred
	for suffix := 2; set.used[alias]; suffix++ {
		alias = fmt.Sprintf("%s%d", preferred, suffix)
	}
	set.aliases[path], set.used[alias] = alias, true
	return alias
}
func (set *importSet) write(buffer *bytes.Buffer) {
	paths := make([]string, 0, len(set.aliases))
	for path := range set.aliases {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	buffer.WriteString("import (\n")
	for _, path := range paths {
		fmt.Fprintf(buffer, "\t%s %q\n", set.aliases[path], path)
	}
	buffer.WriteString(")\n\n")
}
