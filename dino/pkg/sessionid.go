package pkg

import "errors"

var ErrInvalidSessionID = errors.New("invalid session_id format")

func ValidateSessionID(sessionID string) error {
	if len(sessionID) < 8 || len(sessionID) > 128 {
		return ErrInvalidSessionID
	}
	for _, c := range sessionID {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return ErrInvalidSessionID
		}
	}
	return nil
}
