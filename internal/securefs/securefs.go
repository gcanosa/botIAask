// Package securefs picks the permissions of the directories and files that hold secrets (databases
// with session tokens and password hashes, backups, channel/PM logs).
//
// The default keeps the historical modes (dirs 0755, log files 0644): a rootless-Podman/Quadlet
// deployment writes the bind-mounted volumes as a subuid, and the host operator reads logs/ and
// data/ through the "other" permission bits. With bot.private_files: true (see SetPrivate) new dirs
// are 0700 and new files 0600, which is what a bare-metal multi-user host wants.
package securefs

import (
	"os"
	"sync/atomic"
)

var private atomic.Bool

// SetPrivate switches between the default modes and owner-only modes (config bot.private_files).
func SetPrivate(on bool) { private.Store(on) }

// FileMode is the mode for newly created log files.
func FileMode() os.FileMode {
	if private.Load() {
		return 0o600
	}
	return 0o644
}

// Mkdir creates dir (and parents). In private mode dir itself is also made 0700, best effort: a
// directory we don't own (e.g. a bind mount owned by another uid) keeps its mode. In the default
// mode an existing directory is never touched.
func Mkdir(dir string) error {
	if !private.Load() {
		return os.MkdirAll(dir, 0o755)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(dir, 0o700)
	return nil
}
