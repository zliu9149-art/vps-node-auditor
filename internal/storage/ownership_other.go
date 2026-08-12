//go:build !linux

package storage

import "os"

func matchDatabaseOwnership(string, os.FileInfo) error {
	return nil
}
