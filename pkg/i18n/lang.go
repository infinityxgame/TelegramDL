package i18n

import "strings"

type Lang int

// LangES y LangEN son los únicos idiomas con diccionario.
const (
	langEmpty Lang = iota
	LangES
	LangEN
	langCount
)

const DEFAULT_LANG = LangES

// messages junta los dos diccionarios. Se declara aquí y no en los archivos de
// mensajes para que la referencia quede junto a T().
var messages = map[Lang]map[string]string{
	LangES: messagesES,
	LangEN: messagesEN,
}

func LangFromString(raw string) (lang Lang, ok bool) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(raw, "es"):
		return LangES, true
	case strings.HasPrefix(raw, "en"):
		return LangEN, true
	default:
		return langEmpty, false
	}
}

func (lang Lang) Valid() bool {
	return lang > langEmpty && lang < langCount
}

func (lang Lang) String() string {
	switch lang {
	case LangES:
		return "es"
	case LangEN:
		return "en"
	default:
		return ""
	}
}

// Label da un nombre legible al código de idioma en el registro ("es →
// en" se lee mejor como "Español → English"). El vacío (aún sin elegir) queda
// como el propio código para que se note que no había valor.
func (lang Lang) Label() string {
	switch lang {
	case LangES:
		return "Español"
	case LangEN:
		return "English"
	default:
		return ""
	}
}
