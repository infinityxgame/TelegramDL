package downloader

// Control de las peticiones de bloques de archivo a Telegram.
//
// Todo lo que pide bytes de un archivo —el downloader de gotd para una descarga
// nueva y downloadMissingParts para una reanudación— pasa por el mismo sitio:
// el envoltorio de este archivo. Así hay un único punto donde se limita cuántas
// peticiones van en vuelo y donde se atiende el FLOOD_WAIT, en vez de dos
// caminos con comportamientos distintos, que era la razón de que una descarga
// empezara rápida por un lado y siguiera arrastrándose por el otro.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"tgdown/pkg/i18n"
	"tgdown/pkg/logbus"
	"tgdown/pkg/telegram"
)

// maxInFlightBlocks es el tope de peticiones de bloque simultáneas de TODO el
// programa, sumando todos los workers de todas las descargas.
//
// Antes no había ninguno: con 6 descargas a la vez y 4 workers cada una se
// pedían 24 bloques al mismo tiempo, y con los topes de la configuración se
// podía llegar a 256. Pedir más de lo que las conexiones pueden servir no
// acelera nada —la cola se forma igual, solo que dentro del cliente— y es la
// forma más segura de que Telegram empiece a contestar FLOOD_WAIT.
//
// Ocho por conexión permite que las descargas con pocos workers trabajen a
// pleno rendimiento mientras se mantiene un límite razonable cuando se usan
// muchas descargas a la vez.
var maxInFlightBlocks = telegram.DownloadConnections * 8

// inFlightBlocks reparte esas ranuras entre todos los workers del programa.
var inFlightBlocks = make(chan struct{}, maxInFlightBlocks)

func acquireSlot(ctx context.Context) error {
	select {
	case inFlightBlocks <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseSlot() {
	select {
	case <-inFlightBlocks:
	default:
	}
}

// maxFloodAttempts es cuántas veces se aguanta una pausa de Telegram para la
// misma petición antes de devolver el error hacia arriba.
const maxFloodAttempts = 3

// floodGate para a TODOS los workers cuando Telegram pide esperar.
//
// El límite de Telegram es por cuenta y método, no por conexión ni por worker.
// Si uno se para y los demás siguen pidiendo, la cuenta sigue castigada y lo
// único que se consigue es que el castigo se renueve: cada worker se lleva su
// propia pausa, se duermen escalonados y la tubería nunca vuelve a llenarse.
// Eso es exactamente lo que se ve como «los workers no trabajan todos juntos».
// Parando todos a la vez, el tiempo se cumple una sola vez y la descarga
// recupera el ritmo completo de golpe.
var (
	floodGate struct {
		mu    sync.Mutex
		until time.Time
	}
	onFloodStateChange func()
)

// RegisterFloodNotifier registra la función a llamar cuando se activa o extiende un FLOOD_WAIT.
func RegisterFloodNotifier(fn func()) {
	floodGate.mu.Lock()
	defer floodGate.mu.Unlock()
	onFloodStateChange = fn
}

// GetFloodWaitUntil devuelve el instante hasta el cual Telegram mantendrá el FLOOD_WAIT activo.
func GetFloodWaitUntil() time.Time {
	floodGate.mu.Lock()
	defer floodGate.mu.Unlock()
	if time.Now().Before(floodGate.until) {
		return floodGate.until
	}
	return time.Time{}
}

// SetFloodWait activa una pausa global por FLOOD_WAIT desde cualquier punto de la aplicación.
func SetFloodWait(wait time.Duration) {
	closeFloodGate(wait)
}

// closeFloodGate deja a todos los workers parados hasta dentro de espera. Si
// ya había una pausa en curso más larga, se respeta la más larga.
func closeFloodGate(wait time.Duration) {
	limit := time.Now().Add(wait)
	floodGate.mu.Lock()
	changed := false
	if limit.After(floodGate.until) {
		floodGate.until = limit
		changed = true
	}
	notifier := onFloodStateChange
	floodGate.mu.Unlock()

	if changed && notifier != nil {
		go notifier()
	}
}

// waitFloodGate duerme lo que quede de la pausa global, si hay alguna.
func waitFloodGate(ctx context.Context) error {
	for {
		floodGate.mu.Lock()
		until := floodGate.until
		floodGate.mu.Unlock()

		remaining := time.Until(until)
		if remaining <= 0 {
			return nil
		}
		if remaining > maxFloodWait {
			remaining = maxFloodWait
		}

		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type blockClientState struct {
	mu        sync.RWMutex
	client    *tg.Client
	clientMgr *telegram.ClientManager
}

// blockClient es el cliente de Telegram con el control de bloques puesto.
//
// Lleva *tg.Client incrustado a propósito: así hereda tal cual todos los
// métodos que el downloader de gotd necesita (hashes, CDN, archivos web) y solo
// se cambia el que importa, UploadGetFile. Escribir a mano esa lista de métodos
// sería pedir que se rompa la compilación cada vez que gotd toque su interfaz.
type blockClient struct {
	*tg.Client
	state *blockClientState
}

// wrapClient prepara el cliente de bloques. Devuelve el original sin
// envolver si no hay cliente, para no esconder un nil detrás de un struct.
func wrapClient(client *tg.Client, cm *telegram.ClientManager) blockClient {
	if client == nil {
		return blockClient{}
	}
	state := &blockClientState{
		client:    client,
		clientMgr: cm,
	}
	return blockClient{
		Client: client,
		state:  state,
	}
}

func (c blockClient) currentClient() *tg.Client {
	if c.state != nil {
		c.state.mu.RLock()
		defer c.state.mu.RUnlock()
		if c.state.client != nil {
			return c.state.client
		}
	}
	return c.Client
}

func (c blockClient) setClient(newClient *tg.Client) {
	if c.state != nil {
		c.state.mu.Lock()
		c.state.client = newClient
		c.state.mu.Unlock()
	}
	c.Client = newClient
}

func detectFileMigrate(err error) int {
	if err == nil {
		return 0
	}
	var rpcErr *tgerr.Error
	if errors.As(err, &rpcErr) && (rpcErr.Code == 303 || rpcErr.Type == "FILE_MIGRATE") {
		if rpcErr.Argument > 0 {
			return rpcErr.Argument
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "FILE_MIGRATE") {
		re := regexp.MustCompile(`FILE_MIGRATE(?:_|\s*\()(\d+)`)
		matches := re.FindStringSubmatch(msg)
		if len(matches) > 1 {
			if dc, convErr := strconv.Atoi(matches[1]); convErr == nil && dc > 0 {
				return dc
			}
		}
	}
	return 0
}

// maxFileMigratesPerRequest es el techo duro de migraciones de DC que se
// aceptan dentro de una misma petición de bloque. Sin techo, un DC que siga
// respondiendo FILE_MIGRATE mantiene el bucle en `attempt--` sin avanzar
// nunca: un ciclo de RPCs a velocidad de red que acaba en FLOOD_WAIT continuo
// o en el baneo de la cuenta del usuario.
const maxFileMigratesPerRequest = 2

// Backoff aplicado entre migraciones de DC: base de 500 ms, se duplica en cada
// migración y se topa en 5 s para no castigar la descarga si Telegram insiste.
const (
	migrateBackoffBase = 500 * time.Millisecond
	migrateBackoffMax  = 5 * time.Second
)

// UploadGetFile pide un bloque respetando la pausa global y la ranura, y
// aguanta el FLOOD_WAIT en vez de dejarlo subir. Además, detecta si el archivo
// reside en otro centro de datos (FILE_MIGRATE) y conmuta la conexión
// transparentemente al DC adecuado, acotado por techo de migraciones, anti-ciclo
// de DCs y backoff creciente.
func (c blockClient) UploadGetFile(ctx context.Context, request *tg.UploadGetFileRequest) (tg.UploadFileClass, error) {
	var (
		resp tg.UploadFileClass
		err  error
		// migrations cuenta las migraciones ya hechas en ESTA petición y
		// visitedDCs recuerda a qué DCs se ha conmutado, para cortar si la
		// siguiente migración apunta a uno ya visitado (p. ej. DC2→DC3→DC2).
		migrations int
		visitedDCs = make(map[int]bool)
	)

	for attempt := 0; attempt < maxFloodAttempts; attempt++ {
		if waitErr := waitFloodGate(ctx); waitErr != nil {
			return nil, waitErr
		}
		if slotErr := acquireSlot(ctx); slotErr != nil {
			return nil, slotErr
		}

		client := c.currentClient()
		resp, err = client.UploadGetFile(ctx, request)
		releaseSlot()

		if err == nil {
			return resp, nil
		}

		if targetDC := detectFileMigrate(err); targetDC > 0 && c.state != nil && c.state.clientMgr != nil {
			// Techo duro de migraciones por petición: a partir de aquí ya no se
			// conmuta más y se devuelve el error envuelto para que arriba se vea
			// el motivo exacto del corte.
			if migrations >= maxFileMigratesPerRequest {
				logbus.Warn(logbus.CatDownloads,
					fmt.Sprintf("FILE_MIGRATE limit reached (%d migrations) while targeting DC %d", migrations, targetDC), "")
				return nil, fmt.Errorf("FILE_MIGRATE limit reached (%d migrations) targeting DC %d: %w",
					migrations, targetDC, err)
			}
			// Anti-ciclo: si el DC de destino ya se visitó en esta petición,
			// migrar a él solo alternaría entre dos centros de datos.
			if visitedDCs[targetDC] {
				logbus.Warn(logbus.CatDownloads,
					fmt.Sprintf("FILE_MIGRATE loop detected: DC %d already visited", targetDC), "")
				return nil, fmt.Errorf("FILE_MIGRATE loop detected: DC %d already visited: %w", targetDC, err)
			}
			logbus.Info(logbus.CatDownloads,
				i18n.T("downloads.migratingDC", targetDC), "")
			newClient, dcErr := c.state.clientMgr.DownloadClientForDC(ctx, targetDC)
			if dcErr == nil && newClient != nil {
				visitedDCs[targetDC] = true
				migrations++
				c.setClient(newClient)

				// Backoff creciente y con techo: 500 ms en la primera migración,
				// el doble en cada una siguiente, sin pasar nunca de 5 s.
				backoff := migrateBackoffBase << (migrations - 1)
				if backoff > migrateBackoffMax {
					backoff = migrateBackoffMax
				}
				// Si el error traería además un FLOOD_WAIT explícito más largo,
				// se respeta el mayor de los dos. floodWaitDuration ya descarta
				// lo que supere maxFloodWait, así que ese tope se respeta solo.
				if wait, isFlood := floodWaitDuration(err); isFlood && wait > backoff {
					backoff = wait
				}
				// La vuelta siguiente espera ctx-aware en waitFloodGate, de
				// forma que todos los workers se pausan juntos y sin quemar CPU.
				closeFloodGate(backoff)
				// Con el techo de migraciones el bucle ya no puede ser infinito
				// (como mucho maxFloodAttempts + maxFileMigratesPerRequest
				// vueltas); se resta para que una migración de DC no consuma un
				// intento de FLOOD_WAIT, que son cosas distintas.
				attempt--
				continue
			}
			logbus.Warn(logbus.CatDownloads,
				fmt.Sprintf("Error conectando a DC %d: %v", targetDC, dcErr), "")
		}

		wait, isFlood := floodWaitDuration(err)
		if !isFlood {
			return resp, err
		}

		// Se avisa en el registro porque es la única forma de saber desde el
		// panel que una descarga lenta es Telegram frenándonos y no un problema
		// del programa.
		logbus.Warn(logbus.CatDownloads,
			i18n.T("downloads.floodWait", wait),
			i18n.T("downloads.floodWaitDetail",
				request.Offset, attempt+1, maxFloodAttempts))

		// 500ms extra es margen suficiente para no volver justo al borde. La espera
		// la cumple la puerta al principio de la vuelta siguiente, y la cumplen
		// todos los workers a la vez.
		closeFloodGate(wait + 500*time.Millisecond)
	}

	return resp, err
}

func (c blockClient) UploadGetFileHashes(ctx context.Context, request *tg.UploadGetFileHashesRequest) ([]tg.FileHash, error) {
	client := c.currentClient()
	hashes, err := client.UploadGetFileHashes(ctx, request)
	if err != nil {
		if targetDC := detectFileMigrate(err); targetDC > 0 && c.state != nil && c.state.clientMgr != nil {
			newClient, dcErr := c.state.clientMgr.DownloadClientForDC(ctx, targetDC)
			if dcErr == nil && newClient != nil {
				c.setClient(newClient)
				return newClient.UploadGetFileHashes(ctx, request)
			}
		}
	}
	return hashes, err
}
