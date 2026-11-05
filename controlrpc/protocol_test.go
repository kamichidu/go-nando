package controlrpc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestResolveTokenSources(t *testing.T) {
	t.Setenv("NANDO_TEST_TOKEN", "from environment")
	tests := []struct {
		name    string
		literal string
		file    string
		env     string
		want    string
	}{
		{name: "unset sources"},
		{name: "literal", literal: "literal value", want: "literal value"},
		{name: "environment", env: "NANDO_TEST_TOKEN", want: "from environment"},
		{name: "unset environment", env: "NANDO_MISSING_TOKEN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveToken(test.literal, test.file, test.env)
			if err != nil || got != test.want {
				t.Fatalf("resolved token=%q err=%v, want %q", got, err, test.want)
			}
		})
	}
	if _, err := ResolveToken("secret-value", "", "NAME"); err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("conflict was accepted or leaked a literal: %v", err)
	}
}

func TestClientRejectsEmptyMethod(t *testing.T) {
	client := &Client{}
	if err := client.Call(context.Background(), "", struct{}{}, nil); err == nil {
		t.Fatal("empty JSON-RPC method succeeded")
	}
}

func TestBeginWriteResultCarriesBeginTimePreconditionSnapshot(t *testing.T) {
	got, err := json.Marshal(BeginWriteResult{SessionID: "s", ResourceURI: "file:///tmp/staging", ExpectedEntryID: "entry", ExpectedRevision: 7, ExpectedMtime: 123, MustBeAbsent: false})
	if err != nil {
		t.Fatal(err)
	}
	wantFields := []string{`"expected_entry_id":"entry"`, `"expected_revision":7`, `"expected_mtime":123`, `"must_be_absent":false`}
	for _, field := range wantFields {
		if !strings.Contains(string(got), field) {
			t.Fatalf("BeginWriteResult missing %s in %s", field, got)
		}
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
