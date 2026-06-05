package img

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MoveToTrash moves src into trashDir, preserving relative structure.
// If a collision occurs, a timestamp suffix is appended. Returns the
// destination path actually used.
func MoveToTrash(src, photoDir, trashDir string) (string, error) {
	rel, err := filepath.Rel(photoDir, src)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(trashDir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	dst = uniqueDest(dst)
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("move to trash: %w", err)
	}
	return dst, nil
}

// RestoreFromTrash moves trashedPath back to its original src location.
func RestoreFromTrash(trashedPath, originalPath string) error {
	if _, err := os.Stat(trashedPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("trashed file missing: %s", trashedPath)
	}
	if err := os.MkdirAll(filepath.Dir(originalPath), 0o755); err != nil {
		return err
	}
	return os.Rename(trashedPath, originalPath)
}

func uniqueDest(dst string) string {
	if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
		return dst
	}
	ext := filepath.Ext(dst)
	base := dst[:len(dst)-len(ext)]
	suffix := time.Now().Format("20060102-150405")
	candidate := fmt.Sprintf("%s.%s%s", base, suffix, ext)
	for i := 1; ; i++ {
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
		candidate = fmt.Sprintf("%s.%s-%d%s", base, suffix, i, ext)
	}
}
