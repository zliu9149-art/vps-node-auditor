//go:build windows

// Package lockfile provides the Windows test/development lock implementation.
// 中文：lockfile 包提供 Windows 测试和开发环境的锁实现。
package lockfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Lock is one held exclusive marker file on Windows.
// 中文：Lock 表示 Windows 上一个已持有的独占标记文件。
type Lock struct {
	file *os.File
	path string
}

// TryAcquire attempts to create the marker without replacing another owner.
// 中文：TryAcquire 尝试创建标记文件，且不会替换其他持有者。
func TryAcquire(path string) (*Lock, bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, fmt.Errorf("create lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("acquire marker lock: %w", err)
	}
	return &Lock{file: file, path: path}, false, nil
}

// Close removes the Windows marker after releasing its handle.
// 中文：Close 释放句柄后删除 Windows 标记文件。
func (l *Lock) Close() error {
	return errors.Join(l.file.Close(), os.Remove(l.path))
}
