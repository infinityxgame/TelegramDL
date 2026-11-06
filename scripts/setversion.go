package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// fileRule define una regla de reemplazo de versión para un archivo específico
type fileRule struct {
	path    string
	regex   *regexp.Regexp
	replace string
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Uso: go run scripts/setversion.go <version>")
		fmt.Println("Ejemplo: go run scripts/setversion.go 2.7.1")
		fmt.Println("O bien:  version.cmd 2.7.1")
		os.Exit(1)
	}

	rawVersion := os.Args[1]
	if rawVersion == "-v" || rawVersion == "--version" {
		if len(os.Args) < 3 {
			fmt.Println("Error: Debe especificar el número de versión.")
			os.Exit(1)
		}
		rawVersion = os.Args[2]
	}

	version := strings.TrimPrefix(strings.TrimSpace(rawVersion), "v")
	if version == "" {
		fmt.Println("Error: La versión especificada no es válida.")
		os.Exit(1)
	}

	fmt.Printf("Actualizando versión a '%s' en todos los archivos del proyecto...\n\n", version)

	rules := []fileRule{
		{
			path:    "pkg/config/config.go",
			regex:   regexp.MustCompile(`AppVersion\s*=\s*"[^"]+"`),
			replace: fmt.Sprintf(`AppVersion = "%s"`, version),
		},
		{
			path:    "wails.json",
			regex:   regexp.MustCompile(`"productVersion"\s*:\s*"[^"]+"`),
			replace: fmt.Sprintf(`"productVersion": "%s"`, version),
		},
		{
			path:    "build/config.yml",
			regex:   regexp.MustCompile(`(?m)^(\s*version:\s*)"[^"]+"`),
			replace: fmt.Sprintf(`${1}"%s"`, version),
		},
		{
			path:    "build/windows/info.json",
			regex:   regexp.MustCompile(`"fileVersion"\s*:\s*"[^"]+"`),
			replace: fmt.Sprintf(`"fileVersion": "%s"`, version),
		},
		{
			path:    "build/windows/info.json",
			regex:   regexp.MustCompile(`"productVersion"\s*:\s*"[^"]+"`),
			replace: fmt.Sprintf(`"productVersion": "%s"`, version),
		},
		{
			path:    "build/darwin/Info.plist",
			regex:   regexp.MustCompile(`(<key>CFBundleVersion</key>\s*<string>)[^<]+(</string>)`),
			replace: fmt.Sprintf(`${1}%s${2}`, version),
		},
		{
			path:    "build/darwin/Info.plist",
			regex:   regexp.MustCompile(`(<key>CFBundleShortVersionString</key>\s*<string>)[^<]+(</string>)`),
			replace: fmt.Sprintf(`${1}%s${2}`, version),
		},
		{
			path:    "build/darwin/Info.dev.plist",
			regex:   regexp.MustCompile(`(<key>CFBundleVersion</key>\s*<string>)[^<]+(</string>)`),
			replace: fmt.Sprintf(`${1}%s${2}`, version),
		},
		{
			path:    "build/darwin/Info.dev.plist",
			regex:   regexp.MustCompile(`(<key>CFBundleShortVersionString</key>\s*<string>)[^<]+(</string>)`),
			replace: fmt.Sprintf(`${1}%s${2}`, version),
		},
	}

	updatedFiles := make(map[string]bool)

	for _, r := range rules {
		content, err := os.ReadFile(r.path)
		if err != nil {
			fmt.Printf("[ERROR] No se pudo leer %s: %v\n", r.path, err)
			continue
		}

		newContent := r.regex.ReplaceAllString(string(content), r.replace)
		if newContent == string(content) {
			fmt.Printf("[INFO] Sin cambios en %s (versión idéntica o coincidencia no encontrada)\n", r.path)
			continue
		}

		err = os.WriteFile(r.path, []byte(newContent), 0644)
		if err != nil {
			fmt.Printf("[ERROR] No se pudo escribir %s: %v\n", r.path, err)
			continue
		}

		if !updatedFiles[r.path] {
			updatedFiles[r.path] = true
			fmt.Printf("[OK] Actualizado %s\n", r.path)
		}
	}

	fmt.Printf("\nProceso finalizado. Se actualizaron %d archivos a la versión %s.\n", len(updatedFiles), version)
}
