//go:build linux

package provision

import (
	"errors"
	"os"
	"syscall"
)

func matchFileOwnership(path string, reference os.FileInfo) error {
	stat, ok := reference.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("reference has no Linux ownership metadata")
	}
	return os.Chown(path, int(stat.Uid), int(stat.Gid))
}
