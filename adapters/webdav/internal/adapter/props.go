package adapter

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/kamichidu/go-nando/controlrpc"
)

type propName struct{ space, local string }
type propRequest struct {
	all      bool
	nameOnly bool
	names    []propName
	include  []propName
}

func (h *Handler) propfind(w http.ResponseWriter, r *http.Request) {
	if h.client == nil {
		h.legacyPropfind(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		http.Error(w, "invalid PROPFIND body", 400)
		return
	}
	request, err := parsePropRequest(body)
	if err != nil {
		http.Error(w, "malformed or invalid namespace in PROPFIND", 400)
		return
	}
	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "infinity"
	}
	if depth != "0" && depth != "1" && depth != "infinity" {
		http.Error(w, "invalid Depth", 400)
		return
	}
	p, err := cleanName(r.URL.Path)
	if err != nil {
		http.Error(w, "invalid path", 400)
		return
	}
	entries, err := h.propEntries(r.Context(), p, depth)
	if err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	if _, err := io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><D:multistatus xmlns:D="DAV:">`); err != nil {
		return
	}
	for _, e := range entries {
		props, missing, propErr := h.responseProps(r.Context(), e, request)
		if propErr != nil {
			return
		}
		fmt.Fprintf(w, `<D:response><D:href>%s</D:href>`, xmlEscape(e.path))
		if len(props) > 0 {
			if _, err := io.WriteString(w, `<D:propstat><D:prop>`); err != nil {
				return
			}
			for _, v := range props {
				if _, err := io.WriteString(w, v); err != nil {
					return
				}
			}
			if _, err := io.WriteString(w, `</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`); err != nil {
				return
			}
		}
		if len(missing) > 0 {
			if _, err := io.WriteString(w, `<D:propstat><D:prop>`); err != nil {
				return
			}
			for _, n := range missing {
				fmt.Fprintf(w, `<x:%s xmlns:x="%s"/>`, xmlEscape(n.local), xmlEscape(n.space))
			}
			if _, err := io.WriteString(w, `</D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>`); err != nil {
				return
			}
		}
		if _, err := io.WriteString(w, `</D:response>`); err != nil {
			return
		}
	}
	if _, err := io.WriteString(w, `</D:multistatus>`); err != nil {
		return
	}
}

type propEntry struct {
	path  string
	entry controlrpc.Entry
}

func (h *Handler) propEntries(ctx context.Context, root, depth string) ([]propEntry, error) {
	var result []propEntry
	var walk func(string, int) error
	walk = func(p string, n int) error {
		var e controlrpc.Entry
		if err := h.client.Call(ctx, controlrpc.MethodStat, map[string]string{"path": p}, &e); err != nil {
			return err
		}
		result = append(result, propEntry{p, e})
		if e.Kind == "directory" && (depth == "infinity" || n == 0 && depth == "1") {
			var children []controlrpc.Entry
			if err := h.client.Call(ctx, controlrpc.MethodList, map[string]string{"path": p}, &children); err != nil {
				return err
			}
			for _, c := range children {
				if err := walk(path.Join(p, c.Name), n+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return result, walk(root, 0)
}

func parsePropRequest(body []byte) (propRequest, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return propRequest{all: true}, nil
	}
	d := xml.NewDecoder(bytes.NewReader(body))
	var out propRequest
	var mode string
	for {
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return out, e
		}
		switch x := t.(type) {
		case xml.StartElement:
			for _, attr := range x.Attr {
				if attr.Name.Space == "xmlns" && attr.Name.Local != "" && attr.Value == "" {
					return out, fmt.Errorf("prefixed namespace cannot be empty")
				}
			}
			if x.Name == davName("allprop") || x.Name == davName("propname") {
				out.all = true
				out.nameOnly = x.Name == davName("propname")
			}
			if x.Name == davName("prop") {
				mode = "prop"
				continue
			}
			if x.Name == davName("include") {
				mode = "include"
				continue
			}
			if mode == "prop" || mode == "include" {
				if !validNamespace(x.Name.Space) {
					return out, fmt.Errorf("unbound namespace")
				}
				name := propName{x.Name.Space, x.Name.Local}
				if mode == "include" {
					out.include = append(out.include, name)
				} else {
					out.names = append(out.names, name)
				}
			}
		case xml.EndElement:
			if x.Name == davName("prop") || x.Name == davName("include") {
				mode = ""
			}
		}
	}
	if !out.all && len(out.names) == 0 {
		return out, fmt.Errorf("missing prop selector")
	}
	return out, nil
}

func (h *Handler) responseProps(ctx context.Context, e propEntry, req propRequest) ([]string, []propName, error) {
	var properties []controlrpc.DeadProperty
	if err := h.client.Call(ctx, controlrpc.MethodDeadProperties, map[string]string{"entry_id": e.entry.ID}, &properties); err != nil {
		return nil, nil, err
	}
	dead := map[propName]string{}
	for _, p := range properties {
		dead[propName{p.Namespace, p.Name}] = p.XML
	}
	known := []propName{{"DAV:", "displayname"}, {"DAV:", "getcontentlength"}, {"DAV:", "getlastmodified"}, {"DAV:", "getetag"}, {"DAV:", "resourcetype"}, {"DAV:", "creationdate"}, {"DAV:", "getcontenttype"}}
	if req.all {
		req.names = append(known, req.include...)
		for n := range dead {
			req.names = append(req.names, n)
		}
	}
	var good []string
	var missing []propName
	for _, n := range req.names {
		if req.nameOnly {
			good = append(good, fmt.Sprintf(`<x:%s xmlns:x="%s"/>`, xmlEscape(n.local), xmlEscape(n.space)))
			continue
		}
		if raw, ok := dead[n]; ok {
			good = append(good, raw)
			continue
		}
		var value string
		switch n {
		case propName{"DAV:", "displayname"}:
			value = xmlEscape(e.entry.Name)
		case propName{"DAV:", "getcontentlength"}:
			if e.entry.Kind == "file" {
				value = fmt.Sprint(e.entry.Size)
			} else {
				missing = append(missing, n)
				continue
			}
		case propName{"DAV:", "getlastmodified"}:
			value = time.Unix(0, e.entry.Mtime).UTC().Format(http.TimeFormat)
		case propName{"DAV:", "creationdate"}:
			value = time.Unix(0, e.entry.CreatedAt).UTC().Format("2006-01-02T15:04:05Z")
		case propName{"DAV:", "getcontenttype"}:
			if e.entry.Kind != "file" {
				missing = append(missing, n)
				continue
			}
			value = xmlEscape(e.entry.ContentType)
		case propName{"DAV:", "getetag"}:
			value = etag(e.entry)
		case propName{"DAV:", "resourcetype"}:
			if e.entry.Kind == "directory" {
				value = `<D:collection/>`
			}
		default:
			missing = append(missing, n)
			continue
		}
		good = append(good, fmt.Sprintf(`<D:%s>%s</D:%s>`, n.local, value, n.local))
	}
	return good, missing, nil
}
func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, `&quot;`, `'`, `&apos;`).Replace(s)
}

type patchInstruction struct {
	set  bool
	name propName
	xml  string
}

func parsePatch(body []byte) ([]patchInstruction, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	var out []patchInstruction
	var op string
	for {
		t, e := d.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, e
		}
		x, ok := t.(xml.StartElement)
		if !ok {
			continue
		}
		if x.Name == davName("set") {
			op = "set"
			continue
		}
		if x.Name == davName("remove") {
			op = "remove"
			continue
		}
		if x.Name == davName("prop") {
			continue
		}
		if op != "" {
			if !validNamespace(x.Name.Space) {
				return nil, fmt.Errorf("unbound property namespace")
			}
			var raw bytes.Buffer
			enc := xml.NewEncoder(&raw)
			x.Attr = withoutNamespaceDeclarations(x.Attr)
			if err := enc.EncodeToken(x); err != nil {
				return nil, err
			}
			level := 1
			for level > 0 {
				tok, err := d.Token()
				if err != nil {
					return nil, err
				}
				if start, ok := tok.(xml.StartElement); ok {
					level++
					start.Attr = withoutNamespaceDeclarations(start.Attr)
					tok = start
				}
				if _, ok := tok.(xml.EndElement); ok {
					level--
				}
				if err := enc.EncodeToken(tok); err != nil {
					return nil, err
				}
			}
			if err := enc.Flush(); err != nil {
				return nil, err
			}
			out = append(out, patchInstruction{set: op == "set", name: propName{x.Name.Space, x.Name.Local}, xml: raw.String()})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("missing property instructions")
	}
	return out, nil
}

func validNamespace(value string) bool {
	if value == "" {
		return true
	}
	u, err := url.Parse(value)
	return err == nil && u.Scheme != ""
}

func withoutNamespaceDeclarations(attrs []xml.Attr) []xml.Attr {
	out := attrs[:0]
	for _, attr := range attrs {
		if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" {
			continue
		}
		out = append(out, attr)
	}
	return out
}

func (h *Handler) proppatch(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(body) > 1<<20 {
		http.Error(w, "invalid PROPPATCH body", 400)
		return
	}
	ops, err := parsePatch(body)
	if err != nil {
		http.Error(w, "malformed or invalid namespace in PROPPATCH", 400)
		return
	}
	p, err := cleanName(r.URL.Path)
	if err != nil {
		http.Error(w, "invalid path", 400)
		return
	}
	var e controlrpc.Entry
	if err = h.client.Call(r.Context(), controlrpc.MethodStat, map[string]string{"path": p}, &e); err != nil {
		http.Error(w, err.Error(), statusFor(err))
		return
	}
	var changes []controlrpc.DeadPropertyChange
	protected := false
	for _, op := range ops {
		if op.name.space == "DAV:" && isLive(op.name.local) {
			protected = true
			break
		}
		v := controlrpc.DeadProperty{Namespace: op.name.space, Name: op.name.local, XML: op.xml}
		changes = append(changes, controlrpc.DeadPropertyChange{Property: v, Remove: !op.set})
	}
	code := 200
	if protected {
		code = 403
	} else if err = h.client.Call(r.Context(), controlrpc.MethodUpdateDeadProperties, controlrpc.DeadPropertiesParams{EntryID: e.ID, Changes: changes}, nil); err != nil {
		code = statusFor(err)
		if code < 400 {
			code = 409
		}
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)
	if _, err := io.WriteString(w, `<?xml version="1.0"?><D:multistatus xmlns:D="DAV:"><D:response><D:href>`+xmlEscape(p)+`</D:href>`); err != nil {
		return
	}
	for _, op := range ops {
		status := code
		if !protected || op.name.space == "DAV:" && isLive(op.name.local) {
			// This property succeeded unless a protected property rejected the batch.
		} else {
			status = 424
		}
		if status == 200 {
			fmt.Fprintf(w, `<D:propstat><D:prop><x:%s xmlns:x="%s"/></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat>`, xmlEscape(op.name.local), xmlEscape(op.name.space))
		} else {
			reason := http.StatusText(status)
			fmt.Fprintf(w, `<D:propstat><D:prop><x:%s xmlns:x="%s"/></D:prop><D:status>HTTP/1.1 %d %s</D:status></D:propstat>`, xmlEscape(op.name.local), xmlEscape(op.name.space), status, reason)
		}
	}
	if _, err := io.WriteString(w, `</D:response></D:multistatus>`); err != nil {
		return
	}
}
func isLive(s string) bool {
	switch s {
	case "displayname", "getcontentlength", "getlastmodified", "getetag", "resourcetype", "creationdate", "getcontenttype", "getcontentlanguage", "getcontentencoding", "supportedlock", "lockdiscovery":
		return true
	}
	return false
}
