//go:build !unix

package systemd

import (
	"os"
	"os/user"
)

func checkUserAccess(*user.User, string, os.FileInfo, string, os.FileInfo) error {
	return nil
}
