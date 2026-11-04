package i18n

import (
	"os"
)

// DetectSystemLanguage mira las variables de entorno de locale habituales en
// Linux y macOS ("es_ES.UTF-8", "en_US"...) y las reduce al idioma del panel:
// español si empieza por "es", inglés en cualquier otro caso (incluido el
// vacío, que en un Mac configurado en inglés es lo habitual bajo launchd).
func detectSystemLanguage() Lang {
	for _, name := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if lang, ok := LangFromString(os.Getenv(name)); ok {
			return lang
		}
	}
	return langEmpty
}
