package downloader

import (
	"path/filepath"

	"tgdown/pkg/config"
)

type DiskInfo struct {
	Total         string `json:"total"`
	Free          string `json:"free"`
	ProjectedFree int64  `json:"projected_free"`
	TotalBytes    int64  `json:"total_bytes"`
	FreeBytes     int64  `json:"free_bytes"`
}

func GetDiskUsage(path string) (*DiskInfo, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	free, total, err := getPlatformDiskSpace(absPath)
	if err != nil {
		return nil, err
	}

	return &DiskInfo{
		Total:         config.FormatBytes(float64(total)),
		Free:          config.FormatBytes(float64(free)),
		ProjectedFree: free,
		TotalBytes:    total,
		FreeBytes:     free,
	}, nil
}
