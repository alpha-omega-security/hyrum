package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alpha-omega-security/hyrum/internal/safefs"
)

func writeWorkspaceJSON(ws, name string, value any) error {
	root, err := safefs.Open(ws)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.WriteJSON(name, value)
}

func writeWorkspaceFile(ws, name string, data []byte) error {
	root, err := safefs.Open(ws)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.WriteFile(name, data, 0o644)
}

func readWorkspaceFile(ws, name string) ([]byte, error) {
	root, err := safefs.Open(ws)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadRegular(name)
}

func removeWorkspacePath(ws, name string, recursive bool) error {
	root, err := safefs.Open(ws)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if recursive {
		return root.RemoveAll(name)
	}
	err = root.Remove(name)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func makeWorkspaceDirectory(ws, name string) error {
	root, err := safefs.Open(ws)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.MkdirAll(name, 0o755)
}

func prepareWorkspaceUnder(workRoot, rel string) (string, error) {
	root, err := safefs.OpenOrCreate(workRoot, 0o755)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(rel, 0o755); err != nil {
		return "", err
	}
	workspace, err := root.Sub(rel)
	if err != nil {
		return "", err
	}
	defer func() { _ = workspace.Close() }()
	for _, name := range transientWorkspaceFiles {
		if err := workspace.Remove(name); err != nil && !os.IsNotExist(err) {
			return "", fmt.Errorf("remove stale %s: %w", name, err)
		}
	}
	for _, name := range transientWorkspaceDirs {
		if err := workspace.RemoveAll(name); err != nil {
			return "", fmt.Errorf("remove stale %s: %w", name, err)
		}
	}
	return workspace.Path(), nil
}

func prepareExternalCheckout(workRoot, rel string) (string, error) {
	root, err := safefs.OpenOrCreate(workRoot, 0o755)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if err := root.MkdirAll(filepath.Dir(rel), privateWorkMode); err != nil {
		return "", err
	}
	if _, err := root.Lstat(rel); err == nil {
		sub, err := root.Sub(rel)
		if err != nil {
			return "", fmt.Errorf("refusing unsafe dependency checkout %q: %w", rel, err)
		}
		_ = sub.Close()
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return filepath.Join(root.Path(), rel), nil
}

func linkWorkspaceDirectory(ws, name, target string) error {
	root, err := safefs.Open(ws)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := root.RemoveAll(name); err != nil {
		return err
	}
	return root.Symlink(target, name)
}
