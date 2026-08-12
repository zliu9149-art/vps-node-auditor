//go:build linux

package storage

import (
	"errors"
	"os"
	"syscall"
)

func matchDatabaseOwnership(path string, directory os.FileInfo) error {
	stat, ok := directory.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("database directory has no Linux ownership metadata")
	}
	return os.Chown(path, int(stat.Uid), int(stat.Gid))
}
