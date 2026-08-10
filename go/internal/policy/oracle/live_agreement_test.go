package oracle

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/eleven-am/golem/go/internal/provider/postgresql"
	"github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

func TestSQLiteProviderAgreementLive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	corpus := SocialCorpus()
	provider := sqlite.New()
	database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "p2-oracle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	resolver := newLiveResolver(corpus, ir.ProviderSQLite, "main")
	proof, err := provider.PolicyCapabilityProof(ctx, database, resolver.SchemaFingerprint())
	if err != nil {
		t.Fatal(err)
	}
	engine := &liveEngine{
		name:        "sqlite",
		provider:    ir.ProviderSQLite,
		database:    database,
		dialect:     sqlite.NewPolicyDialect(),
		resolver:    resolver,
		proof:       proof,
		namespace:   "main",
		placeholder: func(int) string { return "?" },
	}
	if err := engine.createAndSeed(ctx, corpus); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, corpus, engine); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteProviderMigratedSchemaPolicyProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	provider := sqlite.New()
	database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "p2-migrated.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	runMigratedSchemaPolicyProof(t, ctx, provider, database, sqlite.NewPolicyDialect(), ir.ProviderSQLite, "")
}

// Local runs skip explicitly unless both profiles are configured. The P2
// completion/CI profile supplies both DSNs, so every setup, agreement, and
// collation failure remains a hard test failure there.
func TestPostgreSQLProviderAgreementLiveProfiles(t *testing.T) {
	if strings.TrimSpace(os.Getenv(PostgreSQLDSNEnv)) == "" || strings.TrimSpace(os.Getenv(PostgreSQLLinguisticDSNEnv)) == "" {
		t.Skipf("local PostgreSQL agreement requires both %s and %s; the completion profile must provide them", PostgreSQLDSNEnv, PostgreSQLLinguisticDSNEnv)
	}
	profiles, err := PostgreSQLProfiles()
	if err != nil {
		t.Fatal(err)
	}
	corpus := SocialCorpus()
	records := make(map[string]map[string]SQLResult, len(profiles))
	collations := make(map[string]postgresCollationObservation, len(profiles))
	for _, profile := range profiles {
		profile := profile
		t.Run(profile.Name, func(t *testing.T) {
			ctx := context.Background()
			provider := postgresql.New()
			database, _, err := provider.Open(ctx, profile.DSN)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			migrationNamespace := uniqueNamespace(t) + "_migrated"
			if _, err := database.ExecContext(ctx, `DROP SCHEMA IF EXISTS "_golem" CASCADE`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), "DROP SCHEMA "+quoteIdentifier(migrationNamespace)+" CASCADE")
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "_golem" CASCADE`)
			})
			runMigratedSchemaPolicyProof(t, ctx, provider, database, postgresql.NewPolicyDialect(), ir.ProviderPostgreSQL, migrationNamespace)
			observation, err := verifyPostgreSQLCollationProfile(ctx, database, profile)
			if err != nil {
				t.Fatal(err)
			}
			collations[profile.Name] = observation
			namespace := uniqueNamespace(t)
			if _, err := database.ExecContext(ctx, "CREATE SCHEMA "+quoteIdentifier(namespace)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), "DROP SCHEMA "+quoteIdentifier(namespace)+" CASCADE")
			})
			resolver := newLiveResolver(corpus, ir.ProviderPostgreSQL, namespace)
			proof, err := provider.PolicyCapabilityProof(ctx, database, resolver.SchemaFingerprint())
			if err != nil {
				t.Fatal(err)
			}
			engine := &liveEngine{
				name:        "postgresql/" + profile.Name,
				provider:    ir.ProviderPostgreSQL,
				database:    database,
				dialect:     postgresql.NewPolicyDialect(),
				resolver:    resolver,
				proof:       proof,
				namespace:   namespace,
				placeholder: func(position int) string { return fmt.Sprintf("$%d", position) },
			}
			if err := engine.createAndSeed(ctx, corpus); err != nil {
				t.Fatal(err)
			}
			recorder := &recordingEngine{Engine: engine, results: make(map[string]SQLResult)}
			if err := Run(ctx, corpus, recorder); err != nil {
				t.Fatal(err)
			}
			records[profile.Name] = recorder.results
		})
	}
	if !reflect.DeepEqual(records[profiles[0].Name], records[profiles[1].Name]) {
		t.Fatal("PostgreSQL C-default and linguistic profiles produced different agreement records")
	}
	left, right := collations[profiles[0].Name], collations[profiles[1].Name]
	if left.forced != right.forced {
		t.Fatal("explicit COLLATE C controls differ between PostgreSQL profiles")
	}
	if left.unforced == right.unforced {
		t.Fatal("unguarded collation control did not distinguish C-default and linguistic PostgreSQL profiles")
	}
}

type migrationPolicyProvider interface {
	Lower(context.Context, compilerir.ModelIR, physical.LowerOptions) (physical.PhysicalSchema, error)
	ApplyInitial(context.Context, *sqlx.DB, physical.PhysicalSchema) error
	Verify(context.Context, *sqlx.DB, physical.PhysicalSchema) error
	PolicyCapabilityProof(context.Context, *sqlx.DB, [32]byte) (policysql.CapabilityProof, error)
}

func runMigratedSchemaPolicyProof(t *testing.T, ctx context.Context, provider migrationPolicyProvider, database *sqlx.DB, dialect policysql.Dialect, policyProvider ir.Provider, namespace string) {
	t.Helper()
	corpus := SocialCorpus()
	model := migratedUserModelIR()
	options := physical.LowerOptions{}
	resolverNamespace := "main"
	if policyProvider == ir.ProviderPostgreSQL {
		options.Namespace = physical.PhysicalName(namespace)
		resolverNamespace = namespace
	}
	schema, err := provider.Lower(ctx, model, options)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.ApplyInitial(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	if err := provider.Verify(ctx, database, schema); err != nil {
		t.Fatal(err)
	}
	resolver := newLiveResolver(corpus, policyProvider, resolverNamespace)
	ids := socialIDsValue
	user := resolver.models[ids.user]
	user.Table = "migrated_users"
	resolver.models[ids.user] = user
	proof, err := provider.PolicyCapabilityProof(ctx, database, resolver.SchemaFingerprint())
	if err != nil {
		t.Fatal(err)
	}
	condition := scalarCondition(ids.user, ids.userName, socialTypes().text, ir.OperatorEqual, ir.ComparisonSensitive, oneOperand(stringValue("Ada")))
	fragment, err := policysql.Compile(policysql.Request{Condition: condition, Provider: policyProvider, Resolver: resolver, Dialect: dialect, Capabilities: proof, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: "root"})
	if err != nil {
		t.Fatal(err)
	}
	idType := socialTypes().uuid
	nameType := socialTypes().text
	adaID, err := dialect.Encode(policysql.BoundValue{Value: ir.UUIDValue(uuid(81)), Type: idType})
	if err != nil {
		t.Fatal(err)
	}
	bobID, err := dialect.Encode(policysql.BoundValue{Value: ir.UUIDValue(uuid(82)), Type: idType})
	if err != nil {
		t.Fatal(err)
	}
	ada, _ := dialect.Encode(policysql.BoundValue{Value: stringValue("Ada"), Type: nameType})
	bob, _ := dialect.Encode(policysql.BoundValue{Value: stringValue("bob"), Type: nameType})
	modelDescriptor, _ := resolver.Model(policyProvider, ids.user)
	table := dialect.Table(modelDescriptor)
	insert := "INSERT INTO " + table + " (" + dialect.Quote("id") + "," + dialect.Quote("name") + "," + dialect.Quote("score") + ") VALUES (" + dialect.Placeholder(1) + "," + dialect.Placeholder(2) + "," + dialect.Placeholder(3) + "),(" + dialect.Placeholder(4) + "," + dialect.Placeholder(5) + "," + dialect.Placeholder(6) + ")"
	if _, err := database.ExecContext(ctx, insert, adaID, ada, int64(1), bobID, bob, nil); err != nil {
		t.Fatal(err)
	}
	var selected []string
	statement := "SELECT " + dialect.Quote("name") + " FROM " + table + " AS " + dialect.Quote("root") + " WHERE " + fragment.SQL() + " ORDER BY " + dialect.Quote("name")
	if err := database.SelectContext(ctx, &selected, statement, fragment.Args()...); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected, []string{"Ada"}) {
		t.Fatalf("provider-migrated policy selection=%v, want [Ada]", selected)
	}
	var unknown int
	if err := database.GetContext(ctx, &unknown, "SELECT count(*) FROM "+table+" AS "+dialect.Quote("root")+" WHERE ("+fragment.SQL()+") IS NULL", fragment.Args()...); err != nil {
		t.Fatal(err)
	}
	if unknown != 0 {
		t.Fatalf("provider-migrated policy unknown count=%d", unknown)
	}
}

func migratedUserModelIR() compilerir.ModelIR {
	ids := socialIDsValue
	modelID := compilerir.ModelID(hex.EncodeToString(ids.user[:]))
	idField := compilerir.FieldID(hex.EncodeToString(ids.userID[:]))
	nameField := compilerir.FieldID(hex.EncodeToString(ids.userName[:]))
	scoreField := compilerir.FieldID(hex.EncodeToString(ids.userScore[:]))
	return compilerir.ModelIR{
		FormatVersion: compilerir.ModelFormatVersion,
		Schema:        compilerir.SchemaIdentityIR{ID: compilerir.SchemaID("91000000000000000000000000000000"), StableName: "p2_oracle_migrated"},
		Providers:     []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL},
		Models: []compilerir.ModelDeclIR{{
			ID: modelID, LogicalName: "MigratedUser", Table: compilerir.TableBindingIR{PhysicalName: "migrated_users"},
			Fields: []compilerir.FieldIR{
				migratedScalarField(idField, "ID", "id", 0, compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}, false),
				migratedScalarField(nameField, "Name", "name", 1, compilerir.LogicalTypeIR{Kind: compilerir.TypeString}, false),
				migratedScalarField(scoreField, "Score", "score", 2, compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}, true),
			},
			PrimaryKey: &compilerir.KeyIR{ID: compilerir.KeyID("92000000000000000000000000000000"), Kind: compilerir.KeyPrimary, PhysicalName: "pk_migrated_users", Fields: []compilerir.FieldID{idField}},
		}},
	}
}

func migratedScalarField(id compilerir.FieldID, name string, column compilerir.SQLIdentifier, order uint32, typ compilerir.LogicalTypeIR, nullable bool) compilerir.FieldIR {
	return compilerir.FieldIR{ID: id, GoName: name, LogicalName: name, DeclarationOrder: order, Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: column, Type: typ, Nullable: nullable}}
}

type recordingEngine struct {
	Engine
	results map[string]SQLResult
}

func (engine *recordingEngine) Run(ctx context.Context, corpus Corpus, probe Probe) (SQLResult, error) {
	result, err := engine.Engine.Run(ctx, corpus, probe)
	if err == nil {
		engine.results[probe.Name()] = result
	}
	return result, err
}

type liveFieldKey struct {
	model ir.ModelID
	field ir.FieldID
}

type liveRelationKey struct {
	model    ir.ModelID
	field    ir.FieldID
	relation ir.RelationID
}

type liveResolver struct {
	provider    ir.Provider
	namespace   physical.PhysicalName
	fingerprint [32]byte
	providers   ir.ProviderSet
	models      map[ir.ModelID]policysql.Model
	modelSpecs  map[ir.ModelID]ModelSpec
	fields      map[liveFieldKey]policysql.Field
	fieldSpecs  map[liveFieldKey]FieldSpec
	relations   map[liveRelationKey]policysql.Relation
}

func newLiveResolver(corpus Corpus, provider ir.Provider, namespace string) *liveResolver {
	resolver := &liveResolver{
		provider:    provider,
		namespace:   physical.PhysicalName(namespace),
		fingerprint: sha256.Sum256([]byte(fmt.Sprintf("golem:p2:oracle:social:v1:%d", corpus.Seed()))),
		providers:   ir.PortableProviders(),
		models:      make(map[ir.ModelID]policysql.Model),
		modelSpecs:  make(map[ir.ModelID]ModelSpec),
		fields:      make(map[liveFieldKey]policysql.Field),
		fieldSpecs:  make(map[liveFieldKey]FieldSpec),
		relations:   make(map[liveRelationKey]policysql.Relation),
	}
	for _, model := range corpus.Models() {
		resolver.models[model.ID()] = policysql.Model{ID: model.ID(), Namespace: resolver.namespace, Table: physical.PhysicalName(model.Table())}
		resolver.modelSpecs[model.ID()] = model
	}
	for _, field := range corpus.Fields() {
		key := liveFieldKey{model: field.ModelID(), field: field.ID()}
		resolver.fields[key] = policysql.Field{Model: field.ModelID(), ID: field.ID(), Column: physical.PhysicalName(field.Column()), Type: field.Type(), Nullable: field.Nullable()}
		resolver.fieldSpecs[key] = field
	}
	for _, relation := range corpus.Relations() {
		pairs := make([]policysql.Correlation, len(relation.Correlation()))
		for index, pair := range relation.Correlation() {
			pairs[index] = policysql.Correlation{Parent: pair.ParentFieldID(), Child: pair.ChildFieldID()}
		}
		key := liveRelationKey{model: relation.ModelID(), field: relation.FieldID(), relation: relation.ID()}
		resolver.relations[key] = policysql.Relation{Model: relation.ModelID(), Field: relation.FieldID(), ID: relation.ID(), Target: relation.TargetModelID(), Cardinality: relation.Cardinality(), Pairs: pairs}
	}
	return resolver
}

func (resolver *liveResolver) Providers() ir.ProviderSet   { return resolver.providers }
func (resolver *liveResolver) SchemaFingerprint() [32]byte { return resolver.fingerprint }
func (resolver *liveResolver) EnumWire(enum ir.EnumID, value ir.EnumValueID) (string, bool) {
	return scalarMatrixEnumWire(enum, value)
}
func (resolver *liveResolver) Capability(provider ir.Provider, capability ir.Capability) bool {
	return provider == resolver.provider && capability >= ir.CapabilityBinaryText && capability <= ir.CapabilityRelationCorrelation
}
func (resolver *liveResolver) Model(provider ir.Provider, model ir.ModelID) (policysql.Model, bool) {
	if provider != resolver.provider {
		return policysql.Model{}, false
	}
	value, ok := resolver.models[model]
	return value, ok
}
func (resolver *liveResolver) Field(provider ir.Provider, model ir.ModelID, field ir.FieldID) (policysql.Field, bool) {
	if provider != resolver.provider {
		return policysql.Field{}, false
	}
	value, ok := resolver.fields[liveFieldKey{model: model, field: field}]
	return value, ok
}
func (resolver *liveResolver) Relation(model ir.ModelID, field ir.FieldID, relation ir.RelationID) (policysql.Relation, bool) {
	value, ok := resolver.relations[liveRelationKey{model: model, field: field, relation: relation}]
	return value, ok
}

var _ policysql.Resolver = (*liveResolver)(nil)

type liveEngine struct {
	name        string
	provider    ir.Provider
	database    *sqlx.DB
	dialect     policysql.Dialect
	resolver    *liveResolver
	proof       policysql.CapabilityProof
	namespace   string
	placeholder func(int) string
}

func (engine *liveEngine) Name() string { return engine.name }

func (engine *liveEngine) Control(ctx context.Context, _ Corpus) (ControlResult, error) {
	from := engine.table("users") + " AS " + quoteIdentifier("root")
	argument := engine.placeholder(1)
	predicate := quoteIdentifier("root") + "." + quoteIdentifier("score") + " = " + argument
	args := []any{int64(2)}
	var unknown uint64
	if err := engine.database.GetContext(ctx, &unknown, "SELECT count(*) FROM "+from+" WHERE ("+predicate+") IS NULL", args...); err != nil {
		return ControlResult{}, err
	}
	isNotTrue, err := engine.identities(ctx, "SELECT "+quoteIdentifier("oracle_identity")+" FROM "+from+" WHERE ("+predicate+") IS NOT TRUE ORDER BY "+quoteIdentifier("oracle_identity"), args)
	if err != nil {
		return ControlResult{}, err
	}
	negated, err := engine.identities(ctx, "SELECT "+quoteIdentifier("oracle_identity")+" FROM "+from+" WHERE NOT ("+predicate+") ORDER BY "+quoteIdentifier("oracle_identity"), args)
	if err != nil {
		return ControlResult{}, err
	}
	return ControlResult{UnknownCount: unknown, IsNotTrue: isNotTrue, Negated: negated}, nil
}

func (engine *liveEngine) Run(ctx context.Context, _ Corpus, probe Probe) (SQLResult, error) {
	fragment, err := policysql.Compile(policysql.Request{
		Condition:        probe.Condition(),
		Provider:         engine.provider,
		Resolver:         engine.resolver,
		Dialect:          engine.dialect,
		Capabilities:     engine.proof,
		BoundFingerprint: engine.resolver.SchemaFingerprint(),
		RootAlias:        "root",
	})
	if err != nil {
		return SQLResult{}, err
	}
	model, ok := engine.resolver.Model(engine.provider, probe.Condition().ModelID())
	if !ok {
		return SQLResult{}, fmt.Errorf("live oracle: root model is unavailable")
	}
	from := engine.dialect.Table(model) + " AS " + engine.dialect.Quote("root")
	identity := engine.dialect.Quote("oracle_identity")
	selected, err := engine.identities(ctx, "SELECT "+identity+" FROM "+from+" WHERE "+fragment.SQL()+" ORDER BY "+identity, fragment.Args())
	if err != nil {
		return SQLResult{}, fmt.Errorf("selected query: %w; SQL=%s args=%#v", err, fragment.SQL(), fragment.Args())
	}
	var unknown uint64
	if err := engine.database.GetContext(ctx, &unknown, "SELECT count(*) FROM "+from+" WHERE ("+fragment.SQL()+") IS NULL", fragment.Args()...); err != nil {
		return SQLResult{}, fmt.Errorf("unknown query: %w; SQL=%s args=%#v", err, fragment.SQL(), fragment.Args())
	}
	isNotTrue, err := engine.identities(ctx, "SELECT "+identity+" FROM "+from+" WHERE ("+fragment.SQL()+") IS NOT TRUE ORDER BY "+identity, fragment.Args())
	if err != nil {
		return SQLResult{}, err
	}
	negated, err := engine.identities(ctx, "SELECT "+identity+" FROM "+from+" WHERE NOT ("+fragment.SQL()+") ORDER BY "+identity, fragment.Args())
	if err != nil {
		return SQLResult{}, err
	}
	return SQLResult{Selected: selected, UnknownCount: unknown, IsNotTrue: isNotTrue, Negated: negated, SelectionStatements: 1}, nil
}

func (engine *liveEngine) identities(ctx context.Context, statement string, args []any) ([]Identity, error) {
	var raw []string
	if err := engine.database.SelectContext(ctx, &raw, statement, args...); err != nil {
		return nil, err
	}
	result := make([]Identity, len(raw))
	for index := range raw {
		result[index] = Identity(raw[index])
	}
	return result, nil
}

func (engine *liveEngine) createAndSeed(ctx context.Context, corpus Corpus) error {
	for _, statement := range engine.schemaStatements() {
		if _, err := engine.database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("live oracle schema: %w; SQL=%s", err, statement)
		}
	}
	for _, row := range corpus.Rows() {
		if err := engine.insertRow(ctx, row); err != nil {
			return err
		}
	}
	return nil
}

func (engine *liveEngine) insertRow(ctx context.Context, row Row) error {
	model := engine.resolver.modelSpecs[row.ModelID()]
	table := model.Table()
	if model.Name() == "Post" {
		table = "posts_normal"
		if row.Scope() == SeedDanglingRelation {
			table = "posts_drift"
		}
	}
	columns := []string{quoteIdentifier("oracle_identity")}
	args := []any{string(row.Identity())}
	for _, cell := range row.Cells() {
		field, ok := engine.resolver.fieldSpecs[liveFieldKey{model: row.ModelID(), field: cell.FieldID()}]
		if !ok {
			return fmt.Errorf("live oracle seed %q: field descriptor is missing", row.Identity())
		}
		columns = append(columns, quoteIdentifier(field.Column()))
		switch cell.Kind() {
		case CellNull:
			args = append(args, nil)
		case CellValue:
			value, _ := cell.Value()
			bound := policysql.BoundValue{Value: value, Type: field.Type()}
			if field.Type().Kind() == ir.ValueEnum {
				enum, member, _ := value.Enum()
				wire, found := engine.resolver.EnumWire(enum, member)
				if !found {
					return fmt.Errorf("live oracle seed %q: enum value has no descriptor wire label", row.Identity())
				}
				bound.EnumWires = []string{wire}
			} else if field.Type().Kind() == ir.ValueScalarList {
				element, _ := field.Type().Element()
				if element.Kind() == ir.ValueEnum {
					values, _ := value.List()
					bound.EnumWires = make([]string, len(values))
					for index, item := range values {
						enum, member, _ := item.Enum()
						wire, found := engine.resolver.EnumWire(enum, member)
						if !found {
							return fmt.Errorf("live oracle seed %q: enum-list value has no descriptor wire label", row.Identity())
						}
						bound.EnumWires[index] = wire
					}
				}
			}
			encoded, err := engine.dialect.Encode(bound)
			if err != nil {
				return fmt.Errorf("live oracle seed %q: %w", row.Identity(), err)
			}
			args = append(args, encoded)
		case CellRawJSON:
			raw, _ := cell.RawJSON()
			args = append(args, raw)
		default:
			return fmt.Errorf("live oracle seed %q: unsupported cell kind", row.Identity())
		}
	}
	values := make([]string, len(args))
	for index := range values {
		values[index] = engine.placeholder(index + 1)
	}
	statement := "INSERT INTO " + engine.table(table) + " (" + strings.Join(columns, ",") + ") VALUES (" + strings.Join(values, ",") + ")"
	if _, err := engine.database.ExecContext(ctx, statement, args...); err != nil {
		return fmt.Errorf("live oracle seed %q: %w", row.Identity(), err)
	}
	return nil
}

func (engine *liveEngine) table(name string) string {
	if engine.provider == ir.ProviderPostgreSQL {
		return quoteIdentifier(engine.namespace) + "." + quoteIdentifier(name)
	}
	return quoteIdentifier(name)
}

func (engine *liveEngine) schemaStatements() []string {
	q := engine.table
	if engine.provider == ir.ProviderPostgreSQL {
		statements := []string{
			`CREATE TABLE ` + q("users") + ` (oracle_identity text PRIMARY KEY, id uuid UNIQUE NOT NULL, name text NOT NULL, score bigint, tags jsonb, profile jsonb)`,
			`CREATE TABLE ` + q("posts_normal") + ` (oracle_identity text PRIMARY KEY, id uuid UNIQUE NOT NULL, author_id uuid NOT NULL REFERENCES ` + q("users") + `(id), title text NOT NULL, rating bigint)`,
			`CREATE TABLE ` + q("posts_drift") + ` (oracle_identity text PRIMARY KEY, id uuid UNIQUE NOT NULL, author_id uuid NOT NULL, title text NOT NULL, rating bigint)`,
			`CREATE VIEW ` + q("posts") + ` AS SELECT * FROM ` + q("posts_normal") + ` UNION ALL SELECT * FROM ` + q("posts_drift"),
			`CREATE TABLE ` + q("comments") + ` (oracle_identity text PRIMARY KEY, id uuid UNIQUE NOT NULL, post_id uuid NOT NULL REFERENCES ` + q("posts_normal") + `(id), author_id uuid NOT NULL REFERENCES ` + q("users") + `(id), parent_id uuid REFERENCES ` + q("comments") + `(id), body text NOT NULL)`,
			`CREATE TABLE ` + q("friendships") + ` (oracle_identity text UNIQUE NOT NULL, user_id uuid NOT NULL REFERENCES ` + q("users") + `(id), friend_id uuid NOT NULL REFERENCES ` + q("users") + `(id), PRIMARY KEY (user_id,friend_id))`,
			`CREATE TABLE ` + q("tags") + ` (oracle_identity text PRIMARY KEY, name text UNIQUE NOT NULL)`,
			`CREATE TABLE ` + q("post_tags") + ` (oracle_identity text UNIQUE NOT NULL, post_id uuid NOT NULL, tag_name text NOT NULL REFERENCES ` + q("tags") + `(name), PRIMARY KEY (post_id,tag_name))`,
		}
		return append(statements, engine.matrixSchemaStatements()...)
	}
	statements := []string{
		`CREATE TABLE ` + q("users") + ` (oracle_identity TEXT PRIMARY KEY, id TEXT UNIQUE NOT NULL, name TEXT NOT NULL, score INTEGER, tags TEXT, profile TEXT) STRICT`,
		`CREATE TABLE ` + q("posts_normal") + ` (oracle_identity TEXT PRIMARY KEY, id TEXT UNIQUE NOT NULL, author_id TEXT NOT NULL REFERENCES users(id), title TEXT NOT NULL, rating INTEGER) STRICT`,
		`CREATE TABLE ` + q("posts_drift") + ` (oracle_identity TEXT PRIMARY KEY, id TEXT UNIQUE NOT NULL, author_id TEXT NOT NULL, title TEXT NOT NULL, rating INTEGER) STRICT`,
		`CREATE VIEW ` + q("posts") + ` AS SELECT * FROM ` + q("posts_normal") + ` UNION ALL SELECT * FROM ` + q("posts_drift"),
		`CREATE TABLE ` + q("comments") + ` (oracle_identity TEXT PRIMARY KEY, id TEXT UNIQUE NOT NULL, post_id TEXT NOT NULL REFERENCES posts_normal(id), author_id TEXT NOT NULL REFERENCES users(id), parent_id TEXT REFERENCES comments(id), body TEXT NOT NULL) STRICT`,
		`CREATE TABLE ` + q("friendships") + ` (oracle_identity TEXT UNIQUE NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), friend_id TEXT NOT NULL REFERENCES users(id), PRIMARY KEY (user_id,friend_id)) STRICT`,
		`CREATE TABLE ` + q("tags") + ` (oracle_identity TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL) STRICT`,
		`CREATE TABLE ` + q("post_tags") + ` (oracle_identity TEXT UNIQUE NOT NULL, post_id TEXT NOT NULL, tag_name TEXT NOT NULL REFERENCES tags(name), PRIMARY KEY (post_id,tag_name)) STRICT`,
	}
	return append(statements, engine.matrixSchemaStatements()...)
}

func (engine *liveEngine) matrixSchemaStatements() []string {
	return []string{
		engine.scalarMatrixSchemaStatement(),
		engine.jsonBackedMatrixSchemaStatement(scalarListMatrixTable, scalarListMatrixFixture().fields),
		engine.jsonBackedMatrixSchemaStatement(jsonMatrixTable, jsonMatrixFixture().fields),
	}
}

func (engine *liveEngine) scalarMatrixSchemaStatement() string {
	columns := []string{quoteIdentifier("oracle_identity") + " TEXT PRIMARY KEY"}
	for _, field := range scalarMatrixFixture().fields {
		column := quoteIdentifier(field.Column()) + " " + scalarMatrixSQLType(engine.provider, field.Type())
		if !field.Nullable() {
			column += " NOT NULL"
		}
		columns = append(columns, column)
	}
	statement := "CREATE TABLE " + engine.table(scalarMatrixTable) + " (" + strings.Join(columns, ",") + ")"
	if engine.provider == ir.ProviderSQLite {
		statement += " STRICT"
	}
	return statement
}

func (engine *liveEngine) jsonBackedMatrixSchemaStatement(table string, fields []FieldSpec) string {
	columns := []string{quoteIdentifier("oracle_identity") + " TEXT PRIMARY KEY"}
	storageType := "TEXT"
	if engine.provider == ir.ProviderPostgreSQL {
		storageType = "jsonb"
	}
	for _, field := range fields {
		column := quoteIdentifier(field.Column()) + " " + storageType
		if !field.Nullable() {
			column += " NOT NULL"
		}
		columns = append(columns, column)
	}
	statement := "CREATE TABLE " + engine.table(table) + " (" + strings.Join(columns, ",") + ")"
	if engine.provider == ir.ProviderSQLite {
		statement += " STRICT"
	}
	return statement
}

func scalarMatrixSQLType(provider ir.Provider, typ ir.TypeRef) string {
	if provider == ir.ProviderPostgreSQL {
		switch typ.Kind() {
		case ir.ValueBool:
			return "boolean"
		case ir.ValueInt16:
			return "smallint"
		case ir.ValueInt32:
			return "integer"
		case ir.ValueInt64:
			return "bigint"
		case ir.ValueFloat32:
			return "real"
		case ir.ValueFloat64:
			return "double precision"
		case ir.ValueDecimal:
			return fmt.Sprintf("numeric(%d,%d)", typ.Precision(), typ.Scale())
		case ir.ValueString, ir.ValueEnum:
			return "text"
		case ir.ValueBytes:
			return "bytea"
		case ir.ValueUUID:
			return "uuid"
		case ir.ValueDate:
			return "date"
		case ir.ValueTime:
			return fmt.Sprintf("time(%d) without time zone", typ.Precision())
		case ir.ValueDateTime:
			return fmt.Sprintf("timestamp(%d) with time zone", typ.Precision())
		}
	}
	switch typ.Kind() {
	case ir.ValueBool, ir.ValueInt16, ir.ValueInt32, ir.ValueInt64, ir.ValueDateTime:
		return "INTEGER"
	case ir.ValueFloat32, ir.ValueFloat64:
		return "REAL"
	case ir.ValueBytes:
		return "BLOB"
	default:
		return "TEXT"
	}
}

type postgresCollationObservation struct {
	name     string
	unforced [2]bool
	forced   [2]bool
}

func verifyPostgreSQLCollationProfile(ctx context.Context, database *sqlx.DB, profile PostgreSQLProfile) (postgresCollationObservation, error) {
	var collation string
	var unforcedZA, unforcedAB, forcedZA, forcedAB bool
	statement := `SELECT d.datcollate, ('Z' < 'a'), ('a' < 'B'), ('Z' COLLATE "C" < 'a' COLLATE "C"), ('a' COLLATE "C" < 'B' COLLATE "C") FROM pg_catalog.pg_database AS d WHERE d.datname = pg_catalog.current_database()`
	if err := database.QueryRowxContext(ctx, statement).Scan(&collation, &unforcedZA, &unforcedAB, &forcedZA, &forcedAB); err != nil {
		return postgresCollationObservation{}, fmt.Errorf("live oracle collation profile %q: %w", profile.Name, err)
	}
	observation := postgresCollationObservation{name: collation, unforced: [2]bool{unforcedZA, unforcedAB}, forced: [2]bool{forcedZA, forcedAB}}
	isC := isCCollation(collation)
	if profile.Linguistic {
		if isC {
			return postgresCollationObservation{}, fmt.Errorf("live oracle profile %q has C-like lc_collate %q", profile.Name, collation)
		}
		if unforcedZA == forcedZA && unforcedAB == forcedAB {
			return postgresCollationObservation{}, fmt.Errorf("live oracle profile %q did not make the unforced collation control differ from COLLATE C", profile.Name)
		}
		return observation, nil
	}
	if !isC {
		return postgresCollationObservation{}, fmt.Errorf("live oracle profile %q must be C-default; lc_collate=%q", profile.Name, collation)
	}
	if unforcedZA != forcedZA || unforcedAB != forcedAB {
		return postgresCollationObservation{}, fmt.Errorf("live oracle C-default profile %q disagrees with explicit COLLATE C", profile.Name)
	}
	return observation, nil
}

func isCCollation(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return normalized == "c" || normalized == "posix" || normalized == "c.utf8" || normalized == "c.utf-8"
}

func uniqueNamespace(t *testing.T) string {
	t.Helper()
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	return "golem_p2_oracle_" + hex.EncodeToString(suffix[:])
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func TestPostgreSQLProfileClassification(t *testing.T) {
	for value, want := range map[string]bool{"C": true, "POSIX": true, "C.UTF-8": true, "en_US.UTF-8": false, "fr_FR": false} {
		if got := isCCollation(value); got != want {
			t.Errorf("isCCollation(%q)=%v want %v", value, got, want)
		}
	}
}

func TestLiveResolverUsesCompleteCorpusInventory(t *testing.T) {
	corpus := SocialCorpus()
	resolver := newLiveResolver(corpus, ir.ProviderSQLite, "main")
	if len(resolver.models) != len(corpus.Models()) || len(resolver.fields) != len(corpus.Fields()) || len(resolver.relations) != len(corpus.Relations()) {
		t.Fatalf("resolver inventory models/fields/relations=%d/%d/%d corpus=%d/%d/%d", len(resolver.models), len(resolver.fields), len(resolver.relations), len(corpus.Models()), len(corpus.Fields()), len(corpus.Relations()))
	}
	got := make([]string, 0, len(resolver.models))
	for _, model := range resolver.modelSpecs {
		got = append(got, model.Name())
	}
	sort.Strings(got)
	want := []string{"Comment", "Friendship", "JSONMatrix", "Post", "PostTag", "ScalarListMatrix", "ScalarMatrix", "Tag", "User"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolver models=%v want=%v", got, want)
	}
}
