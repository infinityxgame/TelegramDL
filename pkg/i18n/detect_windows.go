//go:build windows

package i18n

import (
	"golang.org/x/sys/windows/registry"
)

// DetectSystemLanguage averigua el idioma de Windows leyendo del registro el
// locale del usuario ("es-ES", "en-US"...). Los procesos de escritorio no
// heredan LANG, así que la variable de entorno solo es un fallback (sirve para
// Cygwin/MSYS o un arranque forzado). Cualquier idioma que no sea español se
// traduce a inglés.
func DetectSystemLanguage() Lang {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Control Panel\International`, registry.QUERY_VALUE)
	if err == nil {
		defer key.Close()
		if name, _, err := key.GetStringValue("LocaleName"); err == nil {
			if lang, ok := LangFromString(name); ok {
				return lang
			}
		}
	}

	return detectSystemLanguage()
}
