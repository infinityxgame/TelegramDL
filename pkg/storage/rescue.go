package storage

// Recuperación automática ante bases de datos dañadas.
//
// El historial de este proyecto demuestra que una BD corrupta deja la
// aplicación sin arrancar y que la única salida era borrar el archivo a mano,
// perdiendo configuración e historial. Este archivo implementa la salida
// automática:
//
//  1. QuickCheck comprueba la integridad tras cada apertura.
//  2. Si la BD no abre o no pasa la comprobación, se renombran sus archivos
//     (principal, -wal, -shm) con el sufijo .corrupt-<fecha> para conservar
//     las pruebas del fallo.
//  3. Se crea una BD nueva con el esquema de siempre.
//  4. salvageFrom copia a la nueva todo lo que se pueda leer de la dañada,
//     tabla por tabla y fila por fila: lo que caiga en una página rota se
//     pierde, el resto se recupera.
//
// Los archivos renombrados no se borran: ocupan poco, permiten diagnosticar
// el fallo con calma y reintentar la recuperación con otras herramientas; el
// usuario puede eliminarlos cuando quiera.

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"tgdown/pkg/i18n"
)

// rescueTableOrder son las tablas que se intentan rescatar, los padres antes
// que las hijas: download_chunks referencia downloads con una clave foránea,
// así que si la tabla padre no se rescata entera, los chunks huérfanos se
// rechazan al insertar y quedan contabilizados como ilegibles.
var rescueTableOrder = []string{
	"app_config",
	"listener_chats",
	"downloads",
	"download_chunks",
	"scanner_recent_chats",
	"scanner_cache_items",
	"scanner_cache_ranges",
}

// OpenResilient abre la base de datos de la aplicación y, si está dañada (no
// abre o no pasa la comprobación de integridad), la aparta con un renombrado,
// crea una nueva y rescatar en ella todo lo legible de la vieja. Devuelve
// siempre un Storage utilizable o el error que lo impide.
//
// Es la única vía de apertura que deben usar app.go y el modo CLI: NewStorage
// queda como pieza interna que abre sin comprobaciones.
func OpenResilient(dbPath string) (*Storage, error) {
	st, err := NewStorage(dbPath)
	if err == nil {
		// La BD abre, pero una página dañada en mitad de una tabla no impide
		// arrancar: sin esta comprobación los datos se perderían en silencio
		// más adelante.
		if checkErr := st.QuickCheck(); checkErr != nil {
			log.Printf("[STORAGE] %s", i18n.T("storage.quickCheckFailed", filepath.Base(dbPath), checkErr))
			if closeErr := st.Close(); closeErr != nil {
				log.Printf("[STORAGE] could not close damaged database before rescue: %v", closeErr)
			}
			err = checkErr
		} else {
			return st, nil
		}
	}

	log.Printf("[STORAGE] %s", i18n.T("storage.rescueNeeded", filepath.Base(dbPath), err))

	// Apartar los archivos dañados. Si ni siquiera existían (primer arranque
	// que falló al crearlos: disco lleno, permisos), no hay nada que renombrar
	// y se reintenta la creación tal cual.
	rescuedPath := ""
	if fileExists(dbPath) || fileExists(dbPath+"-wal") || fileExists(dbPath+"-shm") {
		rescuedPath, err = rescueRenameFileSet(dbPath)
		if err != nil {
			// Sin poder apartar el archivo dañado no se toca nada más: la BD
			// original se conserva tal cual y el fallo original es el que se
			// devuelve. Renombrar a medias y crear una BD vacía encima sería
			// la receta para perder datos dos veces.
			log.Printf("[STORAGE] %s", i18n.T("storage.rescueRenameFailed", filepath.Base(dbPath), err))
			return nil, err
		}
		log.Printf("[STORAGE] %s", i18n.T("storage.rescueRenamed", filepath.Base(rescuedPath)))
	}

	st, err = NewStorage(dbPath)
	if err != nil {
		log.Printf("[STORAGE] %s", i18n.T("storage.rescueRebuildFailed", err))
		return nil, err
	}

	if rescuedPath != "" {
		st.salvageFrom(rescuedPath)
	}
	// Versiones anteriores podían acabar usando el respaldo tgdown.db cuando
	// la BD principal no abría; si existe, se incorpora lo legible y se
	// aparta para que no vuelva a mezclarse con la principal.
	st.absorbLegacyFallback(dbPath)

	return st, nil
}

// QuickCheck ejecuta PRAGMA quick_check y devuelve un error con todos los
// problemas encontrados, o nil si la base está sana. Es quick_check (y no
// integrity_check) porque es la variante rápida pensada para ejecutarse en
// cada arranque: no verifica índices, solo que todas las páginas se lean con
// contenido coherente.
func (s *Storage) QuickCheck() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query("PRAGMA quick_check")
	if err != nil {
		return err
	}
	defer rows.Close()

	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(line), "ok") {
			continue
		}
		problems = append(problems, line)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(problems, "; "))
}

// rescueStamp da el sufijo común de un renombrado: fecha y hora con
// resolución de segundo, que basta porque solo se rescata una vez por
// arranque.
func rescueStamp() string {
	return time.Now().Format("20060102-150405")
}

// rescueRenameFileSet aparta los archivos de una base dañada añadiendo el
// sufijo .corrupt-<fecha> y devuelve la ruta nueva del archivo principal.
// Los satélites (-wal, -shm) se renombran primero y el principal al final:
// si un satélite no se puede mover, se aborta sin haber tocado el principal,
// evitando el escenario de un -wal huérfano junto a una BD principal nueva y
// vacía (recuperar el WAL viejo contra una base recién creada es justo lo
// que produce "database disk image is malformed").
func rescueRenameFileSet(dbPath string) (string, error) {
	stamp := rescueStamp()
	for _, p := range []string{dbPath + "-shm", dbPath + "-wal"} {
		if !fileExists(p) {
			continue
		}
		if err := os.Rename(p, p+".corrupt-"+stamp); err != nil {
			return "", err
		}
	}
	if err := os.Rename(dbPath, dbPath+".corrupt-"+stamp); err != nil {
		return "", err
	}
	return dbPath + ".corrupt-" + stamp, nil
}

// fileExists informa de si una ruta existe y es un archivo regular.
func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// salvageFrom copia a esta base (recién creada) todo lo que se pueda leer de
// la base dañada oldPath. Se tolera el fallo por tabla y por fila: una página
// rota solo se pierde a sí misma. Las filas se insertan con INSERT OR IGNORE
// para que los conflictos de clave primaria (datos ya rescatados de otra
// fuente) no interrumpan el proceso.
func (s *Storage) salvageFrom(oldPath string) {
	if !fileExists(oldPath) {
		// El archivo dañado nunca llegó a existir: no hay nada que rescatar y
		// este es el resultado normal cuando la apertura falló al crear la BD.
		return
	}
	oldDB, err := sql.Open("sqlite", oldPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		log.Printf("[STORAGE] salvage source %s could not be opened: %v", filepath.Base(oldPath), err)
		return
	}
	defer oldDB.Close()

	for _, table := range rescueTableOrder {
		s.salvageTable(oldDB, oldPath, table)
	}
}

// salvageTable copia una tabla completa de la base dañada a esta base. Las
// columnas se cruzan entre el esquema viejo y el nuevo, así que una base de
// una versión anterior (con menos columnas) también se rescata en lo que
// compartan ambos esquemas.
func (s *Storage) salvageTable(oldDB *sql.DB, oldPath, table string) {
	oldCols, err := tableColumnsOn(oldDB, table)
	if err != nil {
		// El daño alcanzó las páginas del esquema de esta tabla: se deja
		// constancia y se sigue con las demás.
		log.Printf("[STORAGE] %s", i18n.T("storage.rescueTableUnreadable", table, err))
		return
	}
	if len(oldCols) == 0 {
		// La tabla no existe en la base dañada: es lo normal cuando viene de
		// un esquema anterior que no la tenía, no es un fallo del rescate.
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	newCols, err := s.tableColumns(table)
	if err != nil {
		log.Printf("[STORAGE] %s", i18n.T("storage.rescueTableUnreadable", table, err))
		return
	}

	// Columnas comunes a ambos esquemas, en orden determinista. SELECT e
	// INSERT usan exactamente la misma lista, así que el orden es libre.
	common := make([]string, 0, len(oldCols))
	for col := range oldCols {
		if newCols[col] {
			common = append(common, col)
		}
	}
	if len(common) == 0 {
		return
	}
	sort.Strings(common)

	rows, err := oldDB.Query("SELECT " + strings.Join(common, ", ") + " FROM " + table)
	if err != nil {
		log.Printf("[STORAGE] %s", i18n.T("storage.rescueTableUnreadable", table, err))
		return
	}
	defer rows.Close()

	placeholders := make([]string, len(common))
	for i := range placeholders {
		placeholders[i] = "?"
	}
	insertSQL := "INSERT OR IGNORE INTO " + table +
		"(" + strings.Join(common, ", ") + ") VALUES(" + strings.Join(placeholders, ", ") + ")"

	tx, err := s.db.Begin()
	if err != nil {
		log.Printf("[STORAGE] %s", i18n.T("storage.rescueTableUnreadable", table, err))
		return
	}
	defer tx.Rollback()

	copied, skipped := 0, 0
	for rows.Next() {
		// Todo se copia como texto: SQLite convierte por afinidad al insertar
		// (un "-100123" acaba siendo INTEGER de nuevo), y NULL viaja bien en
		// un sql.NullString.
		values := make([]sql.NullString, len(common))
		dest := make([]any, len(common))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			skipped++
			continue
		}
		args := make([]any, len(common))
		for i := range values {
			args[i] = values[i]
		}
		if _, err := tx.Exec(insertSQL, args...); err != nil {
			skipped++
			continue
		}
		copied++
	}
	if err := tx.Commit(); err != nil {
		log.Printf("[STORAGE] %s", i18n.T("storage.rescueTableUnreadable", table, err))
		return
	}

	log.Printf("[STORAGE] %s", i18n.T("storage.rescueTable", table, copied, skipped))
}

// absorbLegacyFallback incorpora el respaldo antiguo tgdown.db (el fallback
// que las versiones anteriores creaban cuando la principal no abría) y lo
// aparta con sufijo .legacy-<fecha>. Solo se llama tras un rescate: si la
// base principal está sana, el respaldo antiguo no se toca, como siempre.
func (s *Storage) absorbLegacyFallback(dbPath string) {
	legacy := filepath.Join(filepath.Dir(dbPath), "tgdown.db")
	if !fileExists(legacy) {
		return
	}

	// INSERT OR IGNORE en salvageTable hace que lo ya rescatado de la base
	// principal dañada prevalezca sobre el respaldo: la principal es la
	// fuente autorizada cuando se puede leer.
	s.salvageFrom(legacy)

	stamp := rescueStamp()
	aside := legacy + ".legacy-" + stamp
	for _, p := range []string{legacy + "-shm", legacy + "-wal"} {
		if fileExists(p) {
			if err := os.Rename(p, p+".legacy-"+stamp); err != nil {
				// No es fatal: el respaldo ya se incorporó y el arranque no
				// vuelve a mirar estos archivos; solo queda el aviso.
				log.Printf("[STORAGE] %s", i18n.T("storage.legacyAbsorbFailed", filepath.Base(p), err))
			}
		}
	}
	if err := os.Rename(legacy, aside); err != nil {
		log.Printf("[STORAGE] %s", i18n.T("storage.legacyAbsorbFailed", filepath.Base(legacy), err))
		return
	}
	log.Printf("[STORAGE] %s", i18n.T("storage.legacyAbsorbed", filepath.Base(legacy), filepath.Base(aside)))
}

// tableColumnsOn es tableColumns para una conexión arbitraria: sirve para
// inspeccionar el esquema de la base dañada que se está rescatando.
func tableColumnsOn(db *sql.DB, table string) (map[string]bool, error) {
	cols := make(map[string]bool)
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return cols, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err == nil {
			cols[name] = true
		}
	}
	return cols, rows.Err()
}
