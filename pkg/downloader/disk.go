package downloader

import (
	"math"
	"path/filepath"

	"tgdown/pkg/config"
	"tgdown/pkg/storage"
)

type DiskInfo struct {
	Total            int64   `json:"total"`
	Free             int64   `json:"free"`
	ProjectedFree    int64   `json:"projected_free"`
	TotalStr         string  `json:"total_str"`
	FreeStr          string  `json:"free_str"`
	ProjectedFreeStr string  `json:"projected_free_str"`
	Percent          float64 `json:"percent"`
	Status           string  `json:"status"`
}

func GetDiskUsage(path string, activeDownloads ...storage.DownloadItem) (*DiskInfo, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	free, total, err := getPlatformDiskSpace(absPath)
	if err != nil {
		return nil, err
	}

	var projectedNeeded int64
	for _, item := range activeDownloads {
		if item.Status == "pending" || item.Status == "queued" || item.Status == "downloading" || item.Status == "paused" {
			remaining := item.TotalBytes - item.CurrentBytes
			if remaining > 0 {
				projectedNeeded += remaining
			}
		}
	}

	projectedFree := free - projectedNeeded
	displayFree := projectedFree
	if displayFree < 0 {
		displayFree = 0
	}

	percent := 0.0
	if total > 0 {
		percent = (1.0 - (float64(displayFree) / float64(total))) * 100.0
	}
	percent = math.Round(percent*10) / 10

	status := "green"
	if total > 0 && (float64(displayFree)/float64(total)) <= 0.1 {
		status = "red"
	}

	projFreeStr := config.FormatBytes(float64(projectedFree))
	if projectedFree < 0 {
		projFreeStr = "-" + config.FormatBytes(float64(-projectedFree))
	}

	return &DiskInfo{
		Total:            total,
		Free:             free,
		ProjectedFree:    projectedFree,
		TotalStr:         config.FormatBytes(float64(total)),
		FreeStr:          config.FormatBytes(float64(free)),
		ProjectedFreeStr: projFreeStr,
		Percent:          percent,
		Status:           status,
	}, nil
}
