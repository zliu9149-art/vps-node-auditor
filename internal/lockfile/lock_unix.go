//go:build !windows

// Package lockfile provides a crash-safe process lock shared by collection and
// provision operations.
// 中文：lockfile 包为采集和 provision 操作提供进程崩溃后可自动释放的共享互斥锁。
package lockfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Lock is one held advisory file lock.
// 中文：Lock 表示一个已持有的建议性文件锁。
type Lock struct{ file *os.File }

// TryAcquire attempts a non-blocking exclusive lock. busy is true when another
// process currently owns it.
// 中文：TryAcquire 尝试非阻塞独占锁；其他进程持有锁时 busy 为 true。
func TryAcquire(path string) (lock *Lock, busy bool, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, false, fmt.Errorf("create lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		file, err = os.Open(path)
		if err != nil {
			return nil, false, fmt.Errorf("open lock file: %w", err)
		}
	} else if err := os.Chmod(path, 0o644); err != nil {
		file.Close()
		return nil, false, fmt.Errorf("set lock file permissions: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("acquire file lock: %w", err)
	}
	return &Lock{file: file}, false, nil
}

// Close releases the advisory lock. The inode remains so concurrent processes
// cannot bypass a lock by racing an unlink.
// 中文：Close 释放建议性锁；保留 inode 可避免并发进程通过删除竞争绕过锁。
func (l *Lock) Close() error {
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	return errors.Join(unlockErr, closeErr)
}
