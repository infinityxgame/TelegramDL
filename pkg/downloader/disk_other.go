//go:build !windows

package downloader

func getPlatformDiskSpace(path string) (free int64, total int64, err error) {
	// Fallback por defecto en entornos no-Windows si no se incluye unix.Statfs
	return 100 * 1024 * 1024 * 1024, 500 * 1024 * 1024 * 1024, nil
}
