package rolesource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBoundedFSReadsRegularFile(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "roles", "PROFILE.yaml"), []byte("id: writer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenBoundedFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	body, err := reader.ReadFile("roles/PROFILE.yaml", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(body), "id: writer\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestBoundedFSRejectsUnsafeNames(t *testing.T) {
	reader, err := OpenBoundedFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	for _, name := range []string{"../secret", "/etc/passwd", `C:\\secret`, `roles\\PROFILE.yaml`, "roles/../secret", "."} {
		t.Run(name, func(t *testing.T) {
			if _, err := reader.ReadFile(name, 1024); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("ReadFile(%q) error = %v, want ErrUnsafePath", name, err)
			}
		})
	}
}

func TestBoundedFSRejectsRootSymlink(t *testing.T) {
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "source")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := OpenBoundedFS(link); !errors.Is(err, ErrSymlink) {
		t.Fatalf("OpenBoundedFS error = %v, want ErrSymlink", err)
	}
}

func TestBoundedFSRejectsIntermediateAndFinalSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "secret-link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	reader, err := OpenBoundedFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	for _, name := range []string{"outside/secret", "secret-link"} {
		if _, err := reader.ReadFile(name, 1024); !errors.Is(err, ErrSymlink) {
			t.Fatalf("ReadFile(%q) error = %v, want ErrSymlink", name, err)
		}
	}
}

func TestBoundedFSRejectsDirectoryAsFile(t *testing.T) {
	reader, err := OpenBoundedFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if _, err := reader.ReadFile(".", 1024); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("ReadFile error = %v, want ErrUnsafePath", err)
	}
}

func TestBoundedFSAppliesSizeLimit(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenBoundedFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if _, err := reader.ReadFile("large.txt", 4); !errors.Is(err, ErrFileLimitExceeded) {
		t.Fatalf("ReadFile error = %v, want ErrFileLimitExceeded", err)
	}
}

func TestBoundedFSReadDirDoesNotTraverseSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	reader, err := OpenBoundedFS(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	entries, err := reader.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Type()&os.ModeSymlink == 0 {
		t.Fatalf("ReadDir entries = %#v, want one reported symlink", entries)
	}
	if _, err := reader.ReadDir("linked"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("ReadDir(linked) error = %v, want ErrSymlink", err)
	}
}
