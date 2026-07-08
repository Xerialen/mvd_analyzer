//go:build !unix

package authkeys

// flockLocked is a no-op on non-unix platforms: the deploy target is Linux
// (D9), where flock_unix.go provides real cross-process exclusion. On a
// non-unix dev machine mutations fall back to in-process (s.mu) exclusion only,
// which is correct for the single-process case that `make test` exercises
// there. Documented so a future porter knows the cross-process guarantee is
// unix-only by design, not omission.
func (s *Store) flockLocked() (unlock func(), err error) {
	return func() {}, nil
}
