package img

import (
	"io/fs"
	"path/filepath"
	"strings"

	"photoSwipe/internal/store"
)

// supportedExts lists image formats decodable by the Go standard library
// plus golang.org/x/image. HEIC is intentionally excluded — it would
// require linking libheif and break the FROM-scratch container.
var supportedExts = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".webp": true,
}

// Scan walks photoDir and registers every supported image with the store.
// Files inside trashDir (or directories starting with ".") are skipped.
// Returns counts: (added, total seen).
func Scan(photoDir, trashDir string, st *store.Store) (added, total int, err error) {
	seen := map[string]struct{}{}
	absTrash, _ := filepath.Abs(trashDir)

	err = filepath.WalkDir(photoDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") && path != photoDir {
				return fs.SkipDir
			}
			abs, _ := filepath.Abs(path)
			if absTrash != "" && abs == absTrash {
				return fs.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		if !supportedExts[ext] {
			return nil
		}
		rel, err := filepath.Rel(photoDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		info, err := d.Info()
		if err != nil {
			return err
		}
		_, isNew, err := st.UpsertPhoto(rel, info.Size(), info.ModTime())
		if err != nil {
			return err
		}
		if isNew {
			added++
		}
		seen[store.PhotoID(rel)] = struct{}{}
		total++
		return nil
	})
	if err != nil {
		return added, total, err
	}
	// Prune photos that no longer exist on disk (and weren't trashed).
	_, _ = st.ForgetMissing(seen)
	return added, total, nil
}
