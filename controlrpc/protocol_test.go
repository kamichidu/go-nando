package controlrpc

import (
	"context"
	"encoding/json"
	"testing"
)

func TestClientRejectsEmptyMethod(t *testing.T) {
	client := &Client{}
	if err := client.Call(context.Background(), "", struct{}{}, nil); err == nil {
		t.Fatal("empty JSON-RPC method succeeded")
	}
}

func TestProtocolDTOJSONNames(t *testing.T) {
	got, err := json.Marshal(BeginReadResult{SessionID: "s", ResourceURI: "file:///tmp/staging"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"session_id":"s","resource_uri":"file:///tmp/staging","entry":{"id":"","parent_id":null,"name":"","kind":"","size":0,"mtime":0,"mode":0,"content_type":"","revision":0,"created_at":0,"updated_at":0}}` {
		t.Fatalf("unexpected protocol JSON: %s", got)
	}
}
