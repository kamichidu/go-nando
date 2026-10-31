package controlrpc

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const protocolVersion = "2.0"

const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeEntryNotFound  = -32004
	CodeEntryConflict  = -32009
)

const (
	MethodPing       = "daemon.ping"
	MethodStat       = "repository.stat"
	MethodBeginWrite = "resource.beginWrite"
	MethodCommit     = "resource.commit"
	MethodAbort      = "resource.abort"
	MethodRelease    = "resource.release"
)

type BeginWriteResult struct {
	SessionID   string `json:"session_id"`
	ResourceURI string `json:"resource_uri"`
}

// API handles one JSON-RPC method. Params and results are JSON values.
type API interface {
	Call(context.Context, string, json.RawMessage) (any, error)
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// RemoteError is the error returned by a JSON-RPC peer.
type RemoteError struct {
	Code    int
	Message string
}

func (e *RemoteError) Error() string { return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message) }

// NewHandler creates the HTTP JSON-RPC endpoint. A non-empty token requires
// bearer authentication on every request.
func NewHandler(api API, bearerToken string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "JSON-RPC requires POST", http.StatusMethodNotAllowed)
			return
		}
		if bearerToken != "" {
			got := []byte(r.Header.Get("Authorization"))
			want := []byte("Bearer " + bearerToken)
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || len(got) != len(want) || subtle.ConstantTimeCompare(got, want) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		defer func() {
			if err := r.Body.Close(); err != nil {
				log.Printf("close JSON-RPC request body: %v", err)
			}
		}()
		body, err := io.ReadAll(io.LimitReader(r.Body, 16<<20+1))
		if err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if len(body) > 16<<20 {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
			return
		}
		var req request
		if err := json.Unmarshal(body, &req); err != nil || req.JSONRPC != protocolVersion || len(req.ID) == 0 || req.Method == "" {
			writeResponse(w, response{JSONRPC: protocolVersion, ID: req.ID, Error: &rpcError{Code: CodeInvalidRequest, Message: "invalid JSON-RPC request"}})
			return
		}
		if len(req.Params) == 0 {
			req.Params = json.RawMessage(`{}`)
		}
		result, callErr := api.Call(r.Context(), req.Method, req.Params)
		resp := response{JSONRPC: protocolVersion, ID: req.ID, Result: result}
		if callErr != nil {
			var methodErr *MethodError
			if errors.As(callErr, &methodErr) {
				resp.Error = &rpcError{Code: methodErr.Code, Message: methodErr.Err.Error()}
			} else {
				resp.Error = &rpcError{Code: -32000, Message: callErr.Error()}
			}
			resp.Result = nil
		}
		writeResponse(w, resp)
	})
	return mux
}

// MethodError represents a stable JSON-RPC application error.
type MethodError struct {
	Code int
	Err  error
}

func (e *MethodError) Error() string { return e.Err.Error() }
func (e *MethodError) Unwrap() error { return e.Err }

func writeResponse(w http.ResponseWriter, resp response) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(resp); err != nil {
		http.Error(w, "failed to encode JSON-RPC response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("write JSON-RPC response: %v", err)
	}
}
