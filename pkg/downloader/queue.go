package downloader

// Cola y planificacion de descargas: cuantas corren a la vez, cual sigue,
// y el arranque de cada job de descarga.

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log"
	"sort"
	"time"

	"tgdown/pkg/config"
	"tgdown/pkg/i18n"
	"tgdown/pkg/logbus"
	"tgdown/pkg/storage"
)

func (e *Engine) enforceConcurrencyLimit() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.runningJobs <= e.config.MaxConcurrentDownloads {
		return
	}

	diff := e.runningJobs - e.config.MaxConcurrentDownloads
	log.Printf("[ENGINE] %s", i18n.T("downloader.reduceConcurrency", diff))

	type jobInfo struct {
		id        string
		startTime time.Time
	}
	running := make([]jobInfo, 0, len(e.cancelFuncs))
	for id := range e.cancelFuncs {
		running = append(running, jobInfo{id: id, startTime: e.startTimes[id]})
	}

	// Ordenar por tiempo de inicio descendente (las más nuevas primero)
	sort.Slice(running, func(i, j int) bool {
		return running[i].startTime.After(running[j].startTime)
	})

	for i := 0; i < diff && i < len(running); i++ {
		id := running[i].id
		if cancel, ok := e.cancelFuncs[id]; ok {
			log.Printf("[ENGINE] %s", i18n.T("downloader.pauseExcess", id))
			e.cancelledForLimit[id] = true
			cancel()
		}
	}
}

func (e *Engine) QueueItem(item storage.DownloadItem) string {
	e.mu.Lock()
	if item.ID == "" {
		item.ID = config.NewID()
	}

	// Evitar duplicados por ID único
	if existing, ok := e.downloads[item.ID]; ok {
		e.mu.Unlock()
		return existing.ID
	}

	// Evitar duplicados por ChatID y MessageID usando el mapa optimizado
	key := fmt.Sprintf("%d:%d", item.ChatID, item.MessageID)
	if existingID, ok := e.chatMsgMap[key]; ok {
		if existing, exists := e.downloads[existingID]; exists {
			// Si el item ya existe y no está fallido/cancelado, no duplicar
			if existing.Status != "failed" && existing.Status != "cancelled" {
				e.mu.Unlock()
				return existing.ID
			}
		}
	}

	item.Status = "queued"
	e.queuedIDs[item.ID] = true
	delete(e.forceDuplicate, item.ID)
	// Usar mayor precisión para evitar colisiones en CreatedAt durante bucles rápidos (rangos)
	item.CreatedAt = float64(time.Now().UnixNano()) / 1e9
	item.UpdatedAt = item.CreatedAt

	e.downloads[item.ID] = &item
	e.chatMsgMap[key] = item.ID
	cp := item
	e.mu.Unlock()
	if e.storage != nil {
		if err := e.storage.SaveDownload(item); err != nil {
			log.Printf("[DOWNLOADER] %s", i18n.T("downloader.saveStateError", err))
		}
	}
	e.notifyState(cp)

	e.launchDownloadJob(item.ID)
	return item.ID
}

func (e *Engine) launchDownloadJob(itemID string) {
	e.mu.Lock()
	if e.jobsInFlight[itemID] {
		e.mu.Unlock()
		return
	}
	e.jobsInFlight[itemID] = true
	e.mu.Unlock()

	e.jobsWG.Add(1)
	go func() {
		defer e.jobsWG.Done()
		relaunch := e.startDownloadJob(itemID)

		e.mu.Lock()
		delete(e.jobsInFlight, itemID)
		e.mu.Unlock()

		// El relanzamiento va DESPUÉS de liberar jobsInFlight: hacerlo desde
		// dentro del propio job lo descartaría silenciosamente y la tarea
		// quedaría huérfana en cola, sin goroutine que la retome jamás.
		if relaunch {
			e.launchDownloadJob(itemID)
		}
	}()
}

func (e *Engine) startDownloadJob(itemID string) (relaunch bool) {
	// Pre-resolver metadatos (nombre, tamaño) antes de esperar el slot de descarga
	go e.resolveItemMetadata(itemID)

	// Adquirir slot de concurrencia respetando el orden de la cola
	logbus.Debug(logbus.CatDownloads, i18n.T("downloads.requestTurn", itemID), "")
	e.mu.Lock()
	// Puede que el resolutor de metadatos ya la haya descartado por ser un ID
	// que no existe. Se comprueba antes de entrar a esperar, porque dentro del
	// bucle la salida solo se evalúa después de un Wait y quedaría dormida hasta
	// el siguiente aviso de la cola.
	if _, existe := e.downloads[itemID]; !existe {
		e.mu.Unlock()
		e.activeCond.Broadcast()
		return false
	}
	for (e.runningJobs >= e.config.MaxConcurrentDownloads || !e.isNextInQueue(itemID)) && !e.stopping {
		e.activeCond.Wait()
		// Si mientras esperaba fue cancelada, pausada o ya no existe, salir
		if it, exists := e.downloads[itemID]; !exists || (it.Status != "queued" && it.Status != "downloading") || e.pauseStates[itemID] {
			e.mu.Unlock()
			e.activeCond.Broadcast()
			return false
		}
	}

	item, ok := e.downloads[itemID]
	if !ok || item.Status == "cancelled" || item.Status == "paused" || e.stopping {
		e.mu.Unlock()
		e.activeCond.Broadcast() // Despertar al siguiente si este decide no iniciar
		return false
	}

	e.runningJobs++
	item.Status = "downloading" // Cambiar a downloading inmediatamente bajo lock
	delete(e.queuedIDs, itemID)

	ctx, cancel := context.WithCancel(context.Background())
	e.cancelFuncs[itemID] = cancel
	e.startTimes[itemID] = time.Now()
	e.lastBroadcastTimes[itemID] = time.Now()
	e.seenChunks[itemID] = make(map[int64]struct{})
	delete(e.lastProgressBytes, itemID)
	delete(e.lastProgressTimes, itemID)
	item.Speed = "0 B/s"
	// Se limpia el tiempo restante de la vez anterior: al reanudar una tarea
	// pausada se vería la estimación vieja hasta que llegue el primer bloque.
	item.ETA = ""
	e.itemSpeeds[itemID] = 0
	// El aviso visible de "descarga iniciada" lo emite el observador de estados
	// (applog.go), que sí conoce el nombre del archivo y del chat. Aquí solo
	// queda la traza interna con el ID de la tarea.
	logbus.Debug(logbus.CatDownloads,
		i18n.T("downloads.taskStarted", itemID, item.ChatID, item.MessageID), "")
	cp := *item
	e.mu.Unlock()
	e.notifyState(cp)

	defer func() {
		e.mu.Lock()
		e.runningJobs--
		e.activeCond.Broadcast() // Broadcast es más seguro que Signal para asegurar que la cola siga

		delete(e.cancelFuncs, itemID)
		delete(e.startTimes, itemID)
		delete(e.lastBroadcastTimes, itemID)
		delete(e.lastSaveTimes, itemID)
		delete(e.itemSpeeds, itemID)
		delete(e.seenChunks, itemID)
		delete(e.lastProgressBytes, itemID)
		delete(e.lastProgressTimes, itemID)
		delete(e.forceDuplicate, itemID)
		delete(e.messageCache, itemID) // Limpiar cache de mensaje

		// Si el job termina y el estado sigue siendo 'downloading', significa
		// que hubo un error no controlado o pánico. Lo marcamos como fallido.
		if it, ok := e.downloads[itemID]; ok && it.Status == "downloading" {
			it.Status = "failed"
			it.Error = "descarga interrumpida inesperadamente"
			e.sessionFailed++
			e.sessionFailedIDs = append(e.sessionFailedIDs, itemID)
		}

		// Una tarea detenida al reducir el límite de concurrencia debe volver a
		// la cola con un job vivo. No puede relanzarse aquí: este job sigue
		// marcado como 'en vuelo' (jobsInFlight) hasta que retorna, así que la
		// decisión se devuelve al wrapper de launchDownloadJob.
		if e.cancelledForLimit[itemID] {
			delete(e.cancelledForLimit, itemID)
			if it, ok := e.downloads[itemID]; ok && it.Status == "queued" && !e.stopping {
				relaunch = true
			}
		}
		e.mu.Unlock()
	}()

	err := e.executeDownloadWithRetry(ctx, itemID)
	logbus.Debug(logbus.CatDownloads, i18n.T("downloads.taskFinished", itemID, err), "")
	if errors.Is(err, errMensajeInexistente) {
		// Caso típico al descargar un rango: algunos IDs del intervalo no
		// corresponden a ningún mensaje descargable (borrados, inexistentes o
		// avisos de servicio del chat). Se descartan en silencio en vez de
		// dejarlos en el historial como fallidos: no hay nada que reintentar.
		//
		// La comprobación va con errors.Is y no comparando el texto del error:
		// así entran los dos caminos por los que fetchMessage se rinde, no solo
		// uno. Antes el caso del mensaje borrado, que es el más común, se
		// escapaba y acababa marcado como fallido.
		//
		// Se usa la copia (cp), no el elemento del mapa: aquí ya no tenemos el
		// mutex y el propio motor puede estar escribiendo en él.
		logbus.Warn(logbus.CatDownloads,
			i18n.T("downloads.msgMissing", cp.MessageID),
			i18n.T("downloads.msgMissingDetail",
				e.chatLabel(cp.ChatID)))
		e.discardDownload(itemID)
		return false
	}
	e.mu.Lock()

	curItem, ok := e.downloads[itemID]
	if !ok {
		e.mu.Unlock()
		return false
	}

	if errors.Is(err, context.Canceled) {
		// Si el estado ya es 'queued', significa que fue reanudado mientras se
		// cerraba: el wrapper lo relanzará al liberar este job.
		if curItem.Status == "queued" && !e.pauseStates[itemID] && !e.stopping {
			e.mu.Unlock()
			return true
		}

		if e.stopping || e.cancelledForLimit[itemID] {
			curItem.Status = "queued"
			e.queuedIDs[itemID] = true
			delete(e.pauseStates, itemID)
		} else if e.pauseStates[itemID] {
			curItem.Status = "paused"
		} else {
			curItem.Status = "cancelled"
		}
		curItem.Error = "" // Limpiar error si fue cancelado/pausado
	} else if err != nil {
		if errors.Is(err, errDownloadAlreadyExists) {
			curItem.Status = "duplicate"
			curItem.Error = ""
			curItem.Progress = 100.0
			curItem.Speed = "0 B/s"
			curItem.UpdatedAt = float64(time.Now().Unix())
			e.sessionSkipped++
			cp = *curItem
			e.mu.Unlock()
			if e.storage != nil {
				if err := e.storage.SaveDownload(cp); err != nil {
					log.Printf("[DOWNLOADER] %s", i18n.T("downloader.saveStateError", err))
				}
			}
			e.notifyState(cp)
			return false
		}
		curItem.Status = "failed"
		curItem.Error = err.Error()
		curItem.Speed = "0 B/s"
		e.sessionFailed++
		e.sessionFailedIDs = append(e.sessionFailedIDs, itemID)

		// Notificación de error: depende del mismo interruptor (bot del panel
		// lateral) que el resumen de cola, no de uno propio.
		if e.config.BotNotifications {
			e.sendNotification(i18n.T("notifier.downloadFailed", html.EscapeString(curItem.FileName), html.EscapeString(err.Error())))
		}
	} else {
		curItem.Status = "completed"
		curItem.Error = ""
		curItem.Progress = 100.0
		curItem.Speed = "0 B/s"
		e.sessionCompleted++
	}

	curItem.UpdatedAt = float64(time.Now().Unix())
	cp = *curItem
	e.mu.Unlock()
	if e.storage != nil {
		if err := e.storage.SaveDownload(cp); err != nil {
			log.Printf("[DOWNLOADER] %s", i18n.T("downloader.saveStateError", err))
		}
	}
	e.notifyState(cp)
	return
}

func (e *Engine) isNextInQueue(itemID string) bool {
	item, ok := e.downloads[itemID]
	if !ok || (item.Status != "queued" && item.Status != "downloading") || e.pauseStates[itemID] {
		return false
	}

	// Si hay hueco para nosotros según el límite, verificamos si hay alguien
	// antes que nosotros que también esté en "queued" y NO esté pausado.
	if e.runningJobs < e.config.MaxConcurrentDownloads {
		// Necesitamos saber cuántos huecos libres quedan
		slotsAvailable := e.config.MaxConcurrentDownloads - e.runningJobs

		// Buscamos cuántos elementos "queued" deberían ir antes que nosotros
		betterCandidates := 0
		for id := range e.queuedIDs {
			if id == itemID {
				continue
			}
			other, ok := e.downloads[id]
			if !ok || e.pauseStates[id] {
				continue
			}

			if e.shouldGoBefore(other, item) {
				betterCandidates++
				if betterCandidates >= slotsAvailable {
					return false
				}
			}
		}

		return true
	}

	return false
}

func (e *Engine) shouldGoBefore(a, b *storage.DownloadItem) bool {
	// 1. Mismo JobID (rango): priorizar MessageID
	if a.JobID != "" && a.JobID == b.JobID {
		if a.MessageID != b.MessageID {
			return a.MessageID < b.MessageID
		}
	}
	// 2. Tiempo de creación
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt < b.CreatedAt
	}
	// 3. Tie-breaker final: MessageID
	return a.MessageID < b.MessageID
}
