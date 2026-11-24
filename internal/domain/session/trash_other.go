//go:build !darwin

package session

import "errors"

// Trash is only implemented on macOS; Linux support is deferred.
func (s *Session) Trash() error {
	return errors.New("could not move to Trash: not supported on this platform")
}
