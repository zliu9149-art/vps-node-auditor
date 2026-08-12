//go:build !linux

package provision

import "os"

func matchFileOwnership(string, os.FileInfo) error {
	return nil
}
