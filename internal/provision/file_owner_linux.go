//go:build linux

package provision

import (
	"os"
	"os/user"
	"strconv"
	"syscall"
)

func sameFileOwner(left, right os.FileInfo) bool {
	l, lok := left.Sys().(*syscall.Stat_t)
	r, rok := right.Sys().(*syscall.Stat_t)
	return lok && rok && l.Uid == r.Uid && l.Gid == r.Gid
}

func hasNamedOwner(info os.FileInfo, userName, groupName string) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	wantedUser, err := user.Lookup(userName)
	if err != nil {
		return false
	}
	wantedGroup, err := user.LookupGroup(groupName)
	if err != nil {
		return false
	}
	uid, uidErr := strconv.ParseUint(wantedUser.Uid, 10, 32)
	gid, gidErr := strconv.ParseUint(wantedGroup.Gid, 10, 32)
	return uidErr == nil && gidErr == nil && uint64(stat.Uid) == uid && uint64(stat.Gid) == gid
}
