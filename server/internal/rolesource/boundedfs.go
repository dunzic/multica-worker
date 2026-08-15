package rolesource

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

var (
	ErrUnsafePath        = errors.New("role source path is unsafe")
	ErrSymlink           = errors.New("role source path contains a symbolic link")
	ErrUnsupportedFile   = errors.New("role source file type is unsupported")
	ErrFileLimitExceeded = errors.New("role source file exceeds size limit")
	ErrChangedDuringRead = errors.New("role source file changed during read")
)

// BoundedFS is a read-only, root-confined filesystem view for role-source
// adapters. The selected daemon must still apply its existing top-level local
// directory policy before opening a source root; this type protects every
// descendant read from traversal, symlinks, non-regular files, and common
// read-time replacement races.
type BoundedFS struct {
	root *os.Root
}

func OpenBoundedFS(rootPath string) (*BoundedFS, error) {
	if !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != rootPath {
		return nil, fmt.Errorf("%w: source root must be a clean absolute path", ErrUnsafePath)
	}
	info, err := os.Lstat(rootPath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: source root", ErrSymlink)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: source root is not a directory", ErrUnsupportedFile)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, err
	}
	opened, err := root.Lstat(".")
	if err != nil {
		root.Close() //nolint:errcheck
		return nil, err
	}
	if !os.SameFile(info, opened) {
		root.Close() //nolint:errcheck
		return nil, fmt.Errorf("%w: source root changed during secure open", ErrChangedDuringRead)
	}
	return &BoundedFS{root: root}, nil
}

func (b *BoundedFS) Close() error {
	if b == nil || b.root == nil {
		return nil
	}
	return b.root.Close()
}

// ReadFile reads a regular file without following any symlink component. It
// checks identity, size, and modification time before and after the read so an
// adapter never silently hashes bytes from a replaced or concurrently changed
// file. maxBytes must be positive.
func (b *BoundedFS) ReadFile(name string, maxBytes int64) ([]byte, error) {
	if b == nil || b.root == nil {
		return nil, errors.New("role source filesystem is closed")
	}
	if maxBytes <= 0 {
		return nil, errors.New("role source file limit must be positive")
	}
	clean, err := normalizeSourcePath(name, false)
	if err != nil {
		return nil, err
	}
	before, err := b.lstatPath(clean)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedFile, clean)
	}
	if before.Size() > maxBytes {
		return nil, fmt.Errorf("%w: %s", ErrFileLimitExceeded, clean)
	}

	file, err := b.root.Open(clean)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, opened) || !sameSnapshot(before, opened) {
		return nil, fmt.Errorf("%w: %s", ErrChangedDuringRead, clean)
	}
	body, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("%w: %s", ErrFileLimitExceeded, clean)
	}
	after, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, after) || !sameSnapshot(before, after) || after.Size() != int64(len(body)) {
		return nil, fmt.Errorf("%w: %s", ErrChangedDuringRead, clean)
	}
	return body, nil
}

// ReadDir returns one directory level. Symlink entries are returned as entries
// so adapters can report them, but no operation in BoundedFS will traverse one.
func (b *BoundedFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if b == nil || b.root == nil {
		return nil, errors.New("role source filesystem is closed")
	}
	clean, err := normalizeSourcePath(name, true)
	if err != nil {
		return nil, err
	}
	info, err := b.lstatPath(clean)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrUnsupportedFile, clean)
	}
	return fs.ReadDir(b.root.FS(), clean)
}

// Lstat returns metadata for a root-relative path after verifying that no path
// component is a symlink.
func (b *BoundedFS) Lstat(name string) (fs.FileInfo, error) {
	if b == nil || b.root == nil {
		return nil, errors.New("role source filesystem is closed")
	}
	clean, err := normalizeSourcePath(name, true)
	if err != nil {
		return nil, err
	}
	return b.lstatPath(clean)
}

func (b *BoundedFS) lstatPath(name string) (fs.FileInfo, error) {
	if name == "." {
		return b.root.Lstat(name)
	}
	parts := strings.Split(name, "/")
	for index := range parts {
		current := strings.Join(parts[:index+1], "/")
		info, err := b.root.Lstat(current)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: %s", ErrSymlink, current)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("%w: %s is not a directory", ErrUnsupportedFile, current)
		}
		if index == len(parts)-1 {
			return info, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrUnsafePath, name)
}

func normalizeSourcePath(name string, allowRoot bool) (string, error) {
	if name == "" || strings.Contains(name, "\\") || strings.Contains(name, ":") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, name)
	}
	clean := path.Clean(name)
	if clean != name || clean == ".." || strings.HasPrefix(clean, "../") || (!allowRoot && clean == ".") {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, name)
	}
	return clean, nil
}

func sameSnapshot(before, after fs.FileInfo) bool {
	return before.Size() == after.Size() && before.ModTime().Equal(after.ModTime()) && sameMode(before.Mode(), after.Mode())
}

func sameMode(before, after fs.FileMode) bool {
	const relevant = fs.ModeType | fs.ModePerm
	return before&relevant == after&relevant
}
