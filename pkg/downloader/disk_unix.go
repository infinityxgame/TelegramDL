//go:build !windows

package downloader

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func getPlatformDiskSpace(path string) (free int64, total int64, err error) {
	absPath, err := filepath.Abs(path)
	if err == nil {
		path = absPath
	}

	// Encontrar el primer directorio existente hacia arriba
	for {
		if fi, statErr := os.Stat(path); statErr == nil && fi.IsDir() {
			break
		}
		parent := filepath.Dir(path)
		if parent == path || parent == "" {
			break
		}
		path = parent
	}

	var stat unix.Statfs_t
	err = unix.Statfs(path, &stat)
	if err != nil {
		return 0, 0, err
	}

	// Available blocks * block size
	free = int64(stat.Bavail) * int64(stat.Bsize)
	// Total blocks * block size
	total = int64(stat.Blocks) * int64(stat.Bsize)

	return free, total, nil
}
