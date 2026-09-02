package main

import (
	"fmt"
	"os"
	"path/filepath"
)

const privateWorkMode = 0o700

func defaultWorkPath(kind string) string {
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cache, "hyrum", kind)
}

// ensurePrivateWorkRoot establishes the default cache root before any
// reusable clone or model-visible workspace is created. Explicit --work paths
// remain operator-controlled and keep their existing permissions.
func ensurePrivateWorkRoot(path string) error {
	if path == "" {
		return fmt.Errorf("determine user cache directory: no cache directory is available; pass --work explicitly")
	}
	if err := os.MkdirAll(path, privateWorkMode); err != nil {
		return fmt.Errorf("create private work root: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect private work root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("refusing non-directory default work root %q", path)
	}
	if err := os.Chmod(path, privateWorkMode); err != nil {
		return fmt.Errorf("secure private work root: %w", err)
	}
	return nil
}
