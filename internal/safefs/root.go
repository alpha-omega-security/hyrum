// Package safefs provides rooted file operations for directories that may
// contain paths controlled by an untrusted process.
package safefs

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Root confines all operations to Path. Call Close when finished.
type Root struct {
	path string
	root *os.Root
}

// Open opens an existing real directory as a rooted filesystem. The final
// path itself must not be a symlink.
func Open(path string) (*Root, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("refusing non-directory root %q", path)
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &Root{path: path, root: r}, nil
}

// OpenOrCreate creates path when absent and opens it as a rooted filesystem.
func OpenOrCreate(path string, perm fs.FileMode) (*Root, error) {
	if err := os.MkdirAll(path, perm); err != nil {
		return nil, err
	}
	return Open(path)
}

func (r *Root) Close() error { return r.root.Close() }
func (r *Root) Path() string { return r.path }

func (r *Root) Lstat(name string) (fs.FileInfo, error) {
	clean, err := local(name)
	if err != nil {
		return nil, err
	}
	return r.root.Lstat(clean)
}

func (r *Root) MkdirAll(name string, perm fs.FileMode) error {
	clean, err := local(name)
	if err != nil {
		return err
	}
	return r.root.MkdirAll(clean, perm)
}

// Sub opens name beneath r as a new rooted filesystem.
func (r *Root) Sub(name string) (*Root, error) {
	clean, err := local(name)
	if err != nil {
		return nil, err
	}
	sub, err := r.root.OpenRoot(clean)
	if err != nil {
		return nil, err
	}
	return &Root{path: filepath.Join(r.path, clean), root: sub}, nil
}

func (r *Root) Remove(name string) error {
	clean, err := local(name)
	if err != nil {
		return err
	}
	return r.root.Remove(clean)
}

func (r *Root) RemoveAll(name string) error {
	clean, err := local(name)
	if err != nil {
		return err
	}
	if clean == "." {
		return fmt.Errorf("refusing to remove root")
	}
	return r.root.RemoveAll(clean)
}

func (r *Root) Symlink(target, name string) error {
	clean, err := localFile(name)
	if err != nil {
		return err
	}
	return r.root.Symlink(target, clean)
}

// WriteFile atomically replaces name. Replacing the directory entry instead
// of opening name for writing means a planted final symlink is never followed.
func (r *Root) WriteFile(name string, data []byte, perm fs.FileMode) error {
	clean, err := localFile(name)
	if err != nil {
		return err
	}
	parent := filepath.Dir(clean)
	if err := r.root.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := temporaryName(parent)
	if err != nil {
		return err
	}
	f, err := r.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		_ = f.Close()
		if removeTemp {
			_ = r.root.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := r.root.Rename(tmp, clean); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func (r *Root) WriteJSON(name string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return r.WriteFile(name, b, 0o644)
}

// ReadRegular reads name only when its directory entry is a regular file.
// os.Root additionally prevents any intermediate symlink from escaping r.
func (r *Root) ReadRegular(name string) ([]byte, error) {
	clean, err := localFile(name)
	if err != nil {
		return nil, err
	}
	info, err := r.root.Lstat(clean)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular file %q", name)
	}
	f, err := r.root.Open(clean)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular file %q", name)
	}
	return io.ReadAll(f)
}

// ReadRegularWithin is like ReadRegular but permits symbolic links whose
// complete resolution remains within r and names a regular file.
func (r *Root) ReadRegularWithin(name string) ([]byte, error) {
	clean, err := localFile(name)
	if err != nil {
		return nil, err
	}
	info, err := r.root.Stat(clean)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular file %q", name)
	}
	f, err := r.root.Open(clean)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("refusing non-regular file %q", name)
	}
	return io.ReadAll(f)
}

func local(name string) (string, error) {
	clean := filepath.Clean(name)
	if !filepath.IsLocal(clean) {
		return "", fmt.Errorf("refusing path %q", name)
	}
	return clean, nil
}

func localFile(name string) (string, error) {
	clean, err := local(name)
	if err != nil {
		return "", err
	}
	if clean == "." {
		return "", fmt.Errorf("refusing file path %q", name)
	}
	return clean, nil
}

func temporaryName(parent string) (string, error) {
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return filepath.Join(parent, ".hyrum-"+hex.EncodeToString(id[:])+".tmp"), nil
}
