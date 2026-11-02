package adapter

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/webdav"
)

func TestPropfindDoesNotAdvertiseUnsupportedLocks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{dav: &webdav.Handler{FileSystem: webdav.Dir(root), LockSystem: webdav.NewMemLS()}}
	for _, tc := range []struct {
		name, depth, body string
		wantLockStatus    int
		wantOtherStatus   int
	}{
		{name: "allprop depth 0", depth: "0", body: `<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`},
		{name: "allprop depth 1", depth: "1", body: `<D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`},
		{name: "allprop include lock property", depth: "0", body: `<D:propfind xmlns:D="DAV:"><D:allprop/><D:include><D:supportedlock/></D:include></D:propfind>`, wantLockStatus: http.StatusNotFound},
		{name: "propname depth 0", depth: "0", body: `<D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`},
		{name: "propname depth 1", depth: "1", body: `<D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`},
		{name: "explicit depth 0", depth: "0", body: `<D:propfind xmlns:D="DAV:"><D:prop><D:supportedlock/></D:prop></D:propfind>`, wantLockStatus: http.StatusNotFound},
		{name: "explicit depth 1", depth: "1", body: `<D:propfind xmlns:D="DAV:"><D:prop><D:supportedlock/></D:prop></D:propfind>`, wantLockStatus: http.StatusNotFound},
		{name: "explicit mixed properties", depth: "0", body: `<D:propfind xmlns:D="DAV:"><D:prop><D:displayname/><D:supportedlock/></D:prop></D:propfind>`, wantLockStatus: http.StatusNotFound, wantOtherStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("PROPFIND", "http://example.test/", strings.NewReader(tc.body))
			request.Header.Set("Depth", tc.depth)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusMultiStatus {
				t.Fatalf("PROPFIND status=%d, want 207: %s", response.Code, response.Body.String())
			}
			statuses := propStatuses(t, response.Body.Bytes())
			assertPropStatus(t, statuses, "supportedlock", tc.wantLockStatus)
			if tc.wantOtherStatus != 0 {
				assertPropStatus(t, statuses, "displayname", tc.wantOtherStatus)
			}
		})
	}
}

func propStatuses(t *testing.T, body []byte) map[string][]int {
	t.Helper()
	decoder := xml.NewDecoder(bytes.NewReader(body))
	statuses := make(map[string][]int)
	var stack []xml.Name
	var properties []string
	var status string
	propCount := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			if token.Name.Space == "DAV:" && token.Name.Local == "propstat" {
				status = ""
				properties = nil
				propCount = 0
			}
			if len(stack) > 0 && stack[len(stack)-1] == davName("prop") {
				properties = append(properties, token.Name.Local)
				propCount++
			}
			if token.Name.Space == "DAV:" && token.Name.Local == "lockentry" {
				t.Error("unsupported DAV:lockentry was advertised")
			}
			stack = append(stack, token.Name)
		case xml.CharData:
			if strings.Contains(string(token), "HTTP/1.1 ") {
				status = strings.TrimSpace(string(token))
			}
		case xml.EndElement:
			if token.Name == davName("propstat") {
				if propCount == 0 {
					t.Error("PROPFIND returned an empty propstat")
				}
				code := 0
				if _, err := fmt.Sscanf(status, "HTTP/1.1 %d", &code); err != nil {
					t.Fatalf("parse propstat status %q: %v", status, err)
				}
				for _, property := range properties {
					statuses[property] = append(statuses[property], code)
				}
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	return statuses
}

func assertPropStatus(t *testing.T, statuses map[string][]int, property string, wantStatus int) {
	t.Helper()
	got := statuses[property]
	if wantStatus == 0 {
		if len(got) != 0 {
			t.Fatalf("unsupported property %q appeared with statuses %v", property, got)
		}
		return
	}
	if len(got) == 0 {
		t.Fatalf("explicit property %q was absent instead of being reported 404", property)
	}
	for _, status := range got {
		if status != wantStatus {
			t.Fatalf("property %q status=%d, want %d (all: %v)", property, status, wantStatus, got)
		}
	}
}

func TestOptionsAndUnsupportedLockMethodsAgree(t *testing.T) {
	handler := &Handler{dav: http.NotFoundHandler()}
	options := httptest.NewRecorder()
	handler.ServeHTTP(options, httptest.NewRequest(http.MethodOptions, "http://example.test/", nil))
	if options.Code != http.StatusOK || options.Header().Get("DAV") != "1" {
		t.Fatalf("OPTIONS status=%d DAV=%q, want 200/DAV: 1", options.Code, options.Header().Get("DAV"))
	}
	for _, method := range []string{"LOCK", "UNLOCK"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "http://example.test/file", nil))
		if response.Code != http.StatusNotImplemented {
			t.Errorf("%s status=%d, want 501", method, response.Code)
		}
	}
}
