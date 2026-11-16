//go:build linux || darwin

package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type stubReleaseSource struct {
	version        string
	archive        []byte
	checks         int
	downloads      int
	beforeDownload func()
}

func (s *stubReleaseSource) latest(context.Context) (string, error) {
	s.checks++
	return s.version, nil
}

func (s *stubReleaseSource) download(_ context.Context, _, _ string) ([]byte, error) {
	s.downloads++
	if s.beforeDownload != nil {
		s.beforeDownload()
	}
	return s.archive, nil
}

func updateTarget(t *testing.T) (string, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "servctl")
	if err := os.WriteFile(path, []byte("original servctl"), 0750); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	return path, file
}

func fixtureELF(machine elf.Machine) []byte {
	data := make([]byte, 121) // ELF64 header, one loadable segment header, one byte of code.
	copy(data, "\x7fELF\x02\x01\x01")
	write16 := func(offset int, value uint16) { binary.LittleEndian.PutUint16(data[offset:], value) }
	write32 := func(offset int, value uint32) { binary.LittleEndian.PutUint32(data[offset:], value) }
	write64 := func(offset int, value uint64) { binary.LittleEndian.PutUint64(data[offset:], value) }
	write16(16, uint16(elf.ET_EXEC))
	write16(18, uint16(machine))
	write32(20, uint32(elf.EV_CURRENT))
	write64(24, 0x400078)
	write64(32, 64)
	write16(52, 64)
	write16(54, 56)
	write16(56, 1)
	write32(64, uint32(elf.PT_LOAD))
	write32(68, uint32(elf.PF_R|elf.PF_X))
	write64(80, 0x400000)
	write64(96, uint64(len(data)))
	write64(104, uint64(len(data)))
	write64(112, 4096)
	return data
}

func fixtureUpdateArchive(t *testing.T, name string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	compressed := gzip.NewWriter(&buf)
	archive := tar.NewWriter(compressed)
	if err := archive.WriteHeader(&tar.Header{Name: name, Size: int64(len(payload)), Mode: 0755, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUpdateReplacesBinaryAndPreservesMode(t *testing.T) {
	path, running := updateTarget(t)
	original, err := running.Stat()
	if err != nil {
		t.Fatal(err)
	}
	newBinary := fixtureELF(elf.EM_X86_64)
	source := &stubReleaseSource{version: "v0.10.0", archive: fixtureUpdateArchive(t, "servctl", newBinary)}
	var out bytes.Buffer
	if err := update(context.Background(), "v0.9.9", "amd64", &out, source, path, running); err != nil {
		t.Fatal(err)
	}
	installed, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, newBinary) || os.SameFile(original, installed) {
		t.Fatalf("did not replace the original executable: %v", err)
	}
	if installed.Mode().Perm() != original.Mode().Perm() || source.checks != 1 || source.downloads != 1 || !strings.Contains(out.String(), "v0.10.0") {
		t.Fatalf("unexpected mode, requests, or output: %v, %+v, %s", installed.Mode(), source, &out)
	}
	assertOnlyTarget(t, path)
}

func TestUpdateSkipsSameOrOlderRelease(t *testing.T) {
	for _, version := range []string{"v2.0.0", "v1.99.99"} {
		t.Run(version, func(t *testing.T) {
			source := &stubReleaseSource{version: version}
			var out bytes.Buffer
			if err := update(context.Background(), "v2.0.0", "amd64", &out, source, "", nil); err != nil {
				t.Fatal(err)
			}
			if source.checks != 1 || source.downloads != 0 || !strings.Contains(out.String(), "v2.0.0") {
				t.Fatalf("unexpected requests or output: %+v, %s", source, &out)
			}
		})
	}
}

func TestUpdateRejectsInvalidVersionOrArchitecture(t *testing.T) {
	for _, tt := range []struct {
		current, arch, latest string
		checks                int
	}{
		{"dev", "amd64", "v2.0.0", 0},
		{"v1.0.0", "386", "v2.0.0", 0},
		{"v1.0.0", "amd64", "v2.0.0-beta.1", 1},
	} {
		source := &stubReleaseSource{version: tt.latest}
		if err := update(context.Background(), tt.current, tt.arch, io.Discard, source, "", nil); err == nil || source.checks != tt.checks || source.downloads != 0 {
			t.Fatalf("unexpected outcome for %+v: %v, %+v", tt, err, source)
		}
	}
}

func TestUpdateRejectsBadArchiveWithoutChangingTarget(t *testing.T) {
	for _, tt := range []struct {
		name    string
		archive []byte
	}{
		{"wrong member", fixtureUpdateArchive(t, "other", fixtureELF(elf.EM_X86_64))},
		{"wrong architecture", fixtureUpdateArchive(t, "servctl", fixtureELF(elf.EM_AARCH64))},
		{"invalid gzip", []byte("not a gzip archive")},
		{"text file", fixtureUpdateArchive(t, "servctl", []byte("not an ELF"))},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, running := updateTarget(t)
			source := &stubReleaseSource{version: "v1.1.0", archive: tt.archive}
			if err := update(context.Background(), "v1.0.0", "amd64", io.Discard, source, path, running); err == nil {
				t.Fatal("accepted invalid archive")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "original servctl" {
				t.Fatalf("changed target: %q, %v", got, err)
			}
			assertOnlyTarget(t, path)
		})
	}
}

func TestUpdateRefusesToReplaceNewerInstallation(t *testing.T) {
	path, running := updateTarget(t)
	source := &stubReleaseSource{version: "v1.1.0", archive: fixtureUpdateArchive(t, "servctl", fixtureELF(elf.EM_X86_64))}
	source.beforeDownload = func() {
		replacement := filepath.Join(filepath.Dir(path), "other-install")
		if err := os.WriteFile(replacement, []byte("newer installation"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
	}
	if err := update(context.Background(), "v1.0.0", "amd64", io.Discard, source, path, running); err == nil {
		t.Fatal("replaced a different executable")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "newer installation" {
		t.Fatalf("changed target: %q, %v", got, err)
	}
	assertOnlyTarget(t, path)
}

func TestUpdateRefusesConcurrentReplacement(t *testing.T) {
	path, running := updateTarget(t)
	other, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	source := &stubReleaseSource{version: "v1.1.0"}
	if err := update(context.Background(), "v1.0.0", "amd64", io.Discard, source, path, running); err == nil || source.downloads != 0 {
		t.Fatalf("concurrent update attempted a replacement: %v, %+v", err, source)
	}
	assertOnlyTarget(t, path)
}

func TestCancelledUpdateDoesNotReplaceBinary(t *testing.T) {
	path, running := updateTarget(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &stubReleaseSource{
		version:        "v1.1.0",
		archive:        fixtureUpdateArchive(t, "servctl", fixtureELF(elf.EM_X86_64)),
		beforeDownload: cancel,
	}
	if err := update(ctx, "v1.0.0", "amd64", io.Discard, source, path, running); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "original servctl" {
		t.Fatalf("changed target: %q, %v", got, err)
	}
	assertOnlyTarget(t, path)
}

func assertOnlyTarget(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("unexpected install directory entries: %v, %v", entries, err)
	}
}
