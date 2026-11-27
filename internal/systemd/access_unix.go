//go:build unix

package systemd

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

func checkUserAccess(u *user.User, execPath string, execInfo os.FileInfo, workDir string, workInfo os.FileInfo) error {
	uid64, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil
	}
	uid := uint32(uid64)
	if uid == 0 {
		return nil
	}
	gids := map[uint32]bool{}
	if gid64, err := strconv.ParseUint(u.Gid, 10, 32); err == nil {
		gids[uint32(gid64)] = true
	}
	if groups, err := u.GroupIds(); err == nil {
		for _, g := range groups {
			if gid64, err := strconv.ParseUint(g, 10, 32); err == nil {
				gids[uint32(gid64)] = true
			}
		}
	}
	if !canTraverseOrExec(uid, gids, execInfo) {
		return fmt.Errorf("运行用户 %s 无权执行程序: %s（请检查文件权限或属主）", u.Username, execPath)
	}
	if !canTraverseOrExec(uid, gids, workInfo) {
		return fmt.Errorf("运行用户 %s 无权进入工作目录: %s（请检查目录权限或属主）", u.Username, workDir)
	}
	return nil
}

func canTraverseOrExec(uid uint32, gids map[uint32]bool, info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	perm := info.Mode().Perm()
	switch {
	case st.Uid == uid:
		return perm&0100 != 0
	case gids[st.Gid]:
		return perm&0010 != 0
	default:
		return perm&0001 != 0
	}
}
