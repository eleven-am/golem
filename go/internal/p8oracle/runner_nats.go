package p8oracle

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	natsclient "github.com/nats-io/nats.go"
)

const (
	order7NATSImage         = "nats:2.14.4@sha256:ecf677bae6a0ae7900bd3217be041c6614d5dcd2cae780000f9cd69462b36541"
	order7NATSImageDigest   = "nats@sha256:ecf677bae6a0ae7900bd3217be041c6614d5dcd2cae780000f9cd69462b36541"
	order7NATSContainerName = "golem-p8-order7-nats"
	order7NATSAddress       = "127.0.0.1:4222"
	order7NATSMaxPayload    = 2 << 20
	order7NATSConfig        = "port: 4222\nmax_payload: 2097152\n"
)

// RunExternalNATSScenario compiles source as a clean external consumer and
// executes it against the PostgreSQL C and linguistic profiles through a real,
// test-owned Core NATS server. SQLite is deliberately absent from this runner.
func RunExternalNATSScenario(t *testing.T, source []byte, scenario string) {
	runExternalNATSScenario(t, source, scenario, false)
}

// RunExternalNATSScenarioRace is the race-instrumented form of
// RunExternalNATSScenario.
func RunExternalNATSScenarioRace(t *testing.T, source []byte, scenario string) {
	runExternalNATSScenario(t, source, scenario, true)
}

func runExternalNATSScenario(t *testing.T, source []byte, scenario string, race bool) {
	t.Helper()
	if strings.TrimSpace(scenario) == "" {
		t.Fatal("external NATS scenario is required")
	}
	available, err := order7NATSAvailable(
		os.Getenv("GOLEM_P8_REQUIRE_NATS"),
		os.Getenv("GOLEM_P8_REQUIRE_POSTGRESQL"),
		exec.LookPath,
		localNATSImagePresent,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Skip("live Core NATS prerequisites are unavailable")
	}
	startOrder7NATSContainer(t)
	proxy := newNATSCutProxy(t, order7NATSAddress)
	control := newNATSCutControl(t, proxy)
	runExternalNATSProfiles(t, source, scenario, race, proxy.URL(), control.URL())
}

type executableLookup func(string) (string, error)
type imageLookup func(string) bool

func order7NATSAvailable(required, postgresRequired string, lookup executableLookup, imagePresent imageLookup) (bool, error) {
	switch required {
	case "", "0", "1":
	default:
		return false, errors.New("GOLEM_P8_REQUIRE_NATS must be 0 or 1")
	}
	switch postgresRequired {
	case "", "0", "1":
	default:
		return false, errors.New("GOLEM_P8_REQUIRE_POSTGRESQL must be 0 or 1")
	}
	if required == "1" && postgresRequired != "1" {
		return false, errors.New("required live Core NATS requires mandatory PostgreSQL profiles")
	}
	if lookup == nil || imagePresent == nil {
		return false, errors.New("live Core NATS prerequisite inspection is unavailable")
	}
	if _, err := lookup("docker"); err != nil {
		if required == "1" {
			return false, errors.New("required live Core NATS Docker runtime is unavailable")
		}
		return false, nil
	}
	if !imagePresent(order7NATSImage) {
		if required == "1" {
			return false, errors.New("required pinned Core NATS image is unavailable")
		}
		return false, nil
	}
	return true, nil
}

func localNATSImagePresent(image string) bool {
	command := exec.Command("docker", "image", "inspect", image, "--format", "{{join .RepoDigests \"\\n\"}}")
	output, err := command.Output()
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == order7NATSImageDigest {
			return true
		}
	}
	return false
}

func startOrder7NATSContainer(t *testing.T) {
	t.Helper()
	if names, err := dockerOutput("container", "ls", "--all", "--filter", "name=^/"+order7NATSContainerName+"$", "--format", "{{.Names}}"); err != nil {
		t.Fatalf("inspect live Core NATS container ownership: %v", err)
	} else if strings.TrimSpace(names) != "" {
		t.Fatal("live Core NATS container name is already owned")
	}
	var owner [16]byte
	if _, err := cryptorand.Read(owner[:]); err != nil {
		t.Fatal("create live Core NATS ownership token")
	}
	ownerToken := hex.EncodeToString(owner[:])
	config := filepath.Join(t.TempDir(), "nats-server.conf")
	if err := os.WriteFile(config, []byte(order7NATSConfig), 0o444); err != nil {
		t.Fatal("write live Core NATS configuration")
	}
	canonicalConfig, err := filepath.EvalSymlinks(config)
	if err != nil {
		t.Fatal("canonicalize live Core NATS configuration")
	}
	mount := "type=bind,source=" + canonicalConfig + ",target=/etc/nats/golem-p8.conf,readonly"
	_, runErr := dockerOutput(
		"run", "--detach", "--name", order7NATSContainerName,
		"--label", "golem.p8.owner="+ownerToken,
		"--publish", "127.0.0.1:4222:4222",
		"--mount", mount,
		order7NATSImage, "--config", "/etc/nats/golem-p8.conf",
	)
	if runErr != nil {
		cleanupOrder7NATSContainer(t, ownerToken)
		t.Fatalf("start pinned live Core NATS container: %v", runErr)
	}
	t.Cleanup(func() { cleanupOrder7NATSContainer(t, ownerToken) })
	deadline := time.Now().Add(10 * time.Second)
	for {
		connection, connectErr := natsclient.Connect("nats://"+order7NATSAddress, natsclient.Timeout(250*time.Millisecond), natsclient.NoReconnect())
		if connectErr == nil {
			maximum := connection.MaxPayload()
			connection.Close()
			if maximum != order7NATSMaxPayload {
				t.Fatalf("live Core NATS max payload=%d want=%d", maximum, order7NATSMaxPayload)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pinned live Core NATS container did not become ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
	assertOrder7NATSContainer(t, ownerToken)
}

func assertOrder7NATSContainer(t *testing.T, owner string) {
	t.Helper()
	format := "{{.Config.Image}}|{{index .Config.Labels \"golem.p8.owner\"}}|{{json .Config.Cmd}}|{{(index .HostConfig.PortBindings \"4222/tcp\" 0).HostIp}}|{{(index .HostConfig.PortBindings \"4222/tcp\" 0).HostPort}}|{{range .Mounts}}{{.Destination}}:{{.RW}}{{end}}"
	value, err := dockerOutput("container", "inspect", order7NATSContainerName, "--format", format)
	if err != nil {
		t.Fatalf("inspect pinned live Core NATS container: %v", err)
	}
	want := order7NATSImage + "|" + owner + "|[\"--config\",\"/etc/nats/golem-p8.conf\"]|127.0.0.1|4222|/etc/nats/golem-p8.conf:false"
	if strings.TrimSpace(value) != want {
		t.Fatal("live Core NATS container topology disagrees with the reviewed boundary")
	}
}

func cleanupOrder7NATSContainer(t testing.TB, owner string) {
	t.Helper()
	actual, err := dockerOutput("container", "inspect", order7NATSContainerName, "--format", "{{index .Config.Labels \"golem.p8.owner\"}}")
	if err != nil {
		return
	}
	if !order7NATSContainerOwned(actual, owner) {
		t.Errorf("refuse to remove live Core NATS container owned by another test")
		return
	}
	if _, err := dockerOutput("container", "rm", "--force", order7NATSContainerName); err != nil {
		t.Errorf("remove test-owned live Core NATS container: %v", err)
	}
}

func order7NATSContainerOwned(actual, expected string) bool {
	return expected != "" && strings.TrimSpace(actual) == expected
}

func dockerOutput(arguments ...string) (string, error) {
	command := exec.Command("docker", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		diagnostic := strings.Join(strings.Fields(string(output)), " ")
		if len(diagnostic) > 1024 {
			diagnostic = diagnostic[:1024]
		}
		if diagnostic == "" {
			return "", fmt.Errorf("docker command failed: %w", err)
		}
		return "", fmt.Errorf("docker command failed: %w: %s", err, diagnostic)
	}
	return string(output), nil
}

func runExternalNATSProfiles(t *testing.T, source []byte, scenario string, race bool, natsURL, controlURL string) {
	t.Helper()
	validateOracleSource(t, source)
	root, example := repositoryPaths(t)
	consumer := filepath.Join(t.TempDir(), "consumer")
	if err := os.MkdirAll(consumer, 0o755); err != nil {
		t.Fatal(err)
	}
	module := `module p8oracle.nats.consumer

go 1.25.0

require (
	github.com/eleven-am/golem/go v0.0.0
	github.com/eleven-am/golem/go/examples/social v0.0.0
	github.com/nats-io/nats.go v1.52.0
)
`
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumer, "oracle_test.go"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	canonicalConsumer := canonicalPath(t, consumer)
	canonicalRoot := canonicalPath(t, root)
	canonicalExample := canonicalPath(t, example)
	workspace := filepath.Join(t.TempDir(), "go.work")
	work := fmt.Sprintf(`go 1.25.0

use (
	%s
	%s
	%s
)

replace github.com/eleven-am/golem/go v0.0.0 => %s
replace github.com/eleven-am/golem/go/examples/social v0.0.0 => %s
`, filepath.ToSlash(canonicalRoot), filepath.ToSlash(canonicalExample), filepath.ToSlash(canonicalConsumer), filepath.ToSlash(canonicalRoot), filepath.ToSlash(canonicalExample))
	if err := os.WriteFile(workspace, []byte(work), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := setEnvironment(os.Environ(), "GOWORK", workspace)
	cli := filepath.Join(t.TempDir(), "golem")
	runProcess(t, canonicalRoot, environment, "go", "build", "-o", cli, "./cmd/golem")

	for _, profile := range requiredNATSProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn, cleanup := provisionProfile(t, profile)
			defer cleanup()
			runProcess(t, canonicalExample, environment, cli, "migration", "apply", "--provider", profile.provider, "--dsn", dsn, "--migrations", "migrations")
			prefix := order7SubjectPrefix(t, profile.name)
			childEnvironment := setEnvironment(environment, "P8_ORACLE_PROVIDER", profile.provider)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_DSN", dsn)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_EXAMPLE", canonicalExample)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_CLI", cli)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_SCENARIO", scenario)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_NATS_URL", natsURL)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_NATS_CONTROL_URL", controlURL)
			childEnvironment = setEnvironment(childEnvironment, "P8_ORACLE_NATS_SUBJECT_PREFIX", prefix)
			arguments := []string{"test", "-count=1", "-run", "^TestP8ExternalNATSOracleScenario$", "."}
			if race {
				arguments = []string{"test", "-race", "-count=1", "-run", "^TestP8ExternalNATSOracleScenario$", "."}
			}
			runProcess(t, canonicalConsumer, childEnvironment, "go", arguments...)
		})
	}
}

func requiredNATSProfiles() []liveProfile {
	profiles := requiredProfiles()
	result := make([]liveProfile, 0, 2)
	for _, profile := range profiles {
		if profile.provider == "postgresql" {
			result = append(result, profile)
		}
	}
	return result
}

func order7SubjectPrefix(t testing.TB, profile string) string {
	t.Helper()
	var random [8]byte
	if _, err := cryptorand.Read(random[:]); err != nil {
		t.Fatal("create live Core NATS subject prefix")
	}
	profile = strings.NewReplacer("-", "_", ".", "_").Replace(profile)
	return "golem_p8_" + profile + "_" + hex.EncodeToString(random[:])
}

type natsProxyConnection struct {
	client net.Conn
	server net.Conn
}

type natsCutProxy struct {
	listener net.Listener
	target   string
	mu       sync.Mutex
	enabled  bool
	closed   bool
	active   map[*natsProxyConnection]struct{}
}

func newNATSCutProxy(t testing.TB, target string) *natsCutProxy {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen for live Core NATS cut proxy")
	}
	proxy := &natsCutProxy{listener: listener, target: target, enabled: true, active: make(map[*natsProxyConnection]struct{})}
	go proxy.accept()
	t.Cleanup(func() { proxy.Close() })
	return proxy
}

func (proxy *natsCutProxy) URL() string { return "nats://" + proxy.listener.Addr().String() }

func (proxy *natsCutProxy) accept() {
	for {
		client, err := proxy.listener.Accept()
		if err != nil {
			return
		}
		proxy.mu.Lock()
		enabled := proxy.enabled && !proxy.closed
		proxy.mu.Unlock()
		if !enabled {
			_ = client.Close()
			continue
		}
		server, err := net.DialTimeout("tcp", proxy.target, time.Second)
		if err != nil {
			_ = client.Close()
			continue
		}
		pair := &natsProxyConnection{client: client, server: server}
		proxy.mu.Lock()
		if !proxy.enabled || proxy.closed {
			proxy.mu.Unlock()
			_ = client.Close()
			_ = server.Close()
			continue
		}
		proxy.active[pair] = struct{}{}
		proxy.mu.Unlock()
		go proxy.forward(pair)
	}
}

func (proxy *natsCutProxy) forward(pair *natsProxyConnection) {
	done := make(chan struct{}, 2)
	copyOne := func(destination, source net.Conn) {
		_, _ = io.Copy(destination, source)
		done <- struct{}{}
	}
	go copyOne(pair.server, pair.client)
	go copyOne(pair.client, pair.server)
	<-done
	_ = pair.client.Close()
	_ = pair.server.Close()
	<-done
	proxy.mu.Lock()
	delete(proxy.active, pair)
	proxy.mu.Unlock()
}

func (proxy *natsCutProxy) Cut() {
	proxy.mu.Lock()
	proxy.enabled = false
	active := make([]*natsProxyConnection, 0, len(proxy.active))
	for pair := range proxy.active {
		active = append(active, pair)
	}
	proxy.mu.Unlock()
	for _, pair := range active {
		_ = pair.client.Close()
		_ = pair.server.Close()
	}
}

func (proxy *natsCutProxy) Restore() {
	proxy.mu.Lock()
	if !proxy.closed {
		proxy.enabled = true
	}
	proxy.mu.Unlock()
}

func (proxy *natsCutProxy) Close() {
	proxy.mu.Lock()
	if proxy.closed {
		proxy.mu.Unlock()
		return
	}
	proxy.closed = true
	proxy.enabled = false
	active := make([]*natsProxyConnection, 0, len(proxy.active))
	for pair := range proxy.active {
		active = append(active, pair)
	}
	proxy.mu.Unlock()
	_ = proxy.listener.Close()
	for _, pair := range active {
		_ = pair.client.Close()
		_ = pair.server.Close()
	}
}

type natsCutControl struct {
	server *http.Server
	listen net.Listener
	url    string
}

func newNATSCutControl(t testing.TB, proxy *natsCutProxy) *natsCutControl {
	t.Helper()
	var token [16]byte
	if _, err := cryptorand.Read(token[:]); err != nil {
		t.Fatal("create live Core NATS control token")
	}
	path := "/" + hex.EncodeToString(token[:])
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal("listen for live Core NATS control")
	}
	mux := http.NewServeMux()
	mux.HandleFunc(path+"/cut", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		proxy.Cut()
		writer.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(path+"/restore", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		proxy.Restore()
		writer.WriteHeader(http.StatusNoContent)
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	control := &natsCutControl{server: server, listen: listener, url: "http://" + listener.Addr().String() + path}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return control
}

func (control *natsCutControl) URL() string { return control.url }
