package adapter

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kamichidu/go-nando/controlrpc"
)

func TestPreconditionsUseBeginSnapshot(t *testing.T) {
	begin := controlrpc.BeginWriteResult{ExpectedEntryID: "e1", ExpectedRevision: 4, ExpectedMtime: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC).UnixNano()}
	for _, tc := range []struct {
		name, header, value string
		want                bool
	}{
		{"matching if-match", "If-Match", `"e1-4"`, true},
		{"stale if-match", "If-Match", `"e1-3"`, false},
		{"wildcard existing", "If-Match", `*`, true},
		{"if-none match existing", "If-None-Match", `"e1-4"`, false},
		{"weak if-none match", "If-None-Match", `W/"e1-4"`, false},
		{"unmodified since", "If-Unmodified-Since", "Thu, 02 Jan 2025 03:04:06 GMT", true},
		{"modified since expected", "If-Unmodified-Since", "Thu, 02 Jan 2025 03:04:04 GMT", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("PUT", "/x", nil)
			r.Header.Set(tc.header, tc.value)
			if got := preconditionsMatch(r, begin); got != tc.want {
				t.Fatalf("preconditionsMatch=%t, want %t", got, tc.want)
			}
		})
	}
	absent := controlrpc.BeginWriteResult{MustBeAbsent: true}
	for _, tc := range []struct {
		header, value string
		want          bool
	}{{"If-Match", "*", false}, {"If-None-Match", "*", true}} {
		r := httptest.NewRequest("PUT", "/x", nil)
		r.Header.Set(tc.header, tc.value)
		if got := preconditionsMatch(r, absent); got != tc.want {
			t.Errorf("absent %s: got %t want %t", tc.header, got, tc.want)
		}
	}
}
