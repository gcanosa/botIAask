// Package securefs creates the directories that hold secrets (databases with session tokens and
// password hashes, backups, channel/PM logs) so other local users cannot read them.
package securefs

import "os"

// MkdirPrivate creates dir (and parents) and makes dir itself 0700. The chmod is best effort: a
// directory we don't own (e.g. a bind mount owned by another uid) keeps its mode.
func MkdirPrivate(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	return nil
}
