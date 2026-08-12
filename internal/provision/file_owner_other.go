//go:build !linux

package provision

import "os"

func sameFileOwner(os.FileInfo, os.FileInfo) bool { return true }

func hasNamedOwner(os.FileInfo, string, string) bool { return true }
