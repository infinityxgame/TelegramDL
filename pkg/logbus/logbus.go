// Package logbus centraliza el registro de actividad de TelegramDL.
//
// Cumple tres funciones a la vez:
//
//  1. Captura todo lo que la aplicación escribe con el paquete estándar "log",
//     sin necesidad de tocar las llamadas ya existentes: basta con llamar a
//     Attach() una vez al arrancar.
//  2. Ofrece Emit/Debug/Info/Success/Warn/Error para registrar eventos con más
//     contexto del que cabe en una línea suelta (qué archivo, de qué chat, por
//     qué falló exactamente).
//  3. Mantiene las últimas entradas en memoria para alimentar la vista de logs
//     en vivo del panel, y las vuelca además a un archivo rotativo en disco.
package logbus

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"tgdown/pkg/i18n"
)

// Niveles de severidad. El panel los usa para colorear y filtrar.
const (
	LevelDebug   = "debug"
	LevelInfo    = "info"
	LevelSuccess = "success"
	LevelWarn    = "warn"
	LevelError   = "error"
)

// Categorías (el "origen" de cada línea).
const (
	CatSystem    = "System"
	CatDownloads = "Downloads"
	CatListener  = "Listener"
	CatSettings  = "Settings"
	CatTelegram  = "Telegram"
	CatServer    = "Server"
	CatUpdater   = "Updater"
)

const (
	// DefaultCapacity es cuántas entradas se conservan en memoria.
	DefaultCapacity = 2000
	// maxFileBytes es el tamaño a partir del cual se rota el archivo .log.
	maxFileBytes = 5 << 20 // 5 MiB
	// logDirName define la carpeta donde se guardan los archivos de registro.
	logDirName = "logs"
)

// Entry es una línea del registro ya normalizada.
type Entry struct {
	ID       int64   `json:"id"`
	Time     float64 `json:"time"` // segundos Unix con decimales
	Level    string  `json:"level"`
	Category string  `json:"category"`
	Message  string  `json:"message"`
	Detail   string  `json:"detail,omitempty"`
}

// Subscriber recibe cada entrada nueva en cuanto se registra.
type Subscriber func(Entry)

// Bus es el registro en sí. Se puede instanciar, pero lo normal es usar el
// bus global a través de las funciones de paquete.
type Bus struct {
	mu      sync.RWMutex
	entries []Entry
	nextID  int64
	max     int

	subsMu  sync.RWMutex
	subs    map[int]Subscriber
	nextSub int

	mirror io.Writer

	fileMu   sync.Mutex
	file     *os.File
	baseDir  string
	fileDate string
	filePath string
	fileSize int64
}

// NewBus crea un bus con la capacidad indicada.
func NewBus(capacity int) *Bus {
	if capacity <= 0 {
		capacity = DefaultCapacity
	}
	return &Bus{
		entries: make([]Entry, 0, capacity),
		nextID:  1,
		max:     capacity,
		subs:    make(map[int]Subscriber),
		mirror:  os.Stderr,
	}
}

var std = NewBus(DefaultCapacity)

// Default devuelve el bus global.
func Default() *Bus { return std }

// ---------------------------------------------------------------------------
// Archivo en disco
// ---------------------------------------------------------------------------

// OpenFile empieza a volcar el registro a <dir>/logs/log_DD-MM-YY.log. Un error
// al abrir el archivo no es fatal: el registro en memoria sigue funcionando.
func (b *Bus) OpenFile(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("%s", i18n.T("logbus.emptyLogDir"))
	}
	b.fileMu.Lock()
	defer b.fileMu.Unlock()

	b.baseDir = dir
	return b.openFileForDate(time.Now())
}

// openFileForDate abre (o rota) el archivo de registro correspondiente a la fecha de t.
// Debe llamarse con b.fileMu bloqueado.
func (b *Bus) openFileForDate(t time.Time) error {
	if strings.TrimSpace(b.baseDir) == "" {
		return fmt.Errorf("%s", i18n.T("logbus.emptyLogDir"))
	}
	dateStr := t.Format("02-01-06")
	if b.file != nil && b.fileDate == dateStr {
		return nil
	}

	if b.file != nil {
		_ = b.file.Close()
		b.file = nil
	}

	logDir := filepath.Join(b.baseDir, logDirName)
	// SEC-30: el registro lleva nombres de chats y mensajes de error que el
	// usuario puede exportar, así que ni el directorio (0700) ni el archivo
	// (0600) deben ser legibles por otros usuarios de la máquina.
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	fileName := fmt.Sprintf("telegramdl_%s.log", dateStr)
	path := filepath.Join(logDir, fileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}

	var size int64
	if fi, statErr := f.Stat(); statErr == nil {
		size = fi.Size()
	}

	b.file = f
	b.filePath = path
	b.fileDate = dateStr
	b.fileSize = size
	return nil
}

// FilePath devuelve la ruta del archivo de log (vacía si no hay archivo).
func (b *Bus) FilePath() string {
	b.fileMu.Lock()
	defer b.fileMu.Unlock()
	return b.filePath
}

// CloseFile cierra el archivo de log.
func (b *Bus) CloseFile() {
	b.fileMu.Lock()
	defer b.fileMu.Unlock()
	if b.file != nil {
		_ = b.file.Close()
		b.file = nil
	}
}

// writeFile añade una línea al archivo y lo rota si se pasa de tamaño. Nunca
// usa log.Printf: eso provocaría una recursión infinita.
func (b *Bus) writeFile(line string) {
	b.writeFileForTime(time.Now(), line)
}

// writeFileForTime añade una línea al archivo correspondiente a la fecha de t.
func (b *Bus) writeFileForTime(t time.Time, line string) {
	b.fileMu.Lock()
	defer b.fileMu.Unlock()

	if strings.TrimSpace(b.baseDir) == "" && b.file == nil {
		return
	}

	if err := b.openFileForDate(t); err != nil {
		return
	}

	n, err := b.file.WriteString(line + "\n")
	if err != nil {
		return
	}
	b.fileSize += int64(n)
	if b.fileSize < maxFileBytes {
		return
	}

	// Rotación simple por tamaño en el mismo día: log_19-10-26.log -> log_19-10-26.log.1.
	_ = b.file.Close()
	b.file = nil
	backup := b.filePath + ".1"
	_ = os.Remove(backup)
	if err := os.Rename(b.filePath, backup); err != nil {
		// Si no se pudo rotar, reabrimos en modo truncado para no crecer sin
		// fin. El modo es 0600 por lo mismo que en OpenFile (SEC-30): el
		// archivo recién creado no debe quedar legible por otros usuarios.
		if f, oerr := os.OpenFile(b.filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); oerr == nil {
			b.file = f
			b.fileSize = 0
		}
		return
	}
	// Mismo 0600 que arriba: el archivo rotado se vuelve a crear desde cero.
	if f, oerr := os.OpenFile(b.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); oerr == nil {
		b.file = f
		b.fileSize = 0
	}
}

// ---------------------------------------------------------------------------
// Registro de entradas
// ---------------------------------------------------------------------------

// logLineBreaks sustituye cualquier salto de línea (CRLF, LF o CR) por la
// secuencia literal "\n" (dos caracteres). Se usa "\n" y no un espacio porque
// el registro es una pista de auditoría: un espacio borraría la huella de que
// el origen intentó partir la línea, mientras que "\n" deja constancia del
// intento sin permitir que se forgeen líneas físicas falsas con nivel o
// categoría inventados (p. ej. inyectando "\n[ERROR] [AUTH] ..."). El orden de
// los patrones hace que "\r\n" cuente como un solo salto y no genere "\n\n".
var logLineBreaks = strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\n`)

// sanitizeLogField elimina los CR/LF de un campo de texto libre para que cada
// entrada ocupe exactamente una línea física en el archivo y en el panel
// (SEC-28: inyección de log / CRLF). Solo reemplaza los saltos; nunca trunca.
func sanitizeLogField(s string) string {
	return logLineBreaks.Replace(s)
}

// Emit registra un evento con nivel, categoría, mensaje y detalle opcional.
// Es el único punto por el que pasa toda entrada del registro, así que aquí se
// neutralizan los saltos de línea de message y detail: pueden contener texto
// externo (nombres de chat, mensajes de terceros, err.Error()) y nadie debe
// poder partir la línea para falsificar nivel o categoría.
func (b *Bus) Emit(level, category, message, detail string) Entry {
	level = normalizeLevel(level)
	if strings.TrimSpace(category) == "" {
		category = CatSystem
	}
	message = sanitizeLogField(strings.TrimSpace(message))
	detail = sanitizeLogField(strings.TrimSpace(detail))
	if message == "" && detail == "" {
		return Entry{}
	}

	now := time.Now()
	entry := Entry{
		Time:     float64(now.UnixNano()) / 1e9,
		Level:    level,
		Category: category,
		Message:  message,
		Detail:   detail,
	}

	b.mu.Lock()
	entry.ID = b.nextID
	b.nextID++
	b.entries = append(b.entries, entry)
	if len(b.entries) > b.max {
		// Reutilizamos el array subyacente para no crecer indefinidamente.
		n := copy(b.entries, b.entries[len(b.entries)-b.max:])
		b.entries = b.entries[:n]
	}
	b.mu.Unlock()

	formatted := formatLine(now, entry)
	if b.mirror != nil {
		_, _ = io.WriteString(b.mirror, formatted+"\n")
	}
	b.writeFileForTime(now, formatted)
	b.notify(entry)
	return entry
}

func (b *Bus) notify(entry Entry) {
	b.subsMu.RLock()
	subs := make([]Subscriber, 0, len(b.subs))
	for _, s := range b.subs {
		subs = append(subs, s)
	}
	b.subsMu.RUnlock()

	for _, s := range subs {
		func(fn Subscriber) {
			// SEC-38: el pánico se registra ANTES de suprimirlo (regla 5).
			//
			// Canal elegido: NO log.Printf ni Emit/logbus.Error. Los
			// suscriptores se invocan en la misma goroutine de Emit, sin
			// candado tomado, y log está enganchado al propio bus
			// (logbus.Attach → Write → Emit → notify): loguear aquí re-entraría
			// en notify y volvería a invocar al mismo suscriptor, que si panica
			// de forma determinista provocaría recursión infinita hasta
			// desbordar la pila. Tampoco se toma ningún candado nuevo, así que
			// sobran los caminos que pudieran re-entrar en b.mu/subsMu/fileMu.
			// fmt.Fprint a os.Stderr es un descriptor directo (sin pasar por
			// Emit, aunque el bus use stderr como espejo) y writeFile está
			// documentado como "nunca usa log.Printf": ninguno de los dos puede
			// volver a entrar en esta tubería. stderr da visibilidad en
			// terminal y el fichero persiste la traza en tgdown.log aunque la
			// app vaya sin consola.
			defer func() {
				r := recover()
				if r == nil {
					return
				}
				line := fmt.Sprintf("panic in logbus subscriber: %v\n%s",
					r, strings.TrimRight(string(debug.Stack()), "\n"))
				fmt.Fprintln(os.Stderr, line)
				b.writeFile(line)
			}()
			fn(entry)
		}(s)
	}
}

// Subscribe registra un observador y devuelve la función para darse de baja.
func (b *Bus) Subscribe(fn Subscriber) func() {
	if fn == nil {
		return func() {}
	}
	b.subsMu.Lock()
	id := b.nextSub
	b.nextSub++
	b.subs[id] = fn
	b.subsMu.Unlock()

	return func() {
		b.subsMu.Lock()
		delete(b.subs, id)
		b.subsMu.Unlock()
	}
}

// Write implementa io.Writer para poder engancharlo al paquete "log".
func (b *Bus) Write(p []byte) (int, error) {
	text := strings.TrimRight(string(p), "\r\n")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		level, category, message := classifyRaw(line)
		b.Emit(level, category, message, "")
	}
	return len(p), nil
}

// Since devuelve las entradas con ID mayor que afterID (como mucho limit) y el
// último ID registrado, para que el panel pueda pedir solo lo nuevo.
func (b *Bus) Since(afterID int64, limit int) ([]Entry, int64) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if limit <= 0 || limit > b.max {
		limit = b.max
	}
	start := sort.Search(len(b.entries), func(i int) bool {
		return b.entries[i].ID > afterID
	})
	slice := b.entries[start:]
	if len(slice) > limit {
		slice = slice[len(slice)-limit:]
	}
	out := make([]Entry, len(slice))
	copy(out, slice)
	return out, b.nextID - 1
}

// LastID devuelve el ID de la última entrada registrada.
func (b *Bus) LastID() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.nextID - 1
}

// Clear vacía el registro en memoria. Los IDs siguen avanzando para no
// confundir a los paneles que ya tenían un cursor.
func (b *Bus) Clear() {
	b.mu.Lock()
	b.entries = b.entries[:0]
	b.mu.Unlock()
}

// ExportText devuelve todo el registro en memoria como texto plano.
func (b *Bus) ExportText() string {
	b.mu.RLock()
	entries := make([]Entry, len(b.entries))
	copy(entries, b.entries)
	b.mu.RUnlock()

	var sb strings.Builder
	for _, e := range entries {
		sec := int64(e.Time)
		nsec := int64((e.Time - float64(sec)) * 1e9)
		sb.WriteString(formatLine(time.Unix(sec, nsec), e))
		sb.WriteString("\n")
	}
	return sb.String()
}

// ---------------------------------------------------------------------------
// Formato y clasificación
// ---------------------------------------------------------------------------

func formatLine(t time.Time, e Entry) string {
	line := fmt.Sprintf("%s %-7s %-13s %s",
		t.Format("2006-01-02 15:04:05"),
		strings.ToUpper(e.Level),
		"["+e.Category+"]",
		e.Message,
	)
	if e.Detail != "" {
		line += " — " + e.Detail
	}
	return line
}

func normalizeLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case LevelDebug:
		return LevelDebug
	case LevelSuccess:
		return LevelSuccess
	case LevelWarn, "warning":
		return LevelWarn
	case LevelError:
		return LevelError
	default:
		return LevelInfo
	}
}

// rawCategories traduce los prefijos [TAG] que ya usa el código a categorías
// legibles en el panel.
var rawCategories = map[string]string{
	"DOWNLOADER":           CatDownloads,
	"DOWNLOAD":             CatDownloads,
	"DOWNLOAD ERROR":       CatDownloads,
	"DOWNLOAD FETCH":       CatDownloads,
	"DOWNLOAD FETCH ERROR": CatDownloads,
	"ENGINE":               CatDownloads,
	"LISTENER":             CatListener,
	"LISTENER CONFIG":      CatListener,
	"SERVER":               CatServer,
	"TG CLIENT":            CatTelegram,
	"TG CLIENT ERROR":      CatTelegram,
	"UPDATER":              CatUpdater,
	"SISTEMA":              CatSystem,
	"ALERTA":               CatSystem,
}

var (
	// Prefijo de fecha que añade el paquete "log" con las banderas por defecto.
	stdLogPrefix = regexp.MustCompile(`^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}(\.\d+)?\s+`)
	tagPrefix    = regexp.MustCompile(`^\[([^\]]{1,40})\]\s*`)

	// Las palabras de clasificación van en los dos idiomas del registro: lo que
	// la propia aplicación escribe ya llega traducido, y las librerías de
	// terceros (gotd, wails) siempre escriben en inglés.
	errorWords = regexp.MustCompile(`(?i)(error|falló|fallo|no se pudo|inválid|imposible|pánico|panic|rechazad|failed|failure|could not|cannot|unable|invalid|rejected|denied)`)
	warnWords  = regexp.MustCompile(`(?i)(advertencia|aviso|omitiendo|reintent|pausand|sin accesshash|sin sesión|no existe|no contiene|skipping|retr|paus|wait|missing|not found)`)
	okWords    = regexp.MustCompile(`(?i)(exitosa|establecida|completad|resuelto|actualizada|guardada|successful|established|completed|saved|resolved|updated)`)
	debugWords = regexp.MustCompile(`(?i)(solicitando slot|usando mensaje cacheado|obteniendo mensaje|consultando|fetching|querying|requesting|reusing)`)
)

// classifyRaw convierte una línea suelta de log.Printf en nivel + categoría +
// mensaje limpio.
func classifyRaw(line string) (level, category, message string) {
	message = stdLogPrefix.ReplaceAllString(strings.TrimSpace(line), "")
	category = CatSystem

	if m := tagPrefix.FindStringSubmatch(message); m != nil {
		tag := strings.ToUpper(strings.TrimSpace(m[1]))
		if cat, ok := rawCategories[tag]; ok {
			category = cat
			message = strings.TrimSpace(message[len(m[0]):])
		} else if strings.Contains(tag, "DOWNLOAD") {
			category = CatDownloads
			message = strings.TrimSpace(message[len(m[0]):])
		}
		if strings.Contains(tag, "ERROR") {
			return LevelError, category, message
		}
	}

	switch {
	case errorWords.MatchString(message):
		level = LevelError
	case warnWords.MatchString(message):
		level = LevelWarn
	case debugWords.MatchString(message):
		level = LevelDebug
	case okWords.MatchString(message):
		level = LevelSuccess
	default:
		level = LevelInfo
	}
	return level, category, message
}

// ---------------------------------------------------------------------------
// API de paquete sobre el bus global
// ---------------------------------------------------------------------------

// Init abre el archivo de log dentro de dir. Devuelve error solo para poder
// informarlo; el registro en memoria funciona igualmente.
func Init(dir string) error { return std.OpenFile(dir) }

// Attach redirige el paquete estándar "log" hacia el bus global, de forma que
// todo log.Printf existente aparezca en la vista de logs. Las marcas de tiempo
// las pone el propio bus, así que se desactivan las banderas de "log".
func Attach() {
	log.SetFlags(0)
	log.SetOutput(std)
}

// Detach devuelve el paquete "log" a su comportamiento normal.
func Detach() {
	log.SetFlags(log.LstdFlags)
	log.SetOutput(os.Stderr)
}

func Emit(level, category, message, detail string) Entry {
	return std.Emit(level, category, message, detail)
}
func Debug(category, message, detail string) Entry {
	return std.Emit(LevelDebug, category, message, detail)
}
func Info(category, message, detail string) Entry {
	return std.Emit(LevelInfo, category, message, detail)
}
func Success(category, message, detail string) Entry {
	return std.Emit(LevelSuccess, category, message, detail)
}
func Warn(category, message, detail string) Entry {
	return std.Emit(LevelWarn, category, message, detail)
}
func Error(category, message, detail string) Entry {
	return std.Emit(LevelError, category, message, detail)
}

func Subscribe(fn Subscriber) func()                  { return std.Subscribe(fn) }
func Since(afterID int64, limit int) ([]Entry, int64) { return std.Since(afterID, limit) }
func LastID() int64                                   { return std.LastID() }
func ExportText() string                              { return std.ExportText() }
func FilePath() string                                { return std.FilePath() }
func CloseFile()                                      { std.CloseFile() }

// Clear vacía el registro y deja constancia de ello.
func Clear() {
	std.Clear()
	std.Emit(LevelInfo, CatSystem, i18n.T("system.logCleared"), "")
}
