package downloader

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// Nombres de carpeta para organizar las descargas de la escucha.
//
// La idea: cada chat vigilado baja sus archivos a una subcarpeta con su
// nombre. El título lo elige quien administra el canal, así que puede traer
// cualquier cosa —emojis, marcas que invierten el texto, letras decorativas—.
// Lo que aquí se hace es quedarse con la parte legible del título y, si no
// queda nada legible, usar el ID del chat.
//
// El criterio es amplio a propósito: cirílico, chino, griego o árabe se
// conservan, porque NTFS, APFS y ext4 los guardan sin problema. Lo que se tira
// es la decoración: emojis, pictogramas, símbolos sueltos y los caracteres de
// control de dirección (RLO/LRO), que son los que hacen que el Explorador
// enseñe el nombre al revés.

const (
	// maxFolderRunes es más corto que el de los archivos (200 bytes) porque
	// delante va la carpeta de descargas y detrás el nombre del archivo, y
	// Windows sigue teniendo el tope de 260 caracteres en muchas de sus APIs.
	maxFolderRunes = 60

	// minAlphanumerics es lo mínimo que tiene que quedar tras la limpieza para
	// considerar que el título dice algo. Un título de una sola letra no
	// identifica nada, y casi siempre es el resto de un nombre hecho de
	// símbolos.
	minAlphanumerics = 2
)

// ChatFolderName devuelve el nombre de carpeta de un chat: su título si se
// puede escribir y leer en cualquier sistema, y «chat_<id>» si no.
func ChatFolderName(title string, id int64) string {
	if name := readableName(title); name != "" {
		return name
	}
	return FolderByID(id)
}

// FolderByID es el nombre de respaldo. El ID va en positivo: los canales lo
// tienen negativo y un nombre que empieza por «-» se lo comen como una opción
// más de una herramienta de consola.
func FolderByID(id int64) string {
	if id < 0 {
		id = -id
	}
	return "chat_" + strconv.FormatInt(id, 10)
}

// TopicFolderName es lo mismo para el tema de un grupo, con «tema_<id>» de
// respaldo mientras Telegram no nos haya dado su título.
func TopicFolderName(title string, topicID int64) string {
	if name := readableName(title); name != "" {
		return name
	}
	if topicID < 0 {
		topicID = -topicID
	}
	return "tema_" + strconv.FormatInt(topicID, 10)
}

// FolderNameWithID añade el ID al nombre. Se usa cuando dos chats distintos
// resuelven al mismo nombre de carpeta: el segundo lleva su ID detrás para que
// no compartan destino.
func FolderNameWithID(name string, id int64) string {
	if id < 0 {
		id = -id
	}
	suffix := " (" + strconv.FormatInt(id, 10) + ")"
	base := strings.TrimSpace(name)
	if base == "" {
		return FolderByID(id)
	}
	// El recorte va antes de pegar el sufijo: si no, un nombre ya en el límite
	// dejaría el ID fuera, que es justo lo que distingue a las dos carpetas.
	base = truncateRunes(base, maxFolderRunes-len([]rune(suffix)))
	base = strings.TrimSpace(base)
	if base == "" {
		return FolderByID(id)
	}
	return SanitizeFileName(base + suffix)
}

// SafeRelativePath valida la ruta relativa que viaja con una descarga antes
// de pegarla a la carpeta de destino. Aunque la genera el propio programa, sale
// de un nombre que elige un tercero en Telegram, así que se trata como entrada
// no confiable: nada de rutas absolutas, nada de «..» y como mucho dos niveles
// (grupo y tema).
func SafeRelativePath(sub string) string {
	sub = strings.TrimSpace(sub)
	if sub == "" {
		return ""
	}

	// El separador se normaliza porque la ruta pudo guardarse en la base de
	// datos en otro sistema (una configuración copiada de Windows a Linux).
	sub = strings.ReplaceAll(sub, "\\", "/")

	// Una ruta absoluta no es una subcarpeta de nada: se rechaza entera en vez
	// de recortarla, que es la forma de no convertir «/etc/passwd» en algo que
	// parezca válido.
	if strings.HasPrefix(sub, "/") {
		return ""
	}
	if len(sub) > 1 && sub[1] == ':' {
		return ""
	}

	parts := make([]string, 0, 2)
	for _, part := range strings.Split(sub, "/") {
		part = strings.TrimSpace(part)
		if part == "" || part == "." {
			continue
		}
		// Un «..» no se salta: significa que la ruta viene mal, y lo seguro es
		// quedarse sin subcarpeta y bajar a la raíz de descargas.
		if part == ".." {
			return ""
		}
		part = SanitizeFileName(part)
		if part == "" {
			continue
		}
		parts = append(parts, part)
		if len(parts) == 2 {
			break
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return filepath.Join(parts...)
}

// PathWithin informa de si target queda dentro de base. Se usa para validar
// rutas que vienen de fuera (la importación de descargas) y como última
// barrera antes de tocar el disco. La comparación es léxica sobre rutas
// absolutas: no resuelve enlaces simbólicos, que aquí no intervienen porque la
// ruta se construye con componentes ya saneados y el ataque buscado es el
// escape por «..» o por ruta absoluta.
func PathWithin(base, target string) bool {
	if strings.TrimSpace(base) == "" || strings.TrimSpace(target) == "" {
		return false
	}

	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return false
	}

	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil {
		// Volúmenes distintos en Windows: no hay forma de que esté dentro.
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(rel)
}

// readableName devuelve el título ya limpio y apto para el disco, o cadena
// vacía si de ese título no queda nada que sirva.
func readableName(title string) string {
	clean := collapseSpaces(removeDecoration(title))
	if countAlphanumerics(clean) < minAlphanumerics {
		return ""
	}

	clean = strings.TrimSpace(truncateRunes(clean, maxFolderRunes))
	if clean == "" {
		return ""
	}

	// SanitizeFileName es la misma limpieza que se aplica a los archivos:
	// caracteres que no admite la ruta, nombres reservados de Windows y nada
	// de punto o espacio al final.
	safe := SanitizeFileName(clean)

	// «file» es el comodín que devuelve SanitizeFileName cuando no queda
	// nada; como nombre de carpeta de un chat no dice nada, mejor el ID.
	if safe == "file" && !strings.EqualFold(clean, "file") {
		return ""
	}
	if countAlphanumerics(safe) < minAlphanumerics {
		return ""
	}
	return safe
}

// removeDecoration se queda con la parte del título que es texto de verdad.
func removeDecoration(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	combining := 0
	for _, r := range s {
		// Las letras «decorativas» matemáticas (𝓒𝓪𝓷𝓪𝓵, 𝗖𝗮𝗻𝗮𝓵) se convierten a su
		// letra normal en vez de tirarse: son títulos perfectamente legibles
		// escritos con un alfabeto raro, y así no acaban en el ID.
		if letter, ok := mathLetter(r); ok {
			b.WriteRune(letter)
			combining = 0
			continue
		}

		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
			combining = 0

		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r):
			// Marcas combinantes: una por letra base. Más de una es el truco
			// del «Zalgo», texto sepultado bajo cientos de tildes.
			if combining < 1 {
				b.WriteRune(r)
				combining++
			}

		case isDecorative(r):
			// Se sustituye por un espacio para no pegar dos palabras que solo
			// estaban separadas por el emoji de en medio.
			b.WriteRune(' ')
			combining = 0

		default:
			b.WriteRune(r)
			combining = 0
		}
	}

	return b.String()
}

// isDecorative distingue lo que no es texto: emojis, pictogramas, símbolos
// sueltos y los caracteres de control e invisibles.
func isDecorative(r rune) bool {
	switch {
	case r < 0x20 || r == 0x7f:
		return true
	// Cf incluye las marcas de dirección (RLO, LRO, PDF) y los anchos cero.
	// Son las peligrosas de verdad: no se ven, pero le dan la vuelta al nombre
	// en el Explorador.
	case unicode.Is(unicode.Cc, r), unicode.Is(unicode.Cf, r),
		unicode.Is(unicode.Cs, r), unicode.Is(unicode.Co, r):
		return true
	// So son los pictogramas y dingbats; Sk los modificadores (tonos de piel,
	// acentos sueltos con forma de símbolo).
	case unicode.Is(unicode.So, r), unicode.Is(unicode.Sk, r):
		return true
	case r >= 0xFE00 && r <= 0xFE0F: // selectores de variación
		return true
	case r >= 0x1F000 && r <= 0x1FAFF: // emoji y pictogramas
		return true
	case r >= 0xE0000 && r <= 0xE007F: // etiquetas invisibles
		return true
	}
	return false
}

// mathAlphabets son los bloques de letras y números decorativos del
// plano 1, cada uno con la letra por la que empieza. Los huecos del bloque
// (la «h» cursiva vive fuera, en ℎ) no hacen falta: esos caracteres ya son
// letras normales y pasan el filtro por su cuenta.
var mathAlphabets = []struct {
	start  rune
	base   rune
	length rune
}{
	{0x1D400, 'A', 26}, {0x1D41A, 'a', 26}, // negrita
	{0x1D434, 'A', 26}, {0x1D44E, 'a', 26}, // cursiva
	{0x1D468, 'A', 26}, {0x1D482, 'a', 26}, // negrita cursiva
	{0x1D49C, 'A', 26}, {0x1D4B6, 'a', 26}, // caligráfica
	{0x1D4D0, 'A', 26}, {0x1D4EA, 'a', 26}, // caligráfica negrita
	{0x1D504, 'A', 26}, {0x1D51E, 'a', 26}, // gótica
	{0x1D538, 'A', 26}, {0x1D552, 'a', 26}, // hueca
	{0x1D56C, 'A', 26}, {0x1D586, 'a', 26}, // gótica negrita
	{0x1D5A0, 'A', 26}, {0x1D5BA, 'a', 26}, // palo seco
	{0x1D5D4, 'A', 26}, {0x1D5EE, 'a', 26}, // palo seco negrita
	{0x1D608, 'A', 26}, {0x1D622, 'a', 26}, // palo seco cursiva
	{0x1D63C, 'A', 26}, {0x1D656, 'a', 26}, // palo seco negrita cursiva
	{0x1D670, 'A', 26}, {0x1D68A, 'a', 26}, // monoespaciada
	{0x1D7CE, '0', 10}, {0x1D7D8, '0', 10}, // cifras negrita y huecas
	{0x1D7E2, '0', 10}, {0x1D7EC, '0', 10}, // cifras palo seco
	{0x1D7F6, '0', 10}, // cifras monoespaciadas
}

// mathLetter traduce una letra decorativa a su equivalente normal.
func mathLetter(r rune) (rune, bool) {
	if r < 0x1D400 || r > 0x1D7FF {
		return 0, false
	}
	for _, block := range mathAlphabets {
		if r >= block.start && r < block.start+block.length {
			return block.base + (r - block.start), true
		}
	}
	return 0, false
}

// collapseSpaces deja un solo espacio entre palabras y quita los de los
// extremos. Después de quitar la decoración suelen quedar huecos dobles.
func collapseSpaces(s string) string {
	fields := strings.FieldsFunc(s, unicode.IsSpace)
	return strings.Join(fields, " ")
}

// countAlphanumerics cuenta las runas que son letra o número de algún sistema
// de escritura real. Es la prueba de «esto dice algo».
func countAlphanumerics(s string) int {
	total := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			total++
		}
	}
	return total
}

// ChatFolderText es el contenido del archivito que se deja dentro de la
// carpeta cuando el nombre no dice de qué chat es. Sirve para reconocerla
// desde el Explorador sin abrir el programa.
func ChatFolderText(title string, id int64, topic string) string {
	var b strings.Builder
	b.WriteString("Carpeta creada por TelegramDL\r\n")
	b.WriteString(fmt.Sprintf("Chat: %s\r\n", strings.TrimSpace(title)))
	b.WriteString(fmt.Sprintf("ID: %d\r\n", id))
	if topic = strings.TrimSpace(topic); topic != "" {
		b.WriteString(fmt.Sprintf("Tema: %s\r\n", topic))
	}
	return b.String()
}
