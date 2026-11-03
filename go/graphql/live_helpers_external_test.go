package graphql_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const liveAckWire = `{"data":null,"extensions":{"golem":{"live":true}}}`

func liveInit(fields string) json.RawMessage {
	block := `"golem":{"protocol":1,"contract":"` + strings.Repeat("0", 64) + `"}`
	if fields == "" {
		return json.RawMessage(`{` + block + `}`)
	}
	return json.RawMessage(`{` + block + `,` + fields + `}`)
}

func readLiveAck(t *testing.T, connection *websocket.Conn, id string) {
	t.Helper()
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	var message struct {
		ID      string          `json:"id"`
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := connection.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	if message.ID != id || message.Type != "next" || string(message.Payload) != liveAckWire {
		t.Fatalf("operation %q ack = %+v", id, message)
	}
}

func liveBody(query string) []byte {
	body, _ := json.Marshal(map[string]any{"query": query, "extensions": map[string]any{"golem": map[string]any{"protocol": 1, "contract": strings.Repeat("0", 64)}}})
	return body
}
