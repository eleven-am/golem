// Package p7verify owns the executable, adversarial completion harnesses for
// P7. It deliberately drives the real provider coordinators from independent
// processes instead of replacing crashes with returned errors.
package p7verify

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	eventoutbox "github.com/eleven-am/golem/go/internal/event/outbox"
	"github.com/eleven-am/golem/go/internal/event/provider"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	"github.com/eleven-am/golem/go/internal/gentest"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresqlprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const (
	CrashPostgresDSN           = "GOLEM_TEST_POSTGRES_DSN"
	CrashPostgresLinguisticDSN = "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"
	childDSNEnvironment        = "GOLEM_P7_CRASH_CHILD_DSN"
	crashLease                 = 200 * time.Millisecond
)

type CrashConfig struct {
	Executable string
	TempRoot   string
	Env        []string
	Writer     func(CrashEvidence)
}

type CrashEvidence struct {
	Profile       string `json:"profile"`
	Endpoint      string `json:"endpoint"`
	Boundary      string `json:"boundary"`
	KilledPID     int    `json:"killedPid"`
	RestartedPID  int    `json:"restartedPid"`
	AcceptedCount int    `json:"acceptedCount"`
	DuplicateIDs  int    `json:"duplicateIDs"`
	Status        string `json:"status"`
}

type crashProfile struct {
	Name      string
	Provider  string
	DSN       string
	Endpoint  string
	Namespace string
	DBPath    string
}

type CrashChildConfig struct {
	Provider  string
	DBPath    string
	Namespace string
	Mode      string
	Causation string
	ReadyPath string
	LogPath   string
	Result    string
}

type childResult struct {
	PID      int      `json:"pid"`
	Claimed  bool     `json:"claimed"`
	EventIDs []string `json:"eventIDs"`
	Status   string   `json:"status"`
	Facts    int      `json:"facts"`
}

type acceptedNotice struct {
	EventID string `json:"eventID"`
	Encoded []byte `json:"encoded"`
}

type crashResolver struct {
	registry *schema.Registry
	model    golem.ModelID
	event    golem.SchemaDigest
	history  map[golem.SchemaDigest]*schema.Registry
}

func (resolver crashResolver) ResolveFactSchema(reference mutationfact.SchemaReference) (*schema.Registry, golem.SchemaDigest, bool) {
	if resolver.registry == nil {
		return nil, golem.SchemaDigest{}, false
	}
	if reference.FormatVersion == mutationfact.FormatVersionV1 {
		if reference.Generation == resolver.registry.GenerationDigest() {
			return resolver.registry, golem.SchemaDigest{}, true
		}
		historical, ok := resolver.history[reference.Generation]
		return historical, golem.SchemaDigest{}, ok
	}
	if reference.FormatVersion != mutationfact.FormatVersionV2 {
		return nil, golem.SchemaDigest{}, false
	}
	if reference.EventSchema == resolver.event {
		return resolver.registry, resolver.event, true
	}
	return nil, golem.SchemaDigest{}, false
}

func (resolver crashResolver) CanDeliverEventSchema(modelID golem.ModelID, digest golem.EventSchemaDigest) bool {
	return modelID == resolver.model && golem.EventSchemaDigest(resolver.event) == digest
}

var crashBoundaries = []string{
	"before-claim-commit",
	"after-claim",
	"partial-transport-acceptance",
	"accepted-before-ack",
	"after-ack-cleanup",
}

// RunCrashMatrix executes all five durability boundaries on a file-backed
// SQLite database and both required PostgreSQL profiles. Missing PostgreSQL
// configuration is an error, never a skip.
func RunCrashMatrix(ctx context.Context, config CrashConfig) error {
	executable := config.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return fmt.Errorf("P7_CRASH_EXECUTABLE: %w", err)
		}
	}
	root := config.TempRoot
	ownedRoot := false
	if root == "" {
		var err error
		root, err = os.MkdirTemp("", "golem-p7-crash-")
		if err != nil {
			return fmt.Errorf("P7_CRASH_TEMP: %w", err)
		}
		ownedRoot = true
	}
	if ownedRoot {
		defer os.RemoveAll(root)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("P7_CRASH_TEMP: %w", err)
	}
	unique, err := randomHex(6)
	if err != nil {
		return err
	}
	postgresC := envValue(config.Env, CrashPostgresDSN)
	postgresLinguistic := envValue(config.Env, CrashPostgresLinguisticDSN)
	if postgresC == "" || postgresLinguistic == "" {
		missing := []string{}
		if postgresC == "" {
			missing = append(missing, CrashPostgresDSN)
		}
		if postgresLinguistic == "" {
			missing = append(missing, CrashPostgresLinguisticDSN)
		}
		return fmt.Errorf("P7_CRASH_REQUIRED_PROFILE: missing %s", strings.Join(missing, ","))
	}
	profiles := []crashProfile{
		{Name: "sqlite-file", Provider: "sqlite", Endpoint: "file-backed", DBPath: filepath.Join(root, "p7.sqlite")},
		{Name: "postgresql-c", Provider: "postgresql", DSN: postgresC, Endpoint: sanitizedPostgresEndpoint(postgresC), Namespace: "p7crash_" + unique + "c"},
		{Name: "postgresql-linguistic", Provider: "postgresql", DSN: postgresLinguistic, Endpoint: sanitizedPostgresEndpoint(postgresLinguistic), Namespace: "p7crash_" + unique + "l"},
	}
	for _, profile := range profiles {
		if err := runProfile(ctx, executable, root, profile, config.Env, config.Writer); err != nil {
			return fmt.Errorf("%s: %w", profile.Name, err)
		}
	}
	return nil
}

func runProfile(ctx context.Context, executable, root string, profile crashProfile, baseEnv []string, report func(CrashEvidence)) error {
	database, coordinator, cleanup, err := prepareProfile(ctx, profile)
	if err != nil {
		return err
	}
	defer cleanup()
	defer database.Close()
	for index, boundary := range crashBoundaries {
		causation := fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)
		ids := []string{
			fmt.Sprintf("10000000-0000-4000-8000-%012d", index*2+1),
			fmt.Sprintf("10000000-0000-4000-8000-%012d", index*2+2),
		}
		if err := seedCausation(ctx, database, profile, causation, ids); err != nil {
			return fmt.Errorf("%s seed: %w", boundary, err)
		}
		prefix := filepath.Join(root, profile.Name+"-"+strconv.Itoa(index))
		ready := prefix + ".ready"
		acceptance := prefix + ".accepted"
		resultPath := prefix + ".result.json"
		initial := CrashChildConfig{Provider: profile.Provider, DBPath: profile.DBPath, Namespace: profile.Namespace, Mode: boundary, Causation: causation, ReadyPath: ready, LogPath: acceptance}
		killedPID, err := startAndKill(ctx, executable, profile.DSN, initial, ready, baseEnv)
		if err != nil {
			return fmt.Errorf("%s initial process: %w", boundary, err)
		}
		if boundary != "before-claim-commit" && boundary != "after-ack-cleanup" {
			if err := waitLeaseExpiry(ctx); err != nil {
				return err
			}
		}
		recovery := CrashChildConfig{Provider: profile.Provider, DBPath: profile.DBPath, Namespace: profile.Namespace, Mode: "recover-" + boundary, Causation: causation, LogPath: acceptance, Result: resultPath}
		restartedPID, err := runChild(ctx, executable, profile.DSN, recovery, baseEnv)
		if err != nil {
			return fmt.Errorf("%s restart: %w", boundary, err)
		}
		var child childResult
		if err := readJSON(resultPath, &child); err != nil {
			return fmt.Errorf("%s restart result: %w", boundary, err)
		}
		if child.PID != restartedPID {
			return fmt.Errorf("%s restart PID evidence mismatch", boundary)
		}
		accepted, err := readAccepted(acceptance)
		if err != nil {
			return fmt.Errorf("%s acceptance log: %w", boundary, err)
		}
		duplicates, err := verifyBoundary(ctx, coordinator, boundary, causation, ids, child, accepted)
		if err != nil {
			return fmt.Errorf("%s verify: %w", boundary, err)
		}
		if report != nil {
			report(CrashEvidence{Profile: profile.Name, Endpoint: profile.Endpoint, Boundary: boundary, KilledPID: killedPID, RestartedPID: restartedPID, AcceptedCount: len(accepted), DuplicateIDs: duplicates, Status: "PASS"})
		}
	}
	return nil
}

func prepareProfile(ctx context.Context, profile crashProfile) (*sqlx.DB, provider.Coordinator, func(), error) {
	if profile.Provider == "sqlite" {
		database, err := sqlx.Open("sqlite", profile.DBPath)
		if err != nil {
			return nil, nil, func() {}, err
		}
		database.SetMaxOpenConns(8)
		if _, err := database.ExecContext(ctx, sqliteCrashSchema); err != nil {
			database.Close()
			return nil, nil, func() {}, fmt.Errorf("create SQLite crash schema: %w", err)
		}
		coordinator, err := sqliteprovider.New().EventCoordinator(database)
		return database, coordinator, func() {}, err
	}
	database, err := sqlx.ConnectContext(ctx, "pgx", profile.DSN)
	if err != nil {
		return nil, nil, func() {}, fmt.Errorf("connect required PostgreSQL profile: %w", err)
	}
	namespace := quoteIdentifier(profile.Namespace)
	if _, err := database.ExecContext(ctx, `CREATE SCHEMA `+namespace); err != nil {
		database.Close()
		return nil, nil, func() {}, fmt.Errorf("create isolated PostgreSQL crash schema: %w", err)
	}
	if _, err := database.ExecContext(ctx, strings.ReplaceAll(postgresCrashSchema, "$SCHEMA", namespace)); err != nil {
		_, _ = database.ExecContext(context.Background(), `DROP SCHEMA `+namespace+` CASCADE`)
		database.Close()
		return nil, nil, func() {}, fmt.Errorf("create PostgreSQL crash tables: %w", err)
	}
	coordinator, err := postgresqlprovider.New().EventCoordinatorAt(database, physical.PhysicalName(profile.Namespace))
	cleanup := func() { _, _ = database.ExecContext(context.Background(), `DROP SCHEMA `+namespace+` CASCADE`) }
	return database, coordinator, cleanup, err
}

func seedCausation(ctx context.Context, database *sqlx.DB, profile crashProfile, causation string, ids []string) error {
	rows, err := canonicalCrashFacts(causation, ids)
	if err != nil {
		return err
	}
	if profile.Provider == "sqlite" {
		for _, row := range rows {
			_, err := database.ExecContext(ctx, `INSERT INTO "_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","before_identity","after_identity","causation_id","transaction_ordinal","metadata","delete_snapshot","recorded_at") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, row.EventID, row.FactVersion, row.CodecIdentity, row.GenerationFingerprint, row.ModelID, row.Action, row.BeforeIdentity, row.AfterIdentity, row.CausationID, row.TransactionOrdinal, row.Metadata, row.DeleteSnapshot, row.RecordedAt.UnixMicro())
			if err != nil {
				return err
			}
		}
		now := rows[0].RecordedAt.UnixMicro()
		_, err = database.ExecContext(ctx, `INSERT INTO "_golem_outbox_delivery" ("causation_id","status","first_recorded_at","attempt_count","available_at","updated_at") VALUES (?,?,?,?,?,?)`, causation, "pending", now, 0, now, now)
		return err
	}
	outbox := quoteIdentifier(profile.Namespace) + `."_golem_outbox"`
	delivery := quoteIdentifier(profile.Namespace) + `."_golem_outbox_delivery"`
	for _, row := range rows {
		_, err := database.ExecContext(ctx, `INSERT INTO `+outbox+` ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","before_identity","after_identity","causation_id","transaction_ordinal","metadata","delete_snapshot","recorded_at") VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, row.EventID, row.FactVersion, row.CodecIdentity, row.GenerationFingerprint, row.ModelID, row.Action, row.BeforeIdentity, row.AfterIdentity, row.CausationID, row.TransactionOrdinal, row.Metadata, row.DeleteSnapshot, row.RecordedAt)
		if err != nil {
			return err
		}
	}
	_, err = database.ExecContext(ctx, `INSERT INTO `+delivery+` ("causation_id","status","first_recorded_at","attempt_count","available_at","updated_at") VALUES ($1,'pending',$2,0,$2,$2)`, causation, rows[0].RecordedAt)
	return err
}

func canonicalCrashResolver() (crashResolver, error) {
	compilation := gentest.SocialCompilationIR()
	bundle, err := crashSchemaBundle(compilation)
	if err != nil {
		return crashResolver{}, err
	}
	registry, err := schema.New(bundle)
	if err != nil {
		return crashResolver{}, fmt.Errorf("P7_CRASH_SCHEMA: %w", err)
	}
	modelID := golem.ModelID(mustHexDigest16("30000000000000000000000000000000"))
	model, ok := registry.Model(modelID)
	if !ok {
		return crashResolver{}, fmt.Errorf("P7_CRASH_SCHEMA: generated Post is absent")
	}
	fingerprint, _, enabled := model.EventSchema()
	digest, err := mutationfact.ParseEventSchemaFingerprint(fingerprint)
	if !enabled || err != nil {
		return crashResolver{}, fmt.Errorf("P7_CRASH_SCHEMA: generated Post event schema is unavailable")
	}
	historicalGeneration := golem.SchemaDigest{0x78}
	historicalBundle := golem.GeneratedSchemaBundle(
		historicalGeneration, bundle.GeneratorVersion(), bundle.TemplateABIVersion(),
		bundle.Model(), bundle.Contract(), bundle.Providers()...,
	)
	historical, err := schema.New(historicalBundle)
	if err != nil {
		return crashResolver{}, fmt.Errorf("P7_CRASH_SCHEMA: historical registry: %w", err)
	}
	return crashResolver{registry: registry, model: modelID, event: digest, history: map[golem.SchemaDigest]*schema.Registry{historicalGeneration: historical}}, nil
}

func crashSchemaBundle(compilation compilerir.CompilationIR) (golem.SchemaBundle, error) {
	modelPayload, err := compilerir.CanonicalModel(compilation.Model)
	if err != nil {
		return golem.SchemaBundle{}, err
	}
	modelFingerprint, err := compilerir.ModelFingerprint(compilation.Model)
	if err != nil {
		return golem.SchemaBundle{}, err
	}
	contractPayload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		return golem.SchemaBundle{}, err
	}
	contractFingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		return golem.SchemaBundle{}, err
	}
	sqliteSchema, err := sqliteprovider.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{})
	if err != nil {
		return golem.SchemaBundle{}, err
	}
	postgresSchema, err := postgresqlprovider.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{})
	if err != nil {
		return golem.SchemaBundle{}, err
	}
	providerDocument := func(provider golem.Provider, manifest physical.ProviderManifest, value physical.PhysicalSchema) (golem.ProviderSchemaDocument, error) {
		payload, err := physical.CanonicalEncode(value)
		if err != nil {
			return golem.ProviderSchemaDocument{}, err
		}
		fingerprint, err := physical.PhysicalFingerprint(value)
		if err != nil {
			return golem.ProviderSchemaDocument{}, err
		}
		system, err := physical.SystemFingerprint(manifest, value.System)
		if err != nil {
			return golem.ProviderSchemaDocument{}, err
		}
		document := golem.GeneratedSchemaDocument(value.Version, value.CanonicalVersion, golem.SchemaDigest(fingerprint), payload)
		return golem.GeneratedProviderSchemaDocument(provider, golem.SchemaDigest(system), document), nil
	}
	sqliteDocument, err := providerDocument(golem.SQLite, sqliteprovider.New().Manifest(), sqliteSchema)
	if err != nil {
		return golem.SchemaBundle{}, err
	}
	postgresDocument, err := providerDocument(golem.PostgreSQL, postgresqlprovider.New().Manifest(), postgresSchema)
	if err != nil {
		return golem.SchemaBundle{}, err
	}
	modelDigest, err := digestFromHex(string(modelFingerprint))
	if err != nil {
		return golem.SchemaBundle{}, err
	}
	contractDigest, err := digestFromHex(string(contractFingerprint))
	if err != nil {
		return golem.SchemaBundle{}, err
	}
	return golem.GeneratedSchemaBundle(golem.SchemaDigest{0x77}, "p7-crash", "p7-crash-v1",
		golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), modelDigest, modelPayload),
		golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), contractDigest, contractPayload),
		sqliteDocument, postgresDocument), nil
}

func digestFromHex(value string) (golem.SchemaDigest, error) {
	var result golem.SchemaDigest
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(result) {
		return result, fmt.Errorf("P7_CRASH_SCHEMA: invalid fingerprint")
	}
	copy(result[:], decoded)
	return result, nil
}

func mustHexDigest16(value string) [16]byte {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 {
		panic("invalid fixed crash identity")
	}
	var result [16]byte
	copy(result[:], decoded)
	return result
}

func canonicalCrashFacts(causation string, ids []string) ([]mutationfact.OutboxRow, error) {
	resolver, err := canonicalCrashResolver()
	if err != nil {
		return nil, err
	}
	causationID, err := parseUUID16(causation)
	if err != nil {
		return nil, err
	}
	model, _ := resolver.registry.Model(resolver.model)
	primary := model.PrimaryKey()
	if len(primary) != 1 {
		return nil, fmt.Errorf("P7_CRASH_SCHEMA: Post primary identity is not scalar")
	}
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(primary[0])}, nil)
	if err != nil {
		return nil, err
	}
	requirement, err = requirement.WithEventSchema(resolver.event)
	if err != nil {
		return nil, err
	}
	result := make([]mutationfact.OutboxRow, len(ids))
	for index, textID := range ids {
		eventID, err := parseUUID16(textID)
		if err != nil {
			return nil, err
		}
		rowID := [16]byte{0x70, byte(index + 1)}
		row, err := mutationdecode.NewRow(resolver.registry, policyir.ModelID(resolver.model), []mutationdecode.Cell{
			mutationdecode.Value(policyir.FieldID(primary[0]), policyir.UUIDValue(rowID)),
		})
		if err != nil {
			return nil, err
		}
		envelope, err := mutationfact.NewV2(resolver.registry, resolver.event, mutationfact.EventID(eventID), requirement, mutationfact.CausationID(causationID), uint32(index+1), nil, &row)
		if err != nil {
			return nil, err
		}
		stored, err := envelope.OutboxRow(time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond).Add(time.Duration(index) * time.Microsecond))
		if err != nil {
			return nil, err
		}
		result[index] = stored
	}
	return result, nil
}

func parseUUID16(value string) ([16]byte, error) {
	var result [16]byte
	compact := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(compact)
	if err != nil || len(decoded) != len(result) {
		return result, fmt.Errorf("P7_CRASH_UUID: invalid fixture UUID")
	}
	copy(result[:], decoded)
	return result, nil
}

func formatUUID16(value [16]byte) string {
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func startAndKill(ctx context.Context, executable, dsn string, child CrashChildConfig, ready string, baseEnv []string) (int, error) {
	command := childCommand(ctx, executable, dsn, child, baseEnv)
	var output strings.Builder
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			_ = command.Process.Kill()
			_ = command.Wait()
			return 0, err
		}
		select {
		case <-ctx.Done():
			_ = command.Process.Kill()
			_ = command.Wait()
			return 0, ctx.Err()
		case <-deadline.C:
			_ = command.Process.Kill()
			_ = command.Wait()
			return 0, fmt.Errorf("child did not reach crash boundary: %s", bounded(output.String(), 2048))
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := command.Process.Kill(); err != nil {
		return 0, err
	}
	err := command.Wait()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return 0, fmt.Errorf("child was not terminated by kill: %v", err)
	}
	return pid, nil
}

func runChild(ctx context.Context, executable, dsn string, child CrashChildConfig, baseEnv []string) (int, error) {
	command := childCommand(ctx, executable, dsn, child, baseEnv)
	var output strings.Builder
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		return 0, fmt.Errorf("%w: %s", err, bounded(output.String(), 4096))
	}
	var result childResult
	if err := readJSON(child.Result, &result); err != nil {
		return 0, err
	}
	return result.PID, nil
}

func childCommand(ctx context.Context, executable, dsn string, child CrashChildConfig, baseEnv []string) *exec.Cmd {
	arguments := []string{"-child", "-provider", child.Provider, "-mode", child.Mode, "-causation", child.Causation}
	if child.DBPath != "" {
		arguments = append(arguments, "-db", child.DBPath)
	}
	if child.Namespace != "" {
		arguments = append(arguments, "-namespace", child.Namespace)
	}
	if child.ReadyPath != "" {
		arguments = append(arguments, "-ready", child.ReadyPath)
	}
	if child.LogPath != "" {
		arguments = append(arguments, "-acceptance", child.LogPath)
	}
	if child.Result != "" {
		arguments = append(arguments, "-result", child.Result)
	}
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = append(os.Environ(), baseEnv...)
	if dsn != "" {
		command.Env = append(command.Env, childDSNEnvironment+"="+dsn)
	}
	return command
}

// RunCrashChild executes one private child mode. The command wrapper is
// responsible for parsing flags and never printing its DSN.
func RunCrashChild(ctx context.Context, config CrashChildConfig) error {
	database, coordinator, err := openChild(ctx, config)
	if err != nil {
		return err
	}
	defer database.Close()
	if config.Mode == "before-claim-commit" {
		return holdUncommittedClaim(ctx, database, config)
	}
	if strings.HasPrefix(config.Mode, "recover-") {
		return recoverBoundary(ctx, database, coordinator, config)
	}
	return runPublisherToCrashBoundary(ctx, coordinator, config)
}

type crashJournalTransport struct {
	mode      string
	readyPath string
	logPath   string
}

func (transport *crashJournalTransport) Publish(_ context.Context, batch eventvalue.EventBatch) error {
	events := batch.Events()
	switch transport.mode {
	case "after-claim":
		return signalAndHold(transport.readyPath)
	case "partial-transport-acceptance":
		if len(events) == 0 {
			return fmt.Errorf("P7_CRASH_TRANSPORT: causal batch is empty")
		}
		if err := appendAcceptedNotices(transport.logPath, events[:1]); err != nil {
			return err
		}
		return signalAndHold(transport.readyPath)
	case "accepted-before-ack":
		if err := appendAcceptedNotices(transport.logPath, events); err != nil {
			return err
		}
		return signalAndHold(transport.readyPath)
	default:
		return appendAcceptedNotices(transport.logPath, events)
	}
}

func runPublisherToCrashBoundary(ctx context.Context, coordinator provider.Coordinator, config CrashChildConfig) error {
	resolver, err := canonicalCrashResolver()
	if err != nil {
		return err
	}
	transport := &crashJournalTransport{mode: config.Mode, readyPath: config.ReadyPath, logPath: config.LogPath}
	publisher, err := eventoutbox.NewPublisher(coordinator, resolver, transport, eventoutbox.Limits{
		ClaimGroups: 1, Concurrency: 1, LeaseDuration: crashLease, PublishTimeout: 5 * time.Second,
		RetryBase: 10 * time.Millisecond, RetryCap: 100 * time.Millisecond, ShutdownGrace: 50 * time.Millisecond,
	})
	if err != nil {
		return err
	}
	if config.Mode != "after-ack-cleanup" {
		return publisher.Run(ctx)
	}
	workerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- publisher.Run(workerContext) }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		state, inspectErr := coordinator.Inspect(ctx, config.Causation)
		if inspectErr == nil && state.Status == provider.StatusDelivered {
			return signalAndHold(config.ReadyPath)
		}
		select {
		case err := <-done:
			return fmt.Errorf("P7_CRASH_PUBLISHER: stopped before ack: %v", err)
		case <-deadline.C:
			return fmt.Errorf("P7_CRASH_PUBLISHER: acknowledgement was not committed")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func openChild(ctx context.Context, config CrashChildConfig) (*sqlx.DB, provider.Coordinator, error) {
	if config.Provider == "sqlite" {
		database, err := sqlx.Open("sqlite", config.DBPath)
		if err != nil {
			return nil, nil, err
		}
		database.SetMaxOpenConns(2)
		coordinator, err := sqliteprovider.New().EventCoordinator(database)
		return database, coordinator, err
	}
	dsn := os.Getenv(childDSNEnvironment)
	if dsn == "" {
		return nil, nil, fmt.Errorf("P7_CRASH_CHILD: private DSN is absent")
	}
	database, err := sqlx.ConnectContext(ctx, "pgx", dsn)
	if err != nil {
		return nil, nil, err
	}
	coordinator, err := postgresqlprovider.New().EventCoordinatorAt(database, physical.PhysicalName(config.Namespace))
	return database, coordinator, err
}

func holdUncommittedClaim(ctx context.Context, database *sqlx.DB, config CrashChildConfig) error {
	transaction, err := database.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if config.Provider == "sqlite" {
		_ = transaction.Rollback()
		connection, err := database.Connx(ctx)
		if err != nil {
			return err
		}
		defer connection.Close()
		if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
			return err
		}
		defer connection.ExecContext(context.Background(), "ROLLBACK")
		result, err := connection.ExecContext(ctx, `UPDATE "_golem_outbox_delivery" SET "status"='leased',"lease_token"='00000000-0000-4000-8000-000000000999',"lease_until"=`+`CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)`+`+1000000 WHERE "causation_id"=? AND "status"='pending'`, config.Causation)
		if err != nil {
			return err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return fmt.Errorf("P7_CRASH_CHILD: uncommitted SQLite claim did not mutate one state")
		}
		return signalAndHold(config.ReadyPath)
	}
	delivery := quoteIdentifier(config.Namespace) + `."_golem_outbox_delivery"`
	if _, err := transaction.ExecContext(ctx, `SELECT "causation_id" FROM `+delivery+` WHERE "causation_id"=$1 FOR UPDATE SKIP LOCKED`, config.Causation); err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, `UPDATE `+delivery+` SET "status"='leased',"lease_token"='00000000-0000-4000-8000-000000000999',"lease_until"=clock_timestamp()+interval '1 second' WHERE "causation_id"=$1 AND "status"='pending'`, config.Causation)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("P7_CRASH_CHILD: uncommitted PostgreSQL claim did not mutate one state")
	}
	return signalAndHold(config.ReadyPath)
}

func recoverBoundary(ctx context.Context, database *sqlx.DB, coordinator provider.Coordinator, config CrashChildConfig) error {
	boundary := strings.TrimPrefix(config.Mode, "recover-")
	result := childResult{PID: os.Getpid()}
	if boundary == "after-ack-cleanup" {
		before, err := readAccepted(config.LogPath)
		if err != nil {
			return err
		}
		if err := runPublisherFor(ctx, coordinator, config.LogPath, 300*time.Millisecond); err != nil {
			return err
		}
		after, err := readAccepted(config.LogPath)
		if err != nil {
			return err
		}
		if len(after) != len(before) {
			return fmt.Errorf("P7_CRASH_RECOVERY: acknowledged causation republished")
		}
		if _, err := coordinator.RunRetention(ctx, provider.RetentionPolicy{OlderThan: time.Now().UTC().Add(time.Hour), MaxRows: 16}); err != nil {
			return err
		}
		result.Status = "absent"
		return writeJSON(config.Result, result)
	}
	if err := runPublisherUntilDelivered(ctx, coordinator, config.Causation, config.LogPath); err != nil {
		return err
	}
	accepted, err := readAccepted(config.LogPath)
	if err != nil {
		return err
	}
	result.Claimed, result.Status, result.Facts = true, "delivered", 2
	start := len(accepted) - 2
	if start < 0 {
		return fmt.Errorf("P7_CRASH_RECOVERY: complete causal batch was not accepted")
	}
	for _, notice := range accepted[start:] {
		result.EventIDs = append(result.EventIDs, notice.EventID)
	}
	return writeJSON(config.Result, result)
}

func runPublisherUntilDelivered(ctx context.Context, coordinator provider.Coordinator, causation, logPath string) error {
	resolver, err := canonicalCrashResolver()
	if err != nil {
		return err
	}
	publisher, err := eventoutbox.NewPublisher(coordinator, resolver, &crashJournalTransport{mode: "recovery", logPath: logPath}, eventoutbox.Limits{ClaimGroups: 1, Concurrency: 1, LeaseDuration: crashLease, PublishTimeout: time.Second, RetryBase: 10 * time.Millisecond, RetryCap: 100 * time.Millisecond, ShutdownGrace: 50 * time.Millisecond})
	if err != nil {
		return err
	}
	workerContext, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- publisher.Run(workerContext)
	}()
	defer func() { cancel(); <-done }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		state, inspectErr := coordinator.Inspect(ctx, causation)
		if inspectErr == nil && state.Status == provider.StatusDelivered {
			return nil
		}
		select {
		case err := <-done:
			return fmt.Errorf("P7_CRASH_RECOVERY: publisher stopped before delivery: %v", err)
		case <-deadline.C:
			return fmt.Errorf("P7_CRASH_RECOVERY: delivery timeout")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func runPublisherFor(ctx context.Context, coordinator provider.Coordinator, logPath string, duration time.Duration) error {
	resolver, err := canonicalCrashResolver()
	if err != nil {
		return err
	}
	publisher, err := eventoutbox.NewPublisher(coordinator, resolver, &crashJournalTransport{mode: "recovery", logPath: logPath}, eventoutbox.Limits{ClaimGroups: 1, Concurrency: 1, LeaseDuration: crashLease, PublishTimeout: time.Second, RetryBase: 10 * time.Millisecond, RetryCap: 100 * time.Millisecond, ShutdownGrace: 50 * time.Millisecond})
	if err != nil {
		return err
	}
	workerContext, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	return publisher.Run(workerContext)
}

func verifyBoundary(ctx context.Context, coordinator provider.Coordinator, boundary, causation string, ids []string, child childResult, accepted []acceptedNotice) (int, error) {
	if boundary == "after-ack-cleanup" {
		if child.Claimed || child.Status != "absent" || len(accepted) != len(ids) {
			return 0, fmt.Errorf("cleanup restart evidence is inconsistent")
		}
		if _, err := coordinator.Inspect(ctx, causation); err == nil {
			return 0, fmt.Errorf("retention did not atomically remove fact and state")
		}
		return 0, nil
	}
	if !child.Claimed || child.Status != "delivered" || child.Facts != len(ids) || !equalStrings(child.EventIDs, ids) {
		return 0, fmt.Errorf("restart did not deliver exact complete causal group: %#v", child)
	}
	state, err := coordinator.Inspect(ctx, causation)
	if err != nil || state.Status != provider.StatusDelivered {
		return 0, fmt.Errorf("restart did not commit delivered state: status=%s err=%v", state.Status, err)
	}
	expected := []string(nil)
	switch boundary {
	case "before-claim-commit", "after-claim":
		expected = []string{ids[0], ids[1]}
	case "partial-transport-acceptance":
		expected = []string{ids[0], ids[0], ids[1]}
	case "accepted-before-ack":
		expected = []string{ids[0], ids[1], ids[0], ids[1]}
	}
	acceptedIDs := make([]string, len(accepted))
	for index := range accepted {
		acceptedIDs[index] = accepted[index].EventID
	}
	if !equalStrings(acceptedIDs, expected) {
		return 0, fmt.Errorf("accepted IDs=%v want=%v", acceptedIDs, expected)
	}
	if boundary == "partial-transport-acceptance" && !equalBytes(accepted[0].Encoded, accepted[1].Encoded) {
		return 0, fmt.Errorf("partial retry changed encoded duplicate bytes")
	}
	if boundary == "accepted-before-ack" && (!equalBytes(accepted[0].Encoded, accepted[2].Encoded) || !equalBytes(accepted[1].Encoded, accepted[3].Encoded)) {
		return 0, fmt.Errorf("accepted-before-ack retry changed encoded causal batch")
	}
	return len(acceptedIDs) - len(uniqueStrings(acceptedIDs)), nil
}

func appendAcceptedNotices(path string, notices []eventvalue.Notice) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(file)
	encoder := json.NewEncoder(writer)
	for _, notice := range notices {
		if err := encoder.Encode(acceptedNotice{EventID: formatUUID16([16]byte(notice.EventID())), Encoded: notice.Encoded()}); err != nil {
			file.Close()
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func readAccepted(path string) ([]acceptedNotice, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := []acceptedNotice{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record acceptedNotice
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			return nil, err
		}
		if record.EventID == "" || len(record.Encoded) == 0 {
			return nil, fmt.Errorf("P7_CRASH_JOURNAL: accepted notice is incomplete")
		}
		result = append(result, record)
	}
	return result, scanner.Err()
}

func signalAndHold(path string) error {
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return err
	}
	select {}
}

func waitLeaseExpiry(ctx context.Context) error {
	timer := time.NewTimer(crashLease + 150*time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func writeJSON(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o600)
}

func readJSON(path string, value any) error {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, value)
}

func randomHex(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func envValue(extra []string, name string) string {
	for index := len(extra) - 1; index >= 0; index-- {
		key, value, found := strings.Cut(extra[index], "=")
		if found && key == name {
			return value
		}
	}
	return os.Getenv(name)
}

func sanitizedPostgresEndpoint(dsn string) string {
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Host != "" {
		database := strings.TrimPrefix(parsed.Path, "/")
		if database != "" {
			return parsed.Host + "/" + database
		}
		return parsed.Host
	}
	fields := strings.Fields(dsn)
	values := map[string]string{}
	for _, field := range fields {
		key, value, found := strings.Cut(field, "=")
		if found && (key == "host" || key == "port" || key == "dbname") {
			values[key] = value
		}
	}
	host := values["host"]
	if host == "" {
		host = "configured-host"
	}
	if values["port"] != "" {
		host += ":" + values["port"]
	}
	if values["dbname"] != "" {
		host += "/" + values["dbname"]
	}
	return host
}

func quoteIdentifier(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func uniqueStrings(values []string) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		result[value] = true
	}
	return result
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func bounded(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum] + "..."
}

const sqliteCrashSchema = `
PRAGMA journal_mode=WAL;
CREATE TABLE "_golem_outbox" (
 "event_id" TEXT PRIMARY KEY,"fact_version" INTEGER NOT NULL,"codec_identity" TEXT NOT NULL,
 "generation_fingerprint" TEXT NOT NULL,"model_id" TEXT NOT NULL,"action" TEXT NOT NULL,
 "before_identity" BLOB,"after_identity" BLOB,"causation_id" TEXT NOT NULL,
 "transaction_ordinal" INTEGER NOT NULL,"metadata" BLOB NOT NULL,"delete_snapshot" BLOB,
 "recorded_at" INTEGER NOT NULL
);
CREATE TABLE "_golem_outbox_delivery" (
 "causation_id" TEXT PRIMARY KEY,"status" TEXT NOT NULL,"first_recorded_at" INTEGER NOT NULL,
 "attempt_count" INTEGER NOT NULL,"available_at" INTEGER NOT NULL,"lease_token" TEXT,
 "lease_until" INTEGER,"delivered_at" INTEGER,"last_failure_code" TEXT,"blocked_at" INTEGER,
 "retired_at" INTEGER,"updated_at" INTEGER NOT NULL
);`

const postgresCrashSchema = `
CREATE TABLE $SCHEMA."_golem_outbox" (
 "event_id" text PRIMARY KEY,"fact_version" bigint NOT NULL,"codec_identity" text NOT NULL,
 "generation_fingerprint" text NOT NULL,"model_id" text NOT NULL,"action" text NOT NULL,
 "before_identity" bytea,"after_identity" bytea,"causation_id" text NOT NULL,
 "transaction_ordinal" bigint NOT NULL,"metadata" bytea NOT NULL,"delete_snapshot" bytea,
 "recorded_at" timestamptz(6) NOT NULL
);
CREATE TABLE $SCHEMA."_golem_outbox_delivery" (
 "causation_id" text PRIMARY KEY,"status" text NOT NULL,"first_recorded_at" timestamptz(6) NOT NULL,
 "attempt_count" bigint NOT NULL,"available_at" timestamptz(6) NOT NULL,"lease_token" text,
 "lease_until" timestamptz(6),"delivered_at" timestamptz(6),"last_failure_code" text,
 "blocked_at" timestamptz(6),"retired_at" timestamptz(6),"updated_at" timestamptz(6) NOT NULL
);`
