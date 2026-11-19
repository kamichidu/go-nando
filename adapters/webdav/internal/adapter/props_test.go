package adapter

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kamichidu/go-nando/controlrpc"
)

type propertyClient struct {
	updates int
	last    controlrpc.DeadPropertiesParams
}

func (c *propertyClient) Call(_ context.Context, method string, params any, result any) error {
	switch method {
	case controlrpc.MethodStat:
		v := map[string]any{"id": "entry-1", "name": "file", "kind": "file", "size": 5, "mtime": 1, "mode": 420, "revision": 1}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, result)
	case controlrpc.MethodList:
		b, err := json.Marshal([]controlrpc.Entry{})
		if err != nil {
			return err
		}
		return json.Unmarshal(b, result)
	case controlrpc.MethodDeadProperties:
		b, err := json.Marshal([]controlrpc.DeadProperty{{Namespace: "urn:test", Name: "color", XML: `<x:color xmlns:x="urn:test">blue</x:color>`}})
		if err != nil {
			return err
		}
		return json.Unmarshal(b, result)
	case controlrpc.MethodUpdateDeadProperties:
		c.updates++
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, &c.last)
	default:
		return nil
	}
}

func TestPropertiesPropfindAndAtomicProppatch(t *testing.T) {
	client := &propertyClient{}
	handler := New(client)
	find := httptest.NewRequest("PROPFIND", "http://example.test/file", strings.NewReader(`<D:propfind xmlns:D="DAV:" xmlns:X="urn:test"><D:prop><X:color/><D:getetag/></D:prop></D:propfind>`))
	find.Header.Set("Depth", "0")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, find)
	if response.Code != 207 || !strings.Contains(response.Body.String(), "blue") || !strings.Contains(response.Body.String(), "getetag") {
		t.Fatalf("PROPFIND status=%d body=%s", response.Code, response.Body.String())
	}
	patch := httptest.NewRequest("PROPPATCH", "http://example.test/file", strings.NewReader(`<D:propertyupdate xmlns:D="DAV:" xmlns:X="urn:test"><D:set><D:prop><X:color>green</X:color></D:prop></D:set></D:propertyupdate>`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, patch)
	if response.Code != 207 || client.updates != 1 || len(client.last.Changes) != 1 || !strings.Contains(client.last.Changes[0].Property.XML, "green") {
		t.Fatalf("PROPPATCH status=%d updates=%d params=%+v body=%s", response.Code, client.updates, client.last, response.Body.String())
	}
	for _, body := range []string{
		`<D:other xmlns:D="DAV:" xmlns:X="urn:test"><D:set><D:prop><X:color>red</X:color></D:prop></D:set></D:other>`,
		`<D:propertyupdate xmlns:D="DAV:" xmlns:X="urn:test"><D:set><D:remove><D:prop><X:color/></D:prop></D:remove></D:set></D:propertyupdate>`,
		`<D:propertyupdate xmlns:D="DAV:" xmlns:X="urn:test"><D:set><X:color>red</X:color></D:set></D:propertyupdate>`,
		`<D:propertyupdate xmlns:D="DAV:" xmlns:X="urn:test"><D:set><D:prop/></D:set></D:propertyupdate>`,
		`<D:propertyupdate xmlns:D="DAV:" xmlns:X="urn:test"><X:color>red</X:color></D:propertyupdate>`,
	} {
		invalid := httptest.NewRequest("PROPPATCH", "http://example.test/file", strings.NewReader(body))
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, invalid)
		if response.Code != 400 || client.updates != 1 {
			t.Fatalf("invalid PROPPATCH mutated state: status=%d updates=%d body=%s", response.Code, client.updates, response.Body.String())
		}
	}
	protected := httptest.NewRequest("PROPPATCH", "http://example.test/file", strings.NewReader(`<D:propertyupdate xmlns:D="DAV:" xmlns:X="urn:test"><D:set><D:prop><D:getetag/><X:color>red</X:color></D:prop></D:set></D:propertyupdate>`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, protected)
	if response.Code != 207 || client.updates != 1 || !strings.Contains(response.Body.String(), "403 Forbidden") || !strings.Contains(response.Body.String(), "424 Failed Dependency") {
		t.Fatalf("protected patch was not atomic: updates=%d body=%s", client.updates, response.Body.String())
	}
	invalid := httptest.NewRequest("PROPFIND", "http://example.test/file", strings.NewReader(`<D:propfind xmlns:D="DAV:"><D:prop><Q:bad/></D:prop></D:propfind>`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, invalid)
	if response.Code != 400 {
		t.Fatalf("invalid namespace status=%d", response.Code)
	}
}
