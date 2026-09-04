package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"tgdown/pkg/config"
)

type Progress struct {
	Status     string `json:"status"`
	Downloaded int64  `json:"downloaded"`
	Total      int64  `json:"total"`
	Percentage int    `json:"percentage"`
}

type ReleaseAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

type ReleaseInfo struct {
	TagName string         `json:"tag_name"`
	Body    string         `json:"body"`
	Assets  []ReleaseAsset `json:"assets"`
}

type AppUpdater struct {
	currentVersion string
	repoURL        string
	baseDir        string
	tempDir        string

	mu       sync.RWMutex
	progress Progress
}

func NewAppUpdater() *AppUpdater {
	config.InitPaths()
	return &AppUpdater{
		currentVersion: config.AppVersion,
		repoURL:        config.GithubRepo,
		baseDir:        config.BaseDir,
		tempDir:        filepath.Join(config.BaseDir, "update_temp"),
		progress: Progress{
			Status: "idle",
		},
	}
}

func (u *AppUpdater) GetProgress() Progress {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.progress
}

func (u *AppUpdater) setProgress(status string, downloaded, total int64) {
	u.mu.Lock()
	defer u.mu.Unlock()

	pct := 0
	if total > 0 {
		pct = int(float64(downloaded) / float64(total) * 100.0)
		if pct > 100 {
			pct = 100
		}
	}

	u.progress = Progress{
		Status:     status,
		Downloaded: downloaded,
		Total:      total,
		Percentage: pct,
	}
}

func versionToTuple(v string) (int, int, int) {
	v = strings.TrimPrefix(v, "v")
	parts := strings.Split(v, ".")
	var nums [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		nums[i], _ = strconv.Atoi(parts[i])
	}
	return nums[0], nums[1], nums[2]
}

func isNewer(latest, current string) bool {
	l1, l2, l3 := versionToTuple(latest)
	c1, c2, c3 := versionToTuple(current)
	if l1 != c1 {
		return l1 > c1
	}
	if l2 != c2 {
		return l2 > c2
	}
	return l3 > c3
}

func (u *AppUpdater) CheckForUpdate() (*ReleaseInfo, *ReleaseAsset, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", u.repoURL)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "TGDown-Updater")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("GitHub API devolvió código %d", resp.StatusCode)
	}

	var rel ReleaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, nil, err
	}

	if isNewer(rel.TagName, u.currentVersion) {
		asset := u.findAssetForPlatform(rel.Assets)
		if asset != nil {
			return &rel, asset, nil
		}
	}

	return nil, nil, nil
}

func (u *AppUpdater) findAssetForPlatform(assets []ReleaseAsset) *ReleaseAsset {
	sys := runtime.GOOS

	// 1. Coincidencia específica por plataforma
	for _, a := range assets {
		name := strings.ToLower(a.Name)
		if sys == "windows" {
			if strings.HasSuffix(name, ".zip") || strings.HasSuffix(name, ".exe") {
				if strings.Contains(name, "win") || strings.Contains(name, "windows") {
					return &a
				}
			}
		} else if sys == "linux" {
			if strings.HasSuffix(name, ".appimage") || (strings.HasSuffix(name, ".zip") && strings.Contains(name, "linux")) {
				return &a
			}
		} else if sys == "darwin" {
			if strings.HasSuffix(name, ".zip") && (strings.Contains(name, "mac") || strings.Contains(name, "darwin") || strings.Contains(name, "osx") || strings.Contains(name, "apple")) {
				return &a
			}
		}
	}

	// 2. Coincidencia genérica excluyendo otras plataformas
	for _, a := range assets {
		name := strings.ToLower(a.Name)
		if !strings.HasSuffix(name, ".zip") && !strings.HasSuffix(name, ".appimage") && !strings.HasSuffix(name, ".exe") && !strings.HasSuffix(name, ".tar.gz") {
			continue
		}

		if sys == "windows" {
			if !strings.Contains(name, "mac") && !strings.Contains(name, "darwin") && !strings.Contains(name, "osx") && !strings.Contains(name, "linux") && !strings.Contains(name, "appimage") {
				return &a
			}
		} else if sys == "darwin" {
			if !strings.Contains(name, "win") && !strings.Contains(name, "windows") && !strings.Contains(name, "linux") && !strings.Contains(name, "appimage") {
				return &a
			}
		} else if sys == "linux" {
			if !strings.Contains(name, "win") && !strings.Contains(name, "windows") && !strings.Contains(name, "mac") && !strings.Contains(name, "darwin") && !strings.Contains(name, "osx") {
				return &a
			}
		}
	}

	// 3. Fallback: primer asset compatible
	if len(assets) > 0 {
		return &assets[0]
	}
	return nil
}

func (u *AppUpdater) InstallUpdate(rel *ReleaseInfo) error {
	asset := u.findAssetForPlatform(rel.Assets)
	if asset == nil {
		return errors.New("no se encontró asset descargable para este sistema operativo")
	}

	go func() {
		u.setProgress("starting", 0, asset.Size)
		_ = os.RemoveAll(u.tempDir)
		_ = os.MkdirAll(u.tempDir, 0755)

		archivePath := filepath.Join(u.tempDir, asset.Name)
		out, err := os.Create(archivePath)
		if err != nil {
			u.setProgress("error: "+err.Error(), 0, 0)
			return
		}

		u.setProgress("downloading", 0, asset.Size)
		resp, err := http.Get(asset.DownloadURL)
		if err != nil {
			out.Close()
			u.setProgress("error: "+err.Error(), 0, 0)
			return
		}
		defer resp.Body.Close()

		var downloaded int64
		buf := make([]byte, 64*1024)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				_, werr := out.Write(buf[:n])
				if werr != nil {
					break
				}
				downloaded += int64(n)
				u.setProgress("downloading", downloaded, asset.Size)
			}
			if rerr != nil {
				break
			}
		}
		out.Close()

		if strings.HasSuffix(strings.ToLower(archivePath), ".appimage") {
			u.setProgress("finishing", asset.Size, asset.Size)
			u.finishAppImageUpdate(archivePath)
			return
		}

		u.setProgress("extracting", downloaded, asset.Size)
		extractPath := filepath.Join(u.tempDir, "extracted")
		_ = os.MkdirAll(extractPath, 0755)

		lowerArch := strings.ToLower(archivePath)
		if strings.HasSuffix(lowerArch, ".zip") {
			_ = unzip(archivePath, extractPath)
		} else if strings.HasSuffix(lowerArch, ".tar.gz") || strings.HasSuffix(lowerArch, ".tgz") {
			_ = untarGz(archivePath, extractPath)
		}

		// Buscar raíz de aplicación (directorio con .app, _internal, o binario ejecutable)
		finalSrc := extractPath
		_ = filepath.Walk(extractPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				name := strings.ToLower(info.Name())
				if name == "telegramdl.app" || name == "tgdown.app" || name == "_internal" {
					finalSrc = filepath.Dir(path)
					if strings.HasSuffix(name, ".app") {
						finalSrc = path
					}
					return filepath.SkipDir
				}
			} else {
				name := strings.ToLower(info.Name())
				if name == "telegramdl.exe" || name == "tgdown.exe" || name == "telegramdl" || name == "tgdown" {
					finalSrc = filepath.Dir(path)
				}
			}
			return nil
		})

		u.setProgress("finishing", asset.Size, asset.Size)
		u.createFinishScript(finalSrc)
	}()

	return nil
}

func (u *AppUpdater) finishAppImageUpdate(newAppImagePath string) {
	runningAppImage := os.Getenv("APPIMAGE")
	if runningAppImage == "" {
		// Modo desarrollo en Linux
		return
	}

	scriptPath := filepath.Join(u.baseDir, "finish_update.sh")
	appImageFilename := filepath.Base(runningAppImage)

	scriptContent := fmt.Sprintf(`#!/bin/bash
sleep 2
pkill -9 -f "%s" 2>/dev/null
mv "%s" "%s"
chmod +x "%s"
"%s" &
rm "$0"
`, appImageFilename, newAppImagePath, runningAppImage, runningAppImage, runningAppImage)

	_ = os.WriteFile(scriptPath, []byte(scriptContent), 0755)
	cmd := exec.Command("/bin/bash", scriptPath)
	_ = cmd.Start()
	time.Sleep(500 * time.Millisecond)
	os.Exit(0)
}

func (u *AppUpdater) createFinishScript(srcPath string) {
	execPath, err := os.Executable()
	if err != nil {
		execPath = filepath.Join(u.baseDir, "tgdown.exe")
	}
	exeName := filepath.Base(execPath)

	if runtime.GOOS == "windows" {
		scriptPath := filepath.Join(u.baseDir, "finish_update.bat")

		batContent := fmt.Sprintf(`@echo off
setlocal enabledelayedexpansion
title Actualizando TelegramDL...
:wait_process
taskkill /f /im "%s" >nul 2>&1
timeout /t 1 /nobreak >nul
tasklist /FI "IMAGENAME eq %s" 2>NUL | find /I /N "%s">NUL
if "%%ERRORLEVEL%%"=="0" goto wait_process

robocopy "%s" "%s" /e /move /is /it /xf .env config.json downloads.json tgdown.sqlite3 tg_session.json downloader_session.session downloader_session.session-journal /xd descargas cache update_temp .git .github /r:5 /w:2 /nfl /ndl /njh /njs > nul
if exist "%s" rd /s /q "%s" >nul 2>&1
start "" "%s"
endlocal
(goto) 2>nul & del "%%~f0"
`, exeName, exeName, exeName, srcPath, u.baseDir, u.tempDir, u.tempDir, execPath)

		_ = os.WriteFile(scriptPath, []byte(batContent), 0644)
		cmd := exec.Command("cmd.exe", "/C", "start", "", scriptPath)
		_ = cmd.Start()
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	} else if runtime.GOOS == "darwin" {
		scriptPath := filepath.Join(u.baseDir, "finish_update.sh")
		targetAppPath := u.baseDir
		if filepath.Base(targetAppPath) == "MacOS" && filepath.Base(filepath.Dir(targetAppPath)) == "Contents" {
			targetAppPath = filepath.Dir(filepath.Dir(targetAppPath))
		}

		scriptContent := fmt.Sprintf(`#!/bin/bash
sleep 2
pkill -9 -f "%s" 2>/dev/null
pkill -9 -f "TelegramDL" 2>/dev/null
pkill -9 -f "tgdown" 2>/dev/null
rm -rf "%s"
cp -R "%s" "%s"
rm -rf "%s"
open "%s"
rm "$0"
`, exeName, targetAppPath, srcPath, filepath.Dir(targetAppPath), u.tempDir, targetAppPath)

		_ = os.WriteFile(scriptPath, []byte(scriptContent), 0755)
		cmd := exec.Command("/bin/bash", scriptPath)
		_ = cmd.Start()
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	} else {
		// Linux estándar
		scriptPath := filepath.Join(u.baseDir, "finish_update.sh")
		scriptContent := fmt.Sprintf(`#!/bin/bash
sleep 2
pkill -9 -f "%s" 2>/dev/null
cp -R "%s"/* "%s"/ 2>/dev/null || cp "%s" "%s"/
chmod +x "%s"
rm -rf "%s"
"%s" &
rm "$0"
`, exeName, srcPath, u.baseDir, srcPath, u.baseDir, execPath, u.tempDir, execPath)

		_ = os.WriteFile(scriptPath, []byte(scriptContent), 0755)
		cmd := exec.Command("/bin/bash", scriptPath)
		_ = cmd.Start()
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}
}

func unzip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) {
			continue
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(fpath, os.ModePerm)
			continue
		}
		if err = os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}
		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}
		_, _ = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
	}
	return nil
}

func untarGz(src, dest string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(target, 0755)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(target), 0755)
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, _ = io.Copy(outFile, tr)
			outFile.Close()
		}
	}
	return nil
}
