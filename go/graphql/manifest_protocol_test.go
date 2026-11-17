package graphql

import (
	"encoding/json"
	"testing"
)

type objectManifestExecutor struct{ *testExecutor }

func (objectManifestExecutor) ClientManifestVersion() int { return 2 }

func TestDTOClientManifestHandshakeRejectsOldAndUnknownFormats(t *testing.T) {
	server := &Server[int]{executor: objectManifestExecutor{&testExecutor{}}}
	fingerprint := server.contractFingerprint.String()
	for _, test := range []struct {
		version string
		want    handshakeRefusal
	}{
		{"", handshakeRefusal("manifest")}, {`,"manifestVersion":1`, handshakeRefusal("manifest")},
		{`,"manifestVersion":2`, ""}, {`,"manifestVersion":3`, handshakeRefusal("manifest")},
		{`,"manifestVersion":null`, handshakeRefusal("manifest")}, {`,"manifestVersion":"2"`, handshakeRefusal("manifest")},
		{`,"manifestVersion":2.5`, handshakeRefusal("manifest")}, {`,"manifestVersion":0`, handshakeRefusal("manifest")},
	} {
		block := json.RawMessage(`{"protocol":1,"contract":"` + fingerprint + `"` + test.version + `}`)
		if got := server.checkHandshake(block); got != test.want {
			t.Fatalf("%s: refusal=%q want=%q", block, got, test.want)
		}
	}
	base := &Server[int]{executor: &testExecutor{}}
	if got := base.checkHandshake(json.RawMessage(`{"protocol":1,"contract":"` + fingerprint + `"}`)); got != "" {
		t.Fatalf("existing client refused: %s", got)
	}
	if got := base.checkHandshake(json.RawMessage(`{"protocol":1,"contract":"` + fingerprint + `","manifestVersion":2}`)); got != handshakeRefusal("manifest") {
		t.Fatalf("unsupported DTO capability accepted: %s", got)
	}
}
