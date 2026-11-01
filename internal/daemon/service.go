package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kamichidu/go-nando/controlrpc"
	"github.com/kamichidu/go-nando/internal/app"
	"github.com/kamichidu/go-nando/internal/metadata"
	"github.com/kamichidu/go-nando/internal/repository"
)

const (
	jsonRPCMethodNotFound = controlrpc.CodeMethodNotFound
	jsonRPCInvalidParams  = controlrpc.CodeInvalidParams
	jsonRPCEntryNotFound  = controlrpc.CodeEntryNotFound
	jsonRPCEntryConflict  = controlrpc.CodeEntryConflict
)

type repositoryUseCases interface {
	Stat(context.Context, string) (metadata.Entry, error)
	ResolveEntry(context.Context, metadata.EntryID) (metadata.Entry, string, error)
	List(context.Context, string) ([]metadata.Entry, error)
	Mkdir(context.Context, string, uint32) error
	PutExpected(context.Context, app.PutInput, repository.PutExpectation) error
	PutExpectedEntryID(context.Context, app.PutInput, metadata.Entry) error
	Move(context.Context, app.MoveInput) error
	MoveEntry(context.Context, metadata.EntryID, string) error
	Remove(context.Context, string) error
	RemoveEntry(context.Context, metadata.EntryID) error
	GC(context.Context) (int, error)
	MaintenanceOnce(context.Context) error
	Doctor(context.Context) error
	Sync(context.Context) error
	BeginReadResource(context.Context, string) (metadata.Entry, string, func() error, error)
	BeginReadEntryResource(context.Context, metadata.EntryID) (metadata.Entry, string, func() error, error)
}

type service struct {
	app      repositoryUseCases
	sessions *sessionManager
}

type pathParams struct {
	Path string `json:"path"`
}

type moveParams struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
}

type mkdirParams struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
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
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		entry, err := s.app.Stat(ctx, params.Path)
		if err != nil {
			return nil, mapMethodError(err)
		}
		return protocolEntry(entry), nil
	case controlrpc.MethodResolveEntry:
		var params controlrpc.ResolveEntryParams
		if err := decodeParams(raw, &params); err != nil || params.EntryID == "" {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: errors.New("entry_id is required")}
		}
		entry, currentPath, err := s.app.ResolveEntry(ctx, metadata.EntryID(params.EntryID))
		if err != nil {
			return nil, mapMethodError(err)
		}
		return controlrpc.ResolveEntryResult{Entry: protocolEntry(entry), Path: currentPath}, nil
	case controlrpc.MethodList:
		var params pathParams
		if err := decodeParams(raw, &params); err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
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
	case controlrpc.MethodMkdir:
		var params mkdirParams
		if err := decodeParams(raw, &params); err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if _, err := s.app.Stat(ctx, params.Path); err == nil {
			return nil, &methodError{Code: controlrpc.CodeEntryExists, Err: metadata.ErrEntryConflict}
		} else if !errors.Is(err, metadata.ErrEntryNotFound) {
			return nil, mapMethodError(err)
		}
		if err := s.app.Mkdir(ctx, params.Path, params.Mode); err != nil {
			if errors.Is(err, metadata.ErrEntryConflict) {
				if _, statErr := s.app.Stat(ctx, params.Path); statErr == nil {
					return nil, &methodError{Code: controlrpc.CodeEntryExists, Err: err}
				}
			}
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodMove:
		var params moveParams
		if err := decodeParams(raw, &params); err != nil || params.Source == "" || params.Destination == "" {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: errors.New("source and destination are required")}
		}
		if _, err := s.app.Stat(ctx, params.Destination); err == nil {
			return nil, &methodError{Code: controlrpc.CodeEntryExists, Err: metadata.ErrEntryConflict}
		} else if !errors.Is(err, metadata.ErrEntryNotFound) {
			return nil, mapMethodError(err)
		}
		if err := s.app.Move(ctx, app.MoveInput{SourcePath: params.Source, DestinationPath: params.Destination}); err != nil {
			if errors.Is(err, metadata.ErrEntryConflict) {
				if _, statErr := s.app.Stat(ctx, params.Destination); statErr == nil {
					return nil, &methodError{Code: controlrpc.CodeEntryExists, Err: err}
				}
			}
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodMoveEntry:
		var params controlrpc.MoveEntryParams
		if err := decodeParams(raw, &params); err != nil || params.EntryID == "" || params.Destination == "" {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: errors.New("entry_id and destination are required")}
		}
		if _, err := s.app.Stat(ctx, params.Destination); err == nil {
			return nil, &methodError{Code: controlrpc.CodeEntryExists, Err: metadata.ErrEntryConflict}
		} else if !errors.Is(err, metadata.ErrEntryNotFound) {
			return nil, mapMethodError(err)
		}
		if err := s.app.MoveEntry(ctx, metadata.EntryID(params.EntryID), params.Destination); err != nil {
			if errors.Is(err, metadata.ErrEntryConflict) {
				if _, statErr := s.app.Stat(ctx, params.Destination); statErr == nil {
					return nil, &methodError{Code: controlrpc.CodeEntryExists, Err: err}
				}
			}
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodRemove:
		var params pathParams
		if err := decodeParams(raw, &params); err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		entry, statErr := s.app.Stat(ctx, params.Path)
		if statErr != nil {
			return nil, mapMethodError(statErr)
		}
		if err := s.app.Remove(ctx, params.Path); err != nil {
			if entry.Kind == metadata.EntryKindDirectory && errors.Is(err, metadata.ErrEntryConflict) {
				if children, listErr := s.app.List(ctx, params.Path); listErr == nil && len(children) > 0 {
					return nil, &methodError{Code: controlrpc.CodeDirectoryNotEmpty, Err: err}
				}
			}
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodRemoveEntry:
		var params controlrpc.RemoveEntryParams
		if err := decodeParams(raw, &params); err != nil || params.EntryID == "" {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: errors.New("entry_id is required")}
		}
		if err := s.app.RemoveEntry(ctx, metadata.EntryID(params.EntryID)); err != nil {
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
		var params controlrpc.BeginReadParams
		if err := decodeParams(raw, &params); err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if params.EntryID == "" && params.Path == "" {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: errors.New("path or entry_id is required")}
		}
		if !containsScheme(params.ResourceSchemes, "file") {
			return nil, &methodError{Code: -32010, Err: errors.New("client did not declare support for daemon's file Resource URI")}
		}
		result, err := s.sessions.beginRead(ctx, params.Path, metadata.EntryID(params.EntryID))
		if err != nil {
			return nil, mapMethodError(err)
		}
		return result, nil
	case controlrpc.MethodBeginWrite:
		var params controlrpc.BeginWriteParams
		if err := decodeParams(raw, &params); err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if params.EntryID == "" && params.Path == "" {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: errors.New("path or entry_id is required")}
		}
		if !containsScheme(params.ResourceSchemes, "file") {
			return nil, &methodError{Code: -32010, Err: errors.New("client did not declare support for daemon's file Resource URI")}
		}
		var result controlrpc.BeginWriteResult
		var err error
		if params.EntryID != "" {
			result, err = s.sessions.beginWriteEntry(ctx, metadata.EntryID(params.EntryID))
		} else {
			result, err = s.sessions.beginWrite(ctx, params.Path, params.FollowEntryID)
		}
		if err != nil {
			return nil, mapMethodError(err)
		}
		return result, nil
	case controlrpc.MethodCommit:
		params, err := decodeSession(raw)
		if err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if err := s.sessions.commit(ctx, params.SessionID); err != nil {
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodAbort:
		params, err := decodeSession(raw)
		if err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if err := s.sessions.abort(params.SessionID); err != nil {
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodMarkUnlinked:
		params, err := decodeSession(raw)
		if err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if err := s.sessions.markUnlinked(params.SessionID); err != nil {
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodRelease:
		params, err := decodeSession(raw)
		if err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if err := s.sessions.release(params.SessionID); err != nil {
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodRenew:
		params, err := decodeSession(raw)
		if err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		expiresAt, err := s.sessions.renew(params.SessionID)
		if err != nil {
			return nil, mapMethodError(err)
		}
		return controlrpc.RenewResult{ExpiresAt: expiresAt}, nil
	case controlrpc.MethodRevoke:
		params, err := decodeSession(raw)
		if err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if err := s.sessions.revoke(params.SessionID); err != nil {
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodRecoveryList:
		return s.sessions.listRecoveries(), nil
	case controlrpc.MethodRecoveryRetry:
		params, err := decodeSession(raw)
		if err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if err := s.sessions.retryRecovery(ctx, params.SessionID); err != nil {
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	case controlrpc.MethodRecoveryDiscard:
		params, err := decodeSession(raw)
		if err != nil {
			return nil, &methodError{Code: jsonRPCInvalidParams, Err: err}
		}
		if err := s.sessions.discardRecovery(params.SessionID); err != nil {
			return nil, mapMethodError(err)
		}
		return struct{}{}, nil
	default:
		return nil, &methodError{Code: jsonRPCMethodNotFound, Err: fmt.Errorf("unknown method %q", method)}
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
		return &methodError{Code: jsonRPCEntryNotFound, Err: err}
	case errors.Is(err, errSessionExpired):
		return &methodError{Code: controlrpc.CodeResourceSessionExpired, Err: err}
	case errors.Is(err, metadata.ErrEntryConflict), errors.Is(err, errSessionBusy), errors.Is(err, errSessionClosed):
		return &methodError{Code: jsonRPCEntryConflict, Err: err}
	default:
		return err
	}
}
