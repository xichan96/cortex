package approval

import "time"

// ScopeStore routes remembered decisions to the store that owns each scope:
// session decisions stay in memory, workspace decisions go to the project's
// permissions.json, and always decisions go to the user's permissions.json.
//
// It is a convenience for hosts; the pipeline only depends on Store/Broker.
type ScopeStore struct {
	Session   Store
	Workspace Store
	Always    Store
	Now       func() time.Time
}

// NewScopeStore builds a ScopeStore from optional stores. Any nil store means
// that scope cannot be remembered (Remember returns ErrStoreUnavailable).
func NewScopeStore(session, workspace, always Store) *ScopeStore {
	return &ScopeStore{Session: session, Workspace: workspace, Always: always}
}

func (s *ScopeStore) storeFor(scope ApprovalScope) Store {
	if s == nil {
		return nil
	}
	switch scope {
	case ScopeSession:
		return s.Session
	case ScopeWorkspace:
		return s.Workspace
	case ScopeAlways:
		return s.Always
	default:
		return nil
	}
}

// Remember persists an approved response according to its scope. It is a no-op
// when the response was not marked Remember or uses ScopeOnce.
func (s *ScopeStore) Remember(req ApprovalRequest, resp ApprovalResponse) error {
	if !resp.Remember || !resp.Scope.Rememberable() {
		return nil
	}
	store := s.storeFor(resp.Scope)
	if store == nil {
		return ErrStoreUnavailable
	}
	pattern := req.Rule
	if pattern == "" {
		pattern = "*"
	}
	rec := PermissionRecord{
		Tool:     req.ToolName,
		Pattern:  pattern,
		Scope:    resp.Scope,
		Decision: resp.Decision,
		Source:   "user",
	}
	if s.Now != nil {
		rec.CreatedAt = s.Now().UTC()
	} else {
		rec.CreatedAt = time.Now().UTC()
	}
	return store.Put(rec)
}

// Lookup checks session, then workspace, then always, returning the first
// remembered record. The pattern falls back to "*" when empty.
func (s *ScopeStore) Lookup(tool, pattern string) (PermissionRecord, bool) {
	if s == nil {
		return PermissionRecord{}, false
	}
	for _, store := range []Store{s.Session, s.Workspace, s.Always} {
		if store == nil {
			continue
		}
		if rec, ok := store.Lookup(tool, pattern); ok {
			return rec, true
		}
	}
	return PermissionRecord{}, false
}

// LookupRequest is Lookup using the request's tool and rule as the key.
func (s *ScopeStore) LookupRequest(req ApprovalRequest) (PermissionRecord, bool) {
	return s.Lookup(req.ToolName, req.Rule)
}
