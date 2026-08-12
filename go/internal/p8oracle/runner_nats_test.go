package p8oracle

import (
	"errors"
	"io"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestOrder7NATSLiveAuthorityIsExact(t *testing.T) {
	if order7NATSImage != "nats:2.14.4@sha256:ecf677bae6a0ae7900bd3217be041c6614d5dcd2cae780000f9cd69462b36541" ||
		order7NATSImageDigest != "nats@sha256:ecf677bae6a0ae7900bd3217be041c6614d5dcd2cae780000f9cd69462b36541" ||
		order7NATSContainerName != "golem-p8-order7-nats" || order7NATSAddress != "127.0.0.1:4222" ||
		order7NATSMaxPayload != 2097152 || order7NATSConfig != "port: 4222\nmax_payload: 2097152\n" {
		t.Fatal("live Core NATS image, topology, or configuration authority changed")
	}
	profiles := requiredNATSProfiles()
	if len(profiles) != 2 {
		t.Fatalf("live Core NATS profile count=%d", len(profiles))
	}
	want := []liveProfile{
		{name: "postgresql-c", provider: "postgresql", baseDSN: profiles[0].baseDSN, collation: "C", ctype: "C"},
		{name: "postgresql-linguistic", provider: "postgresql", baseDSN: profiles[1].baseDSN, collation: "linguistic"},
	}
	if !reflect.DeepEqual(profiles, want) {
		t.Fatalf("live Core NATS profiles=%#v", profiles)
	}
}

func TestOrder7RequiredNATSEnvironmentFailsClosed(t *testing.T) {
	missingDocker := func(string) (string, error) { return "", errors.New("missing") }
	docker := func(string) (string, error) { return "/usr/bin/docker", nil }
	missingImage := func(string) bool { return false }
	image := func(value string) bool { return value == order7NATSImage }

	if available, err := order7NATSAvailable("1", "1", missingDocker, image); available || err == nil {
		t.Fatalf("required missing Docker availability=(%t,%v)", available, err)
	}
	if available, err := order7NATSAvailable("1", "1", docker, missingImage); available || err == nil {
		t.Fatalf("required missing image availability=(%t,%v)", available, err)
	}
	if available, err := order7NATSAvailable("0", "", missingDocker, image); available || err != nil {
		t.Fatalf("optional missing Docker availability=(%t,%v)", available, err)
	}
	if available, err := order7NATSAvailable("required", "1", docker, image); available || err == nil {
		t.Fatalf("invalid required mode availability=(%t,%v)", available, err)
	}
	if available, err := order7NATSAvailable("1", "1", docker, image); !available || err != nil {
		t.Fatalf("required complete availability=(%t,%v)", available, err)
	}
}

func TestOrder7RequiredNATSRequiresMandatoryPostgreSQL(t *testing.T) {
	unexpectedDocker := func(string) (string, error) {
		t.Fatal("inspected Docker before mandatory PostgreSQL mode")
		return "", nil
	}
	unexpectedImage := func(string) bool { t.Fatal("inspected image before mandatory PostgreSQL mode"); return false }
	for _, value := range []string{"", "0", "required"} {
		if available, err := order7NATSAvailable("1", value, unexpectedDocker, unexpectedImage); available || err == nil {
			t.Fatalf("NATS-required PostgreSQL mode %q availability=(%t,%v)", value, available, err)
		}
	}
	docker := func(string) (string, error) { return "/usr/bin/docker", nil }
	image := func(string) bool { return true }
	if available, err := order7NATSAvailable("", "", docker, image); !available || err != nil {
		t.Fatalf("optional NATS/PostgreSQL availability=(%t,%v)", available, err)
	}
}

func TestOrder7NATSContainerOwnershipIsExact(t *testing.T) {
	if !order7NATSContainerOwned("owner-token\n", "owner-token") {
		t.Fatal("exact container owner was refused")
	}
	for _, pair := range [][2]string{{"other", "owner-token"}, {"", "owner-token"}, {"owner-token", ""}} {
		if order7NATSContainerOwned(pair[0], pair[1]) {
			t.Fatalf("non-owner was accepted actual=%q expected=%q", pair[0], pair[1])
		}
	}
}

func TestOrder7NATSCutProxyDropsAndRestoresConnections(t *testing.T) {
	upstream, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go echoAccepted(upstream)
	proxy := newNATSCutProxy(t, upstream.Addr().String())
	connection := dialEcho(t, proxy.listener.Addr().String())
	assertEcho(t, connection, "before")
	proxy.Cut()
	if _, err := connection.Write([]byte("cut")); err == nil {
		buffer := make([]byte, 3)
		if _, err := connection.Read(buffer); err == nil {
			t.Fatal("cut proxy retained an active connection")
		}
	}
	_ = connection.Close()
	proxy.Restore()
	restored := dialEcho(t, proxy.listener.Addr().String())
	defer restored.Close()
	assertEcho(t, restored, "after")
}

func echoAccepted(listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close()
			_, _ = io.Copy(connection, connection)
		}()
	}
}

func dialEcho(t *testing.T, address string) net.Conn {
	t.Helper()
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return connection
}

func assertEcho(t *testing.T, connection net.Conn, value string) {
	t.Helper()
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	if _, err := connection.Write([]byte(value)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len(value))
	if _, err := io.ReadFull(connection, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != value {
		t.Fatalf("echo=%q want=%q", buffer, value)
	}
}
