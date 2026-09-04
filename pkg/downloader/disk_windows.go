//go:build windows

package downloader

import (
	"golang.org/x/sys/windows"
)

func getPlatformDiskSpace(path string) (free int64, total int64, err error) {
	var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes uint64
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, err
	}

	err = windows.GetDiskFreeSpaceEx(p, &freeBytesAvailable, &totalNumberOfBytes, &totalNumberOfFreeBytes)
	if err != nil {
		return 0, 0, err
	}

	return int64(freeBytesAvailable), int64(totalNumberOfBytes), nil
}
