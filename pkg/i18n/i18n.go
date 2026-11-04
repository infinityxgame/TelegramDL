// Package i18n traduce los mensajes que la aplicación escribe en el registro
// de actividad (la vista de logs del panel, su copia en disco y la consola del
// modo --server).
//
// El panel tiene su propio i18n en JavaScript para la interfaz; este paquete
// solo existe porque los eventos del registro los redacta el backend en Go.
// El diccionario en español es la fuente de verdad: cualquier clave que falte
// en inglés se resuelve en español, y una clave desconocida se devuelve tal
// cual (delata el error en el registro en vez de romperlo).
//
// El idioma activo lo fija SetLanguage: al arrancar se detecta el del sistema
// (DetectSystemLanguage) y en cuanto se carga la configuración manda el que el
// usuario haya elegido en Ajustes. Cambiarlo solo afecta a las entradas nuevas:
// lo ya escrito en el registro y en el archivo queda en su idioma original.
package i18n

import (
	"fmt"
	"sync/atomic"
)

var current atomic.Value // string; LangES o LangEN

func init() {
	current.Store(DEFAULT_LANG)
}

// SetLanguage fija el idioma activo. Cualquier valor que no sea "es" o "en"
// (vacío incluido) deja el idioma como estaba: es lo que permite que la
// detección del sistema no sea pisada por una configuración todavía vacía.
func SetLanguage(lang Lang) {
	if lang.Valid() {
		current.Store(lang)
	}
}

// Language devuelve el idioma activo.
func Language() Lang {
	if v, ok := current.Load().(Lang); ok {
		return v
	}
	return DEFAULT_LANG
}

// T traduce una clave y la formatea con los argumentos (mismo estilo que
// fmt.Sprintf: los textos llevan verbos %s, %d, %v...). Sin argumentos no se
// formatea nada, así los textos con % literales no se corrompen.
func T(key string, args ...any) string {
	lang := Language()
	text, ok := messages[lang][key]
	if !ok {
		text, ok = messages[DEFAULT_LANG][key]
	}
	if !ok {
		return key
	}
	if len(args) == 0 {
		return text
	}
	return fmt.Sprintf(text, args...)
}
