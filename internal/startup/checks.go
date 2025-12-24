package startup

import (
	"fmt"
	"os"
	"os/exec"
)

type Requirements struct {
	Smartctl string
	Nvme     string
	Zpool    string
}

func RunChecks(req Requirements) error {
	if err := ensureBinary(req.Smartctl); err != nil {
		return err
	}
	if err := ensureBinary(req.Nvme); err != nil {
		return err
	}
	if err := ensureBinary(req.Zpool); err != nil {
		return err
	}
	return nil
}

func ensureBinary(name string) error {
	if name == "" {
		return fmt.Errorf("binary not specified")
	}
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("required binary not found: %s", name)
	}
	return nil
}

func EnsurePaths(paths ...string) error {
	for _, p := range paths {
		if p == "" {
			continue
		}
		dir := p
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			dir = p
		} else {
			dir = dirOf(p)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("cannot create dir %s: %w", dir, err)
		}
	}
	return nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}

