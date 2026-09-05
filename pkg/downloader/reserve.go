package downloader

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type PathReservations struct {
	mu       sync.Mutex
	reserved map[string]bool
}

func NewPathReservations() *PathReservations {
	return &PathReservations{
		reserved: make(map[string]bool),
	}
}

func (pr *PathReservations) ReservePath(folder, name string, msgID int64, expectedSize int64, allowExisting bool) (finalPath, finalName string, alreadyExists bool) {
	pr.mu.Lock()
	defer pr.mu.Unlock()

	safeName := SanitizeFileName(name)
	if safeName == "" {
		safeName = fmt.Sprintf("file_%d", msgID)
	}

	ext := filepath.Ext(safeName)
	stem := strings.TrimSuffix(safeName, ext)

	candidateName := safeName
	candidatePath := filepath.Join(folder, candidateName)
	index := 1

	for {
		fi, err := os.Stat(candidatePath)
		pathIsReserved := pr.reserved[candidatePath]

		if err == nil && !pathIsReserved {
			// El archivo ya existe físicamente en disco
			if !allowExisting && expectedSize > 0 && fi.Size() == expectedSize {
				return candidatePath, candidateName, true
			}
			// Si tiene tamaño distinto o está reservado, probamos siguiente sufijo
			candidateName = fmt.Sprintf("%s_%d%s", stem, index, ext)
			candidatePath = filepath.Join(folder, candidateName)
			index++
			continue
		}

		if pathIsReserved {
			candidateName = fmt.Sprintf("%s_%d%s", stem, index, ext)
			candidatePath = filepath.Join(folder, candidateName)
			index++
			continue
		}

		// Encontrado nombre disponible no reservado
		pr.reserved[candidatePath] = true
		return candidatePath, candidateName, false
	}
}

func (pr *PathReservations) ReleasePath(path string) {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	delete(pr.reserved, path)
}
