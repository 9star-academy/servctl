//go:build linux || darwin

package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

type releaseSource interface {
	latest(context.Context) (string, error)
	download(context.Context, string, string) ([]byte, error)
}

// Run checks for a newer stable release and updates the running servctl installation.
func Run(ctx context.Context, currentVersion string, out io.Writer) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("servctl update 仅支持 Linux")
	}
	path, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法确定当前 servctl 路径: %w", err)
	}
	// Opening /proc/self/exe identifies the original inode even if it was already replaced.
	running, err := os.Open("/proc/self/exe")
	if err != nil {
		return fmt.Errorf("无法读取当前 servctl 程序: %w", err)
	}
	defer running.Close()
	return update(ctx, currentVersion, runtime.GOARCH, out, newReleases(), path, running)
}

func update(ctx context.Context, currentVersion, arch string, out io.Writer, source releaseSource, path string, running *os.File) error {
	current, ok := parseVersion(currentVersion)
	if !ok {
		return fmt.Errorf("当前版本 %q 无法自动更新，请先安装正式发布版", currentVersion)
	}
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("servctl update 不支持当前架构 %q", arch)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	latestVersion, err := source.latest(ctx)
	if err != nil {
		return err
	}
	latest, ok := parseVersion(latestVersion)
	if !ok {
		return fmt.Errorf("最新正式版的版本号无效: %q", latestVersion)
	}
	switch slices.Compare(current[:], latest[:]) {
	case 0:
		fmt.Fprintf(out, "当前已经是最新版本 %s。\n", currentVersion)
		return nil
	case 1:
		fmt.Fprintf(out, "当前版本 %s 高于最新正式版 %s。\n", currentVersion, latestVersion)
		return nil
	}
	fmt.Fprintf(out, "检测到新版本 %s（当前 %s），正在更新。\n", latestVersion, currentVersion)
	if err := installRelease(ctx, source, latestVersion, arch, path, running); err != nil {
		return err
	}
	fmt.Fprintf(out, "已将 servctl 从 %s 更新到 %s。\n", currentVersion, latestVersion)
	return nil
}

func parseVersion(version string) ([3]uint64, bool) {
	var parsed [3]uint64
	numerals, prefixed := strings.CutPrefix(version, "v")
	parts := strings.Split(numerals, ".")
	if !prefixed || len(parts) != len(parsed) {
		return parsed, false
	}
	for i, part := range parts {
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return parsed, false
		}
		parsed[i] = number
	}
	return parsed, true
}

func installRelease(ctx context.Context, source releaseSource, version, arch, path string, running *os.File) error {
	fd := int(running.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return fmt.Errorf("另一个 servctl update 正在运行")
		}
		return fmt.Errorf("锁定当前 servctl 程序: %w", err)
	}
	defer syscall.Flock(fd, syscall.LOCK_UN)
	original, err := running.Stat()
	if err != nil {
		return fmt.Errorf("读取当前 servctl 程序信息: %w", err)
	}
	if err := checkTarget(path, original); err != nil {
		return err
	}
	staged, err := os.CreateTemp(filepath.Dir(path), ".servctl-update-*")
	if err != nil {
		return fmt.Errorf("无法在 %s 暂存升级文件: %w", filepath.Dir(path), err)
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	archive, err := source.download(ctx, version, arch)
	if err != nil {
		return fmt.Errorf("下载并校验升级包: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := extractBinary(staged, archive); err != nil {
		return fmt.Errorf("解压升级包: %w", err)
	}
	if err := checkBinary(staged, arch); err != nil {
		return fmt.Errorf("校验升级程序: %w", err)
	}
	if err := preservePermissions(staged, original); err != nil {
		return fmt.Errorf("设置升级文件权限: %w", err)
	}
	if err := staged.Sync(); err != nil {
		return fmt.Errorf("同步升级文件: %w", err)
	}
	if err := staged.Close(); err != nil {
		return fmt.Errorf("关闭升级文件: %w", err)
	}
	if err := checkTarget(path, original); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(staged.Name(), path); err != nil {
		return fmt.Errorf("替换 servctl 程序 %s: %w", path, err)
	}
	return nil
}

func checkTarget(path string, original os.FileInfo) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("读取安装路径 %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || !os.SameFile(info, original) {
		return fmt.Errorf("安装路径 %s 已变更，请重新运行 servctl update", path)
	}
	return nil
}

func extractBinary(target io.Writer, archive []byte) error {
	const maxExecutableSize = 64 << 20
	const tarOverheadLimit = 64 << 10
	reader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer reader.Close()
	limited := &io.LimitedReader{R: reader, N: maxExecutableSize + tarOverheadLimit}
	files := tar.NewReader(limited)
	header, err := files.Next()
	if err != nil {
		return err
	}
	if header.Name != "servctl" || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > maxExecutableSize {
		return fmt.Errorf("升级包中的 servctl 文件无效")
	}
	if _, err := io.Copy(target, files); err != nil {
		return err
	}
	if _, err := files.Next(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("升级包包含额外文件")
	}
	// Consume the gzip trailer to validate its checksum and detect truncation.
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return err
	}
	if limited.N == 0 {
		return fmt.Errorf("升级包解压后的大小超出限制")
	}
	return nil
}

func checkBinary(staged *os.File, arch string) error {
	info, err := staged.Stat()
	if err != nil {
		return err
	}
	binary, err := elf.NewFile(staged)
	if err != nil {
		return err
	}
	wantMachine := elf.EM_X86_64
	if arch == "arm64" {
		wantMachine = elf.EM_AARCH64
	}
	if binary.Class != elf.ELFCLASS64 || binary.Data != elf.ELFDATA2LSB || binary.Machine != wantMachine ||
		(binary.Type != elf.ET_EXEC && binary.Type != elf.ET_DYN) ||
		(binary.OSABI != elf.ELFOSABI_NONE && binary.OSABI != elf.ELFOSABI_LINUX) {
		return fmt.Errorf("升级程序不是 Linux %s 可执行文件", arch)
	}
	fileSize := uint64(info.Size())
	hasEntry := false
	for _, segment := range binary.Progs {
		if segment.Type != elf.PT_LOAD {
			continue
		}
		if segment.Filesz > segment.Memsz || segment.Off > fileSize || segment.Filesz > fileSize-segment.Off {
			return fmt.Errorf("升级程序含有无效的 ELF 加载段")
		}
		if segment.Flags&elf.PF_X != 0 && binary.Entry >= segment.Vaddr && binary.Entry-segment.Vaddr < segment.Filesz {
			hasEntry = true
		}
	}
	if !hasEntry {
		return fmt.Errorf("升级程序缺少可执行入口")
	}
	return nil
}

func preservePermissions(staged *os.File, original os.FileInfo) error {
	info, err := staged.Stat()
	if err != nil {
		return err
	}
	owner := original.Sys().(*syscall.Stat_t)
	stagedOwner := info.Sys().(*syscall.Stat_t)
	if stagedOwner.Uid != owner.Uid || stagedOwner.Gid != owner.Gid {
		if err := staged.Chown(int(owner.Uid), int(owner.Gid)); err != nil {
			return err
		}
	}
	return staged.Chmod(original.Mode().Perm())
}
