//go:build unix

package authkeys

import (
	"fmt"
	"os"
	"syscall"
)

// lockFileName is a DEDICATED lock file, held open across the mutation. It is
// separate from keys.json on purpose: saveLocked writes keys.json via a temp
// file + atomic rename, which replaces the inode — a flock taken on the
// keys.json fd would not guard the new file. The lock file is never renamed, so
// a flock on it is a stable cross-process mutex for the whole issue/revoke
// critical section. Its contents are irrelevant (always empty).
const lockFileName = "keys.json.lock"

// flockLocked acquires an exclusive advisory lock (flock LOCK_EX) on the
// store's lock file, blocking until no other process holds it, and returns a
// function that releases it. The caller already holds s.mu (in-process
// exclusion); this adds cross-process exclusion so a concurrent `keys` CLI and
// the live server's portal cannot lose each other's writes.
//
// unix build: real syscall.Flock. A build-tagged no-op stub covers any
// non-unix dev machine (see flock_other.go); the deploy target is Linux.
func (s *Store) flockLocked() (unlock func(), err error) {
	path := s.dir + string(os.PathSeparator) + lockFileName
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("authkeys: open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("authkeys: flock: %w", err)
	}
	return func() {
		// LOCK_UN then close. Closing the fd also drops the flock, but unlock
		// explicitly so the ordering is obvious.
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}
