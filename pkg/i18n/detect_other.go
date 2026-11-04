//go:build !windows

package i18n

// DetectSystemLanguage mira las variables de entorno de locale habituales en
// Linux y macOS ("es_ES.UTF-8", "en_US"...) y las reduce al idioma del panel:
// español si empieza por "es", inglés en cualquier otro caso (incluido el
// vacío, que en un Mac configurado en inglés es lo habitual bajo launchd).
func DetectSystemLanguage() Lang {
	return detectSystemLanguage()
}
