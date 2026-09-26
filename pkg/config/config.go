package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	AppVersion = "2.5.1"
	GithubRepo = "infinityxgame/tgdown"

	// DefaultBindHost es la dirección en la que escucha el panel cuando el
	// usuario no ha elegido ninguna. 0.0.0.0 atiende todas las interfaces, de
	// forma que el panel se puede abrir desde el móvil u otro equipo de la red
	// local. Para limitarlo a este ordenador basta con poner
	// TGDL_BIND_HOST=127.0.0.1 en el .env.
	DefaultBindHost = "0.0.0.0"
)

var SpeedMultipliers = map[string]float64{
	"B":  1.0,
	"KB": 1024.0,
	"MB": 1024.0 * 1024.0,
	"GB": 1024.0 * 1024.0 * 1024.0,
}

type SpeedLimit struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// GeneralTopicID es el número que Telegram reserva para el tema «General» de
// un grupo con temas. Los mensajes publicados ahí no llevan cabecera de tema,
// así que hay que ponerles este valor a mano para poder compararlos.
const GeneralTopicID int64 = 1

// ListenerChat describe un origen vigilado. Cuando TopicID es nil se escucha el
// grupo entero; cuando trae un número, solo ese tema del grupo. Un mismo grupo
// puede aparecer varias veces con temas distintos, así que lo que identifica a
// una entrada es el par (ID, TopicID) y no el ID a secas.
type ListenerChat struct {
	ID           int64  `json:"id"`
	TopicID      *int64 `json:"topic_id,omitempty"`
	TopicName    string `json:"topic_name,omitempty"`
	Name         string `json:"name"`
	AutoDownload bool   `json:"auto_download"`
	FPhotos      bool   `json:"f_photos"`
	FVideos      bool   `json:"f_videos"`
	FAudios      bool   `json:"f_audios"`
	FDocs        bool   `json:"f_docs"`
	FStickers    bool   `json:"f_stickers"`

	// Folder es la subcarpeta de descargas de este chat, relativa a la carpeta
	// de descargas. Se calcula la primera vez que llega un archivo y se guarda:
	// así, si el canal se renombra, sus archivos siguen cayendo todos juntos.
	// TopicFolder es lo mismo para el tema, y cuelga de Folder.
	Folder              string `json:"folder,omitempty"`
	TopicFolder         string `json:"topic_folder,omitempty"`
	ManualNameSelection bool   `json:"manual_name_selection"`
}

// CarpetaRelativa es la ruta, relativa a la carpeta de descargas, donde van los
// archivos de esta entrada. Vacía significa que todavía no se le asignó una.
func (c ListenerChat) CarpetaRelativa() string {
	base := strings.TrimSpace(c.Folder)
	if base == "" {
		return ""
	}
	if tema := strings.TrimSpace(c.TopicFolder); c.HasTopic() && tema != "" {
		return filepath.Join(base, tema)
	}
	return base
}

// ListenerChatKey construye el identificador único de una entrada de escucha.
func ListenerChatKey(chatID int64, topicID *int64) string {
	if topicID == nil || *topicID <= 0 {
		return strconv.FormatInt(chatID, 10)
	}
	return strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(*topicID, 10)
}

// Key identifica la entrada dentro de la lista de escucha.
func (c ListenerChat) Key() string {
	return ListenerChatKey(c.ID, c.TopicID)
}

// HasTopic indica si la entrada está acotada a un tema concreto.
func (c ListenerChat) HasTopic() bool {
	return c.TopicID != nil && *c.TopicID > 0
}

// Topic devuelve el número de tema vigilado, o 0 si se vigila el grupo entero.
func (c ListenerChat) Topic() int64 {
	if !c.HasTopic() {
		return 0
	}
	return *c.TopicID
}

// GroupName es el título del grupo, con el ID como último recurso.
func (c ListenerChat) GroupName() string {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		return strconv.FormatInt(c.ID, 10)
	}
	return name
}

// TopicLabel es el nombre del tema vigilado ("Tema 42" mientras Telegram no
// nos haya dado su título), o cadena vacía si se vigila el grupo entero.
func (c ListenerChat) TopicLabel() string {
	if !c.HasTopic() {
		return ""
	}
	if topic := strings.TrimSpace(c.TopicName); topic != "" {
		return topic
	}
	return fmt.Sprintf("Tema %d", c.Topic())
}

// DisplayName es lo que ve el usuario: primero el nombre del tema y después el
// del grupo al que pertenece, para saber siempre de dónde viene el archivo.
func (c ListenerChat) DisplayName() string {
	if topic := c.TopicLabel(); topic != "" {
		return topic + " · " + c.GroupName()
	}
	return c.GroupName()
}

// TopicPointer normaliza un número de tema a la forma que guarda ListenerChat:
// 0 o negativo significan «todo el grupo».
func TopicPointer(topicID int64) *int64 {
	if topicID <= 0 {
		return nil
	}
	v := topicID
	return &v
}

type Config struct {
	MaxConcurrentDownloads int    `json:"max_concurrent_downloads"`
	ParallelChunks         bool   `json:"parallel_chunks"`
	ChunkWorkers           int    `json:"chunk_workers"`
	DownloadFolder         string `json:"download_folder"`
	// OrganizeByChat reparte lo que baja la escucha en una subcarpeta por chat.
	// Apagado, todo cae en la carpeta de descargas, como antes.
	OrganizeByChat  bool           `json:"organize_by_chat"`
	ColorID         *int           `json:"color_id"`
	LoaderColorID   *int           `json:"loader_color_id"`
	SpeedLimit      SpeedLimit     `json:"speed_limit"`
	ListenerEnabled bool           `json:"listener_enabled"`
	ListenerChats   []ListenerChat `json:"listener_chats"`
	ListenerChatIDs []int64        `json:"listener_chat_ids"`
}

var (
	DataDir     string
	UserEnvPath string
	BaseDir     string
	initDirOnce sync.Once
)

func InitPaths() {
	initDirOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		DataDir = filepath.Join(home, ".tgdown")
		_ = os.MkdirAll(DataDir, 0o700)

		UserEnvPath = filepath.Join(DataDir, ".env")

		// Base directory
		execPath, err := os.Executable()
		if err == nil {
			BaseDir = filepath.Dir(execPath)
		} else {
			BaseDir = "."
		}

		// Todo lo que vive en esta carpeta —la sesión de Telegram, las
		// credenciales, el historial— es privado del usuario. Se creaba en 0755,
		// es decir legible por cualquier otro usuario de la máquina.
		secureDataDir()
	})
}

// secureDataDir cierra los permisos de la carpeta de datos y de los archivos
// sensibles que haya dentro. Se aplica también a instalaciones que ya existían,
// porque MkdirAll no toca el modo de un directorio ya creado.
//
// En Windows el modo de Go no significa nada —mandan las ACL, y la carpeta del
// usuario ya está restringida por defecto—, así que allí esto no hace daño ni
// falta.
func secureDataDir() {
	_ = os.Chmod(DataDir, 0o700)

	for _, name := range []string{
		"tg_session.json",
		"tgdown.sqlite3",
		"tgdown.sqlite3-wal",
		"tgdown.sqlite3-shm",
		"tgdown.db",
		".env",
	} {
		path := filepath.Join(DataDir, name)
		if _, err := os.Stat(path); err == nil {
			_ = os.Chmod(path, 0o600)
		}
	}
}

// ---------------------------------------------------------------------------
// Lectura del .env antiguo
//
// La aplicación ya no usa ningún archivo .env: las credenciales y la dirección
// de escucha viven en la tabla app_config de la base de datos, que es la única
// fuente de verdad. Lo que queda aquí es solo lo justo para leer una vez el
// archivo de quienes vienen de una versión anterior y volcarlo a la base de
// datos. Para eso no hace falta godotenv.
// ---------------------------------------------------------------------------

// parseEnvFile entiende lo único que la aplicación llegó a escribir: líneas
// CLAVE=valor, saltándose comentarios, líneas en blanco, el prefijo export y
// las comillas alrededor del valor.
func parseEnvFile(path string) map[string]string {
	out := map[string]string{}

	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}

	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		name, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)

		if len(value) >= 2 {
			first, last := value[0], value[len(value)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				value = value[1 : len(value)-1]
			}
		}

		if name != "" {
			out[name] = value
		}
	}
	return out
}

// LegacyEnvValues reúne lo que hubiera en los .env de versiones anteriores. Se
// leen de menor a mayor prioridad, así que el archivo del usuario pisa a los
// que estén junto al ejecutable.
func LegacyEnvValues() map[string]string {
	InitPaths()

	merged := map[string]string{}
	for _, path := range []string{
		".env",
		filepath.Join(BaseDir, ".env"),
		UserEnvPath,
	} {
		for k, v := range parseEnvFile(path) {
			if strings.TrimSpace(v) != "" {
				merged[k] = v
			}
		}
	}
	return merged
}

// ArchiveLegacyEnv aparta el .env del usuario una vez migrado. Se renombra en
// vez de borrarse: deja de leerse, pero si algo saliera mal las credenciales
// siguen ahí. Los .env que puedan estar junto al ejecutable no se tocan, porque
// esa carpeta puede ser de solo lectura.
func ArchiveLegacyEnv() {
	InitPaths()

	if _, err := os.Stat(UserEnvPath); err != nil {
		return
	}
	_ = os.Rename(UserEnvPath, UserEnvPath+".migrado")
}

// ---------------------------------------------------------------------------
// Validación de la carpeta de descargas
//
// La carpeta llega desde /api/settings, o sea desde cualquiera que tenga el
// token, incluido un dispositivo remoto. Sin comprobar nada se podía apuntar a
// la carpeta de Inicio de Windows y conseguir que la aplicación dejara ahí un
// ejecutable descargado de Telegram, que arrancaría en la siguiente sesión. Y
// apuntándola a ~/.tgdown se podía pisar la sesión de Telegram.
//
// No es una lista blanca: el usuario tiene que poder elegir cualquier carpeta
// suya. Se rechazan solo los sitios donde dejar un archivo tiene consecuencias.
// ---------------------------------------------------------------------------

// ValidateDownloadFolder devuelve un error si la carpeta no sirve como destino
// de descargas.
func ValidateDownloadFolder(folder string) error {
	InitPaths()

	folder = strings.TrimSpace(folder)
	if folder == "" {
		return fmt.Errorf("la carpeta de descargas no puede estar vacía")
	}
	if !filepath.IsAbs(folder) {
		return fmt.Errorf("la carpeta de descargas debe ser una ruta absoluta")
	}

	limpia := filepath.Clean(folder)

	for _, prohibida := range carpetasProhibidas() {
		if prohibida == "" {
			continue
		}
		if rutaDentroDe(prohibida, limpia) {
			return fmt.Errorf("esa carpeta está reservada por el sistema o por la propia aplicación; elige otra")
		}
	}

	return nil
}

// rutaDentroDe indica si hijo es padre o está por debajo de él, comparando sin
// distinguir mayúsculas en Windows y macOS, donde el sistema de archivos
// tampoco las distingue.
func rutaDentroDe(padre, hijo string) bool {
	padre = filepath.Clean(padre)
	hijo = filepath.Clean(hijo)

	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		padre = strings.ToLower(padre)
		hijo = strings.ToLower(hijo)
	}

	if padre == hijo {
		return true
	}
	return strings.HasPrefix(hijo, padre+string(filepath.Separator))
}

func carpetasProhibidas() []string {
	// La carpeta de datos de la aplicación: ahí viven la sesión de Telegram, las
	// credenciales y la base de datos.
	prohibidas := []string{DataDir}

	home, _ := os.UserHomeDir()
	unir := func(base string, partes ...string) string {
		if base == "" {
			return ""
		}
		return filepath.Join(append([]string{base}, partes...)...)
	}

	switch runtime.GOOS {
	case "windows":
		prohibidas = append(prohibidas,
			os.Getenv("SystemRoot"),
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
			unir(os.Getenv("AppData"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup"),
			unir(os.Getenv("ProgramData"), "Microsoft", "Windows", "Start Menu", "Programs", "StartUp"),
		)
	case "darwin":
		prohibidas = append(prohibidas,
			"/System", "/Library/LaunchAgents", "/Library/LaunchDaemons", "/Applications",
			unir(home, "Library", "LaunchAgents"),
		)
	default:
		prohibidas = append(prohibidas,
			"/etc", "/bin", "/sbin", "/usr", "/boot", "/lib",
			unir(home, ".config", "autostart"),
			unir(home, ".local", "share", "systemd"),
			unir(home, ".config", "systemd"),
		)
	}

	return prohibidas
}

func GetDefaultDownloadFolder() string {
	InitPaths()
	home, err := os.UserHomeDir()
	if err == nil {
		downloadsDir := filepath.Join(home, "Downloads")
		if _, err := os.Stat(downloadsDir); err == nil {
			return filepath.Join(downloadsDir, "TelegramDL")
		}
	}
	return filepath.Join(BaseDir, "descargas")
}

func DefaultConfig() Config {
	return Config{
		MaxConcurrentDownloads: 6,
		ParallelChunks:         true,
		ChunkWorkers:           4,
		DownloadFolder:         GetDefaultDownloadFolder(),
		OrganizeByChat:         true,
		ColorID:                nil,
		LoaderColorID:          nil,
		SpeedLimit: SpeedLimit{
			Value: 0,
			Unit:  "MB",
		},
		ListenerEnabled: true,
		ListenerChats:   []ListenerChat{},
		ListenerChatIDs: []int64{},
	}
}

// ---------------------------------------------------------------------------
// Dirección de escucha
//
// El host y el puerto se guardan en la base de datos, pero este paquete no
// puede leerla: pkg/storage ya importa pkg/config, así que el import al revés
// sería un ciclo. Por eso quien sí tiene acceso a la base de datos —app.go, al
// arrancar— los inyecta aquí con SetServerBinding antes de levantar el
// servidor.
//
// Las variables de entorno siguen pudiendo pisar el valor guardado. No son
// secretos y son la salida de emergencia: si el puerto está ocupado y la
// ventana no llega a abrir, o si la aplicación corre en modo --server bajo
// systemd, es la única forma de cambiarlo sin interfaz.
// ---------------------------------------------------------------------------

const DefaultServerPort = 8000

var (
	bindingMu  sync.RWMutex
	bindHost   string
	bindPort   int
	bindingSet bool
)

// SetServerBinding fija el host y el puerto leídos de la base de datos. Un
// valor vacío o un puerto fuera de rango se ignoran y se queda el de siempre.
func SetServerBinding(host string, port int) {
	bindingMu.Lock()
	defer bindingMu.Unlock()

	if h := strings.TrimSpace(host); h != "" {
		bindHost = h
	}
	if port > 0 && port <= 65535 {
		bindPort = port
	}
	bindingSet = true
}

// ServerBindingLoaded indica si ya se inyectaron valores desde la base de
// datos. Sirve para no anunciar un puerto que todavía es el de por defecto.
func ServerBindingLoaded() bool {
	bindingMu.RLock()
	defer bindingMu.RUnlock()
	return bindingSet
}

func GetServerPort() int {
	InitPaths()

	// 1. Variable de entorno, que manda sobre todo.
	portStr := strings.TrimSpace(os.Getenv("TGDL_PORT"))
	if portStr == "" {
		portStr = strings.TrimSpace(os.Getenv("PORT"))
	}
	if p, err := strconv.Atoi(portStr); err == nil && p > 0 && p <= 65535 {
		return p
	}

	// 2. Lo guardado en la base de datos.
	bindingMu.RLock()
	p := bindPort
	bindingMu.RUnlock()
	if p > 0 {
		return p
	}

	// 3. Por defecto.
	return DefaultServerPort
}

func GetServerHost() string {
	InitPaths()

	host := strings.TrimSpace(os.Getenv("TGDL_BIND_HOST"))
	if host == "" {
		host = strings.TrimSpace(os.Getenv("BIND_HOST"))
	}
	if host != "" {
		return host
	}

	bindingMu.RLock()
	h := bindHost
	bindingMu.RUnlock()
	if h != "" {
		return h
	}

	return DefaultBindHost
}

func NormalizeConfig(raw Config) Config {
	if raw.MaxConcurrentDownloads < 1 {
		raw.MaxConcurrentDownloads = 1
	} else if raw.MaxConcurrentDownloads > 32 {
		raw.MaxConcurrentDownloads = 32
	}

	if raw.ChunkWorkers < 1 {
		raw.ChunkWorkers = 1
	} else if raw.ChunkWorkers > 8 {
		raw.ChunkWorkers = 8
	}

	if strings.TrimSpace(raw.DownloadFolder) == "" {
		raw.DownloadFolder = GetDefaultDownloadFolder()
	}

	raw.SpeedLimit.Unit = strings.ToUpper(strings.TrimSpace(raw.SpeedLimit.Unit))
	if _, ok := SpeedMultipliers[raw.SpeedLimit.Unit]; !ok {
		raw.SpeedLimit.Unit = "MB"
	}
	if raw.SpeedLimit.Value < 0 {
		raw.SpeedLimit.Value = 0
	}

	// Dos entradas con el mismo grupo y el mismo tema son la misma cosa: la
	// última gana. Sin esto, la clave primaria (chat_id, topic_id) de SQLite
	// rechazaría el guardado entero.
	seenKeys := make(map[string]int, len(raw.ListenerChats))
	uniqueChats := make([]ListenerChat, 0, len(raw.ListenerChats))
	for _, chat := range raw.ListenerChats {
		if chat.TopicID != nil && *chat.TopicID <= 0 {
			chat.TopicID = nil
		}
		if !chat.HasTopic() {
			chat.TopicName = ""
			chat.TopicFolder = ""
		}
		chat.TopicName = strings.TrimSpace(chat.TopicName)
		chat.Name = strings.TrimSpace(chat.Name)
		chat.Folder = strings.TrimSpace(chat.Folder)
		chat.TopicFolder = strings.TrimSpace(chat.TopicFolder)

		if pos, dup := seenKeys[chat.Key()]; dup {
			uniqueChats[pos] = chat
			continue
		}
		seenKeys[chat.Key()] = len(uniqueChats)
		uniqueChats = append(uniqueChats, chat)
	}
	raw.ListenerChats = uniqueChats

	// ListenerChatIDs es la lista plana de grupos vigilados. Con temas, un mismo
	// grupo puede tener varias entradas, así que aquí solo aparece una vez.
	seenIDs := make(map[int64]bool, len(uniqueChats))
	chatIDs := make([]int64, 0, len(uniqueChats))
	for i := range uniqueChats {
		id := uniqueChats[i].ID
		if seenIDs[id] {
			continue
		}
		seenIDs[id] = true
		chatIDs = append(chatIDs, id)
	}
	raw.ListenerChatIDs = chatIDs

	return raw
}

// PreservarCarpetas devuelve las entradas nuevas con la carpeta que ya tenían
// las viejas. El panel manda la lista de chats sin el campo «folder» (la vista
// de Ajustes reconstruye cada chat campo a campo al importar la escucha), y sin
// esto un guardado desde allí borraría el reparto en carpetas y los archivos
// del mismo chat acabarían en dos sitios distintos.
func PreservarCarpetas(previas, nuevas []ListenerChat) []ListenerChat {
	if len(previas) == 0 || len(nuevas) == 0 {
		return nuevas
	}

	porClave := make(map[string]ListenerChat, len(previas))
	for _, vieja := range previas {
		porClave[vieja.Key()] = vieja
	}

	for i := range nuevas {
		vieja, ok := porClave[nuevas[i].Key()]
		if !ok {
			continue
		}
		if strings.TrimSpace(nuevas[i].Folder) == "" {
			nuevas[i].Folder = vieja.Folder
		}
		if strings.TrimSpace(nuevas[i].TopicFolder) == "" {
			nuevas[i].TopicFolder = vieja.TopicFolder
		}
	}

	return nuevas
}

func FormatBytes(size float64) string {
	if size <= 0 {
		return "0 B"
	}
	// macOS utiliza base 1000 para todo (Finder, Disk Utility)
	base := 1024.0
	if runtime.GOOS == "darwin" {
		base = 1000.0
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for size >= base && i < len(units)-1 {
		size /= base
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f B", size)
	}
	return fmt.Sprintf("%.2f %s", size, units[i])
}

// GenerateToken crea un secreto aleatorio criptográficamente seguro
// (32 bytes, codificado en hexadecimal) usado como token de acceso a la
// API HTTP local. Se genera una sola vez y se persiste; ver
// storage.GetAPIToken / storage.SaveAPIToken.
func GenerateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("error generando token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// idFallbackCounter solo se usa si crypto/rand llegara a fallar. Va aparte del
// reloj porque en Windows la marca de tiempo tiene una resolución de
// milisegundos: al encolar un rango de mensajes en un bucle, varias llamadas
// seguidas devolverían el mismo instante y, con él, el mismo identificador.
var idFallbackCounter atomic.Uint64

// NewID devuelve un identificador único para una descarga o un trabajo.
//
// Esto es lo que antes hacía github.com/google/uuid. Un identificador aquí solo
// tiene que ser único y no adivinable —nunca se interpreta ni se compara por
// partes—, así que 16 bytes de crypto/rand en hexadecimal sobran y evitan la
// dependencia. El formato es distinto al de un UUID (sin guiones), pero los
// identificadores ya guardados siguen siendo válidos: la columna es TEXT y
// nadie comprueba su forma.
func NewID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand no falla en la práctica. Si lo hiciera, preferimos un
		// identificador previsible a no poder encolar nada.
		return strconv.FormatInt(time.Now().UnixNano(), 36) + "-" +
			strconv.FormatUint(idFallbackCounter.Add(1), 36)
	}
	return hex.EncodeToString(buf)
}

func ParseInt64(val any) int64 {
	switch v := val.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		res, _ := strconv.ParseInt(v, 10, 64)
		return res
	case json.Number:
		res, _ := v.Int64()
		return res
	default:
		return 0
	}
}
