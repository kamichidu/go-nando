package adapter

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var (
	davName             = func(local string) xml.Name { return xml.Name{Space: "DAV:", Local: local} }
	davPropstatPattern  = regexp.MustCompile(`(?s)<D:propstat>(.*?)</D:propstat>`)
	davPropPattern      = regexp.MustCompile(`(?s)<D:prop>(.*?)</D:prop>`)
	davStatusPattern    = regexp.MustCompile(`<D:status>[^<]*</D:status>`)
	davSupportedPattern = regexp.MustCompile(`<D:supportedlock\b[^>]*/>|<D:supportedlock\b[^>]*>[\s\S]*?</D:supportedlock>`)
)

// propfind avoids advertising the lock capability x/net/webdav assumes for
// every Handler. Its multistatus encoder uses the documented D: DAV prefix.
// This adapter does not implement LOCK/UNLOCK, so an unrequested supportedlock
// property is omitted and an explicitly requested one is reported as 404.
func (h *Handler) propfind(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read PROPFIND body: "+err.Error(), http.StatusBadRequest)
		return
	}
	explicit := requestsSupportedLock(body)
	r.Body = io.NopCloser(bytes.NewReader(body))

	stream := &propfindResponse{ResponseWriter: w, explicit: explicit}
	h.dav.ServeHTTP(stream, r)
	if err := stream.finish(); err != nil {
		return
	}
}

type propfindResponse struct {
	http.ResponseWriter
	status   int
	explicit bool
	inside   bool
	pending  []byte
}

func (w *propfindResponse) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *propfindResponse) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.status != http.StatusMultiStatus {
		return w.ResponseWriter.Write(p)
	}
	w.pending = append(w.pending, p...)
	if err := w.flushResponses(false); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (w *propfindResponse) finish() error { return w.flushResponses(true) }

func (w *propfindResponse) flushResponses(final bool) error {
	const startTag = "<D:response>"
	const endTag = "</D:response>"
	for len(w.pending) > 0 {
		if !w.inside {
			start := bytes.Index(w.pending, []byte(startTag))
			if start < 0 {
				keep := 0
				if !final {
					keep = longestMarkerSuffix(w.pending, startTag)
				}
				writeEnd := len(w.pending) - keep
				if writeEnd == 0 {
					return nil
				}
				if _, err := w.ResponseWriter.Write(w.pending[:writeEnd]); err != nil {
					return err
				}
				w.pending = w.pending[writeEnd:]
				continue
			}
			if start > 0 {
				if _, err := w.ResponseWriter.Write(w.pending[:start]); err != nil {
					return err
				}
				w.pending = w.pending[start:]
			}
			w.inside = true
		}
		end := bytes.Index(w.pending, []byte(endTag))
		if end < 0 {
			if final {
				return fmt.Errorf("incomplete WebDAV response element")
			}
			return nil
		}
		responseEnd := end + len(endTag)
		filtered, err := filterSupportedLock(w.pending[:responseEnd], w.explicit)
		if err != nil {
			return err
		}
		if _, err := w.ResponseWriter.Write(filtered); err != nil {
			return err
		}
		w.pending = w.pending[responseEnd:]
		w.inside = false
	}
	return nil
}

func longestMarkerSuffix(data []byte, marker string) int {
	max := len(marker) - 1
	if len(data) < max {
		max = len(data)
	}
	for n := max; n > 0; n-- {
		if bytes.Equal(data[len(data)-n:], []byte(marker[:n])) {
			return n
		}
	}
	return 0
}

func requestsSupportedLock(body []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	var parents []xml.Name
	for {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		switch token := token.(type) {
		case xml.StartElement:
			if token.Name == davName("supportedlock") && len(parents) > 0 {
				parent := parents[len(parents)-1]
				if parent == davName("prop") || parent == davName("include") {
					return true
				}
			}
			parents = append(parents, token.Name)
		case xml.EndElement:
			if len(parents) > 0 {
				parents = parents[:len(parents)-1]
			}
		}
	}
}

func filterSupportedLock(body []byte, explicit bool) ([]byte, error) {
	if !bytes.Contains(body, []byte("<D:propstat>")) {
		return body, nil
	}
	var result strings.Builder
	result.Grow(len(body))
	last := 0
	for _, match := range davPropstatPattern.FindAllSubmatchIndex(body, -1) {
		start, end := match[0], match[1]
		result.Write(body[last:start])
		group := string(body[start:end])
		filtered, extra, err := rewritePropstatXML(group, explicit)
		if err != nil {
			return nil, err
		}
		result.WriteString(filtered)
		result.WriteString(extra)
		last = end
	}
	result.Write(body[last:])
	return []byte(result.String()), nil
}

func rewritePropstatXML(propstat string, explicit bool) (string, string, error) {
	lock := davSupportedPattern.FindString(propstat)
	if lock == "" {
		return propstat, "", nil
	}
	propertyMatch := davPropPattern.FindStringSubmatchIndex(propstat)
	if propertyMatch == nil {
		return "", "", fmt.Errorf("PROPFIND propstat containing supportedlock has no prop element")
	}
	propertyBody := strings.Replace(propstat[propertyMatch[2]:propertyMatch[3]], lock, "", 1)
	otherProperties, err := countPropChildren(propertyBody)
	if err != nil {
		return "", "", err
	}
	withoutLock := strings.Replace(propstat, lock, "", 1)
	if !explicit {
		if otherProperties == 0 {
			return "", "", nil
		}
		return withoutLock, "", nil
	}
	if otherProperties == 0 {
		empty := `<D:supportedlock/>`
		withoutLock = strings.Replace(propstat, lock, empty, 1)
		status := `<D:status>HTTP/1.1 404 Not Found</D:status>`
		if !davStatusPattern.MatchString(withoutLock) {
			return "", "", fmt.Errorf("PROPFIND propstat containing supportedlock has no status")
		}
		return davStatusPattern.ReplaceAllString(withoutLock, status), "", nil
	}
	notFound := `<D:propstat><D:prop><D:supportedlock/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat>`
	return withoutLock, notFound, nil
}

func countPropChildren(body string) (int, error) {
	decoder := xml.NewDecoder(strings.NewReader(`<D:prop xmlns:D="DAV:">` + body + `</D:prop>`))
	var stack []xml.Name
	count := 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return 0, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			if len(stack) == 1 && stack[0] == davName("prop") {
				stack = append(stack, token.Name)
				count++
				continue
			}
			stack = append(stack, token.Name)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
}
