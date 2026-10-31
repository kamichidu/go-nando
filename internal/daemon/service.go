package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/repository"
	"github.com/kamichidu/go-nando/pkg/controlrpc"
)

const (
	jsonRPCMethodNotFound = controlrpc.CodeMethodNotFound
	jsonRPCInvalidParams  = controlrpc.CodeInvalidParams
	jsonRPCEntryNotFound  = controlrpc.CodeEntryNotFound
	jsonRPCEntryConflict  = controlrpc.CodeEntryConflict
)

type repositoryUseCases interface {
	Stat(context.Context, string) (metadata.Entry, error)
	PutExpected(context.Context, app.PutInput, repository.PutExpectation) error
}

type service struct {
	app      repositoryUseCases
	sessions *sessionManager
}

type pathParams struct {
	Path string `json:"path"`
}

type beginWriteParams struct {
	Path            string   `json:"path"`
	ResourceSchemes []string `json:"resource_schemes"`
}

type sessionParams struct {
	SessionID string `json:"session_id"`
}

type entryResult struct {
	ID       metadata.EntryID   `json:"id"`
	Name     string             `json:"name"`
	Kind     metadata.EntryKind `json:"kind"`
	Revision uint64             `json:"revision"`
	Size     int64              `json:"size"`
}

func newService(application repositoryUseCases, sessions *sessionManager) *service {
	return &service{app: application, sessions: sessions}
}

func (s *service) Call(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	switch method {
	case controlrpc.MethodPing:
		return struct {
			Version string `json:"version"`
		}{Version: "1"}, nil
	case controlrpc.MethodStat:
		var params pathParams
		if err := decodeParams(raw, &params); err != nil {
			return nil, &controlrpc.MethodError{Code: jsonRPCInvalidParams, Err: err}
		}
		entry, err := s.app.Stat(ctx, params.Path)
		if err != nil {
			return nil, mapMethodError(err)
		}
		return entryResult{ID: entry.ID, Name: entry.Name, Kind: entry.Kind, Revision: entry.Revision, Size: entry.Size}, nil
	case controlrpc.MethodBeginWrite:
		var params beginWriteParams
		if err := decodeParams(raw, &params); err != nil {
			return nil, &controlrpc.MethodError{Code: jsonRPCInvalidParams, Err: err}
		}
		fileSupported := false
		for _, scheme := range params.ResourceSchemes {
			if scheme == "file" {
				fileSupported = true
				break
			}
		}
		if !fileSupported {
			return nil, &controlrpc.MethodError{Code: -32010, Err: errors.New("client did not declare support for daemon's file Resource URI")}
		}
		result, err := s.sessions.beginWrite(ctx, params.Path)
		if err != nil {
			return nil, mapMethodError(err)
		}
		return result, nil
	case controlrpc.MethodCommit:
		params, err := decodeSession(raw)
		if err != nil {
			return nil, &controlrpc.MethodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if err := s.sessions.commit(ctx, params.SessionID); err != nil {
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodAbort:
		params, err := decodeSession(raw)
		if err != nil {
			return nil, &controlrpc.MethodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if err := s.sessions.abort(params.SessionID); err != nil {
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodRelease:
		params, err := decodeSession(raw)
		if err != nil {
			return nil, &controlrpc.MethodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if err := s.sessions.release(params.SessionID); err != nil {
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	default:
		return nil, &controlrpc.MethodError{Code: jsonRPCMethodNotFound, Err: fmt.Errorf("unknown method %q", method)}
	}
}

func decodeSession(raw json.RawMessage) (sessionParams, error) {
	var params sessionParams
	if err := decodeParams(raw, &params); err != nil {
		return params, err
	}
	if params.SessionID == "" {
		return params, errors.New("session_id is required")
	}
	return params, nil
}

func decodeParams(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid parameters: %w", err)
	}
	return nil
}

func mapMethodError(err error) error {
	switch {
	case errors.Is(err, metadata.ErrEntryNotFound), errors.Is(err, errSessionNotFound):
		return &controlrpc.MethodError{Code: jsonRPCEntryNotFound, Err: err}
	case errors.Is(err, metadata.ErrEntryConflict), errors.Is(err, errSessionBusy), errors.Is(err, errSessionClosed):
		return &controlrpc.MethodError{Code: jsonRPCEntryConflict, Err: err}
	default:
		return err
	}
}
