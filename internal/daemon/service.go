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
	List(context.Context, string) ([]metadata.Entry, error)
	PutExpected(context.Context, app.PutInput, repository.PutExpectation) error
	Move(context.Context, app.MoveInput) error
	Remove(context.Context, string) error
	GC(context.Context) (int, error)
	MaintenanceOnce(context.Context) error
	Doctor(context.Context) error
	Sync(context.Context) error
	BeginReadResource(context.Context, string) (metadata.Entry, string, func() error, error)
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

type moveParams struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type sessionParams struct {
	SessionID string `json:"session_id"`
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
		return protocolEntry(entry), nil
	case controlrpc.MethodList:
		var params pathParams
		if err := decodeParams(raw, &params); err != nil {
			return nil, &controlrpc.MethodError{Code: jsonRPCInvalidParams, Err: err}
		}
		entries, err := s.app.List(ctx, params.Path)
		if err != nil {
			return nil, mapMethodError(err)
		}
		result := make([]controlrpc.Entry, 0, len(entries))
		for _, entry := range entries {
			result = append(result, protocolEntry(entry))
		}
		return result, nil
	case controlrpc.MethodMove:
		var params moveParams
		if err := decodeParams(raw, &params); err != nil || params.Source == "" || params.Destination == "" {
			return nil, &controlrpc.MethodError{Code: jsonRPCInvalidParams, Err: errors.New("source and destination are required")}
		}
		if err := s.app.Move(ctx, app.MoveInput{SourcePath: params.Source, DestinationPath: params.Destination}); err != nil {
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodRemove:
		var params pathParams
		if err := decodeParams(raw, &params); err != nil {
			return nil, &controlrpc.MethodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if err := s.app.Remove(ctx, params.Path); err != nil {
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodGC:
		count, err := s.app.GC(ctx)
		if err != nil {
			return nil, err
		}
		return struct {
			Removed int `json:"removed"`
		}{Removed: count}, nil
	case controlrpc.MethodMaintenance:
		if err := s.app.MaintenanceOnce(ctx); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	case controlrpc.MethodDoctor:
		if err := s.app.Doctor(ctx); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	case controlrpc.MethodSync:
		if err := s.app.Sync(ctx); err != nil {
			return nil, err
		}
		return struct{}{}, nil
	case controlrpc.MethodBeginRead:
		var params beginWriteParams
		if err := decodeParams(raw, &params); err != nil {
			return nil, &controlrpc.MethodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if !containsScheme(params.ResourceSchemes, "file") {
			return nil, &controlrpc.MethodError{Code: -32010, Err: errors.New("client did not declare support for daemon's file Resource URI")}
		}
		result, err := s.sessions.beginRead(ctx, params.Path)
		if err != nil {
			return nil, mapMethodError(err)
		}
		return result, nil
	case controlrpc.MethodBeginWrite:
		var params beginWriteParams
		if err := decodeParams(raw, &params); err != nil {
			return nil, &controlrpc.MethodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if !containsScheme(params.ResourceSchemes, "file") {
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

func containsScheme(schemes []string, expected string) bool {
	for _, scheme := range schemes {
		if scheme == expected {
			return true
		}
	}
	return false
}

func protocolEntry(entry metadata.Entry) controlrpc.Entry {
	result := controlrpc.Entry{
		ID: string(entry.ID), Name: entry.Name, Kind: string(entry.Kind), Size: entry.Size,
		Mtime: int64(entry.Mtime), Mode: entry.Mode, ContentType: entry.ContentType,
		Revision: entry.Revision, CreatedAt: int64(entry.CreatedAt), UpdatedAt: int64(entry.UpdatedAt),
	}
	if entry.ParentID != nil {
		parent := string(*entry.ParentID)
		result.ParentID = &parent
	}
	if entry.ObjectID != nil {
		result.ObjectID = entry.ObjectID.String()
	}
	return result
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
