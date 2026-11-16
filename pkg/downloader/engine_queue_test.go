package downloader

import (
	"fmt"
	"testing"
	"time"

	"tgdown/pkg/config"
	"tgdown/pkg/storage"
	"tgdown/pkg/telegram"
)

func waitForCondition(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not reached in 8s: %s", what)
}

func (e *Engine) statusOf(id string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if it, ok := e.downloads[id]; ok {
		return it.Status
	}
	return ""
}

func (e *Engine) inFlight(id string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.jobsInFlight[id]
}

// Reproduce el fallo reportado: al reducir el límite de descargas simultáneas,
// la tarea detenida en exceso debe volver a la cola CON un job vivo, y
// retomarse cuando la descarga en curso termina o se cancela. Antes del fix,
// el relanzamiento se hacía desde dentro del propio job mientras aún figuraba
// en jobsInFlight, se descartaba en silencio y la tarea quedaba huérfana.
func TestConcurrencyLimitRequeuesCancelledJob(t *testing.T) {
	cm := telegram.NewClientManager()
	// Cliente con forma válida que nunca conecta: los jobs se quedan
	// bloqueados en WaitReady hasta ser cancelados, simulando descargas.
	if err := cm.InitClient("1234567", "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("InitClient: %v", err)
	}
	t.Cleanup(cm.Stop)

	cfg := config.DefaultConfig()
	cfg.DownloadFolder = t.TempDir()
	cfg.MaxConcurrentDownloads = 2
	eng := NewEngine(cm, nil, cfg)

	eng.QueueItem(storage.DownloadItem{ID: "a", ChatID: 10, MessageID: 1, FileName: "a.bin"})
	eng.QueueItem(storage.DownloadItem{ID: "b", ChatID: 10, MessageID: 2, FileName: "b.bin"})

	waitForCondition(t, "ambas descargas en curso", func() bool {
		return eng.statusOf("a") == "downloading" && eng.statusOf("b") == "downloading"
	})

	// Reducir el límite a 1: la tarea más reciente se detiene y debe quedar
	// en cola con un job vivo esperando hueco.
	cfg2 := cfg
	cfg2.MaxConcurrentDownloads = 1
	eng.UpdateConfig(cfg2)

	waitForCondition(t, "una sigue downloading y la otra está en cola con job vivo", func() bool {
		as, bs := eng.statusOf("a"), eng.statusOf("b")
		queuedWithJob := (as == "queued" && eng.inFlight("a")) || (bs == "queued" && eng.inFlight("b"))
		stillDownloading := as == "downloading" || bs == "downloading"
		return queuedWithJob && stillDownloading
	})

	// Al terminar (cancelar) la descarga activa, la encolada debe tomar el hueco.
	activeID, queuedID := "a", "b"
	if eng.statusOf("b") == "downloading" {
		activeID, queuedID = "b", "a"
	}
	if err := eng.CancelDownload(activeID); err != nil {
		t.Fatal(err)
	}

	waitForCondition(t, "la tarea encolada retoma la descarga", func() bool {
		return eng.statusOf(queuedID) == "downloading"
	})

	// Subir el límite no debe dejar nada huérfano: todo lo que siga en cola
	// tiene un job esperando.
	eng.CancelAll()
	waitForCondition(t, "ningún job huérfano tras cancelar todo", func() bool {
		eng.mu.RLock()
		defer eng.mu.RUnlock()
		for id, item := range eng.downloads {
			if item.Status == "queued" && !eng.jobsInFlight[id] {
				return false
			}
		}
		return true
	})
}

// SEC-21: el techo de cola solo cuenta los estados NO terminales. La lógica
// está extraída a funciones puras (activeQueueSize/queueIsFull) para poder
// probarla sin crear 1000 descargas reales, que en la práctica serían 1000
// jobs con sus goroutines dentro del test.
func TestQueueCeilingCountsOnlyNonTerminalItems(t *testing.T) {
	downloads := map[string]*storage.DownloadItem{
		"done":   {ID: "done", Status: "completed"},
		"failed": {ID: "failed", Status: "failed"},
		"cancel": {ID: "cancel", Status: "cancelled"},
		"skip":   {ID: "skip", Status: "skipped"},
		"dup":    {ID: "dup", Status: "duplicate"},
		"queue":  {ID: "queue", Status: "queued"},
		"active": {ID: "active", Status: "downloading"},
		"pause":  {ID: "pause", Status: "paused"},
		"pend":   {ID: "pend", Status: "pending"},
	}

	// Cuatro no terminales: queued, downloading, paused y pending.
	if got := activeQueueSize(downloads); got != 4 {
		t.Fatalf("activeQueueSize = %d, want 4 (solo estados no terminales)", got)
	}
	if queueIsFull(activeQueueSize(downloads)) {
		t.Error("4 tareas activas no deben dar la cola por llena")
	}
}

// SEC-21: el techo se decide con >= exactamente en maxQueuedDownloads.
func TestQueueIsFullAtCeiling(t *testing.T) {
	if queueIsFull(maxQueuedDownloads - 1) {
		t.Errorf("con %d tareas la cola no debe estar llena", maxQueuedDownloads-1)
	}
	if !queueIsFull(maxQueuedDownloads) {
		t.Errorf("con %d tareas la cola debe estar llena", maxQueuedDownloads)
	}
}

// SEC-21: con la cola llena, QueueItem rechaza sin insertar nada, sin dejar
// basura en los mapas de control y sin lanzar job. La cola se rellena pintando
// e.downloads directamente: encolar 1000 tareas de verdad crearía 1000 jobs y
// 1000 goroutines reales, que es justo lo que el techo pretende evitar.
func TestQueueItemRejectedWhenQueueFull(t *testing.T) {
	cm := telegram.NewClientManager()
	// Cliente con forma válida que nunca conecta: si el techo fallara y se
	// encolara, el job se quedaría bloqueado en WaitReady hasta el cleanup.
	if err := cm.InitClient("1234567", "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("InitClient: %v", err)
	}
	t.Cleanup(cm.Stop)

	cfg := config.DefaultConfig()
	cfg.DownloadFolder = t.TempDir()
	eng := NewEngine(cm, nil, cfg)

	eng.mu.Lock()
	for i := 0; i < maxQueuedDownloads; i++ {
		id := fmt.Sprintf("fill-%d", i)
		eng.downloads[id] = &storage.DownloadItem{
			ID:        id,
			ChatID:    1,
			MessageID: int64(i),
			Status:    "queued",
		}
	}
	eng.mu.Unlock()

	got := eng.QueueItem(storage.DownloadItem{
		ID:        "rejected",
		ChatID:    2,
		MessageID: 1,
		FileName:  "rechazado.bin",
	})
	if got != "" {
		t.Fatalf("con la cola llena QueueItem debe devolver \"\", got %q", got)
	}
	if _, ok := eng.GetItem("rejected"); ok {
		t.Error("el ítem rechazado no debe insertarse en el historial")
	}

	eng.mu.RLock()
	defer eng.mu.RUnlock()
	if len(eng.downloads) != maxQueuedDownloads {
		t.Errorf("e.downloads = %d, want %d (el rechazo no cambia el tamaño)",
			len(eng.downloads), maxQueuedDownloads)
	}
	if len(eng.chatMsgMap) != 0 {
		t.Errorf("chatMsgMap = %d entradas, want 0 (sin basura tras el rechazo)", len(eng.chatMsgMap))
	}
	if len(eng.queuedIDs) != 0 {
		t.Errorf("queuedIDs = %d entradas, want 0 (sin basura tras el rechazo)", len(eng.queuedIDs))
	}
	if eng.jobsInFlight["rejected"] {
		t.Error("no debe lanzarse job para un ítem rechazado")
	}
}

// SEC-21: el techo de historial purga el ítem terminal MÁS ANTIGUO al encolar
// con el historial lleno, sin tocar los activos ni los demás terminales, y
// dejando el mapa de índice limpio.
func TestHistoryCeilingPurgesOldestTerminalItems(t *testing.T) {
	cm := telegram.NewClientManager()
	if err := cm.InitClient("1234567", "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("InitClient: %v", err)
	}
	t.Cleanup(cm.Stop)

	cfg := config.DefaultConfig()
	cfg.DownloadFolder = t.TempDir()
	eng := NewEngine(cm, nil, cfg)
	t.Cleanup(func() { eng.CancelAll() })

	// Historial lleno: los terminales más antiguos con CreatedAt creciente y
	// dos activos que la purga jamás debe tocar.
	const active = 2
	eng.mu.Lock()
	for i := 0; i < maxHistoryItems-active; i++ {
		id := fmt.Sprintf("term-%d", i)
		eng.downloads[id] = &storage.DownloadItem{
			ID:        id,
			ChatID:    1,
			MessageID: int64(i),
			Status:    "completed",
			CreatedAt: float64(i),
		}
		eng.chatMsgMap[fmt.Sprintf("1:%d", i)] = id
	}
	for i := 0; i < active; i++ {
		id := fmt.Sprintf("active-%d", i)
		eng.downloads[id] = &storage.DownloadItem{
			ID:        id,
			ChatID:    1,
			MessageID: int64(10000 + i),
			Status:    "paused",
			CreatedAt: float64(10000 + i),
		}
		eng.chatMsgMap[fmt.Sprintf("1:%d", 10000+i)] = id
	}
	eng.mu.Unlock()

	if got := len(eng.GetDownloads()); got != maxHistoryItems {
		t.Fatalf("preparación: historial = %d, want %d", got, maxHistoryItems)
	}

	// Al encolar, makeHistoryRoom debe purgar el más antiguo para dejar sitio.
	eng.QueueItem(storage.DownloadItem{ID: "new-item", ChatID: 9, MessageID: 1, FileName: "nueva.bin"})

	if _, ok := eng.GetItem("term-0"); ok {
		t.Error("el ítem terminal más antiguo (term-0) debe purgarse")
	}
	if _, ok := eng.GetItem("term-1"); !ok {
		t.Error("solo debe purgarse el más antiguo: term-1 debe seguir en el historial")
	}
	for i := 0; i < active; i++ {
		if _, ok := eng.GetItem(fmt.Sprintf("active-%d", i)); !ok {
			t.Errorf("active-%d está activo y no debe purgarse", i)
		}
	}
	if _, ok := eng.GetItem("new-item"); !ok {
		t.Error("la tarea recién encolada debe estar en el historial")
	}

	eng.mu.RLock()
	defer eng.mu.RUnlock()
	if len(eng.downloads) != maxHistoryItems {
		t.Errorf("e.downloads = %d, want %d (techo de historial respetado)",
			len(eng.downloads), maxHistoryItems)
	}
	if _, ok := eng.chatMsgMap["1:0"]; ok {
		t.Error("chatMsgMap debe quedar sin la entrada del ítem purgado")
	}
}

// ReorderDownloads reparte los CreatedAt ya existentes entre las tareas no
// terminales según el orden pedido, rompe los empates y no toca ni las tareas
// terminadas ni las que no vienen en la lista.
func TestReorderDownloadsReassignsCreatedAt(t *testing.T) {
	cm := telegram.NewClientManager()
	t.Cleanup(cm.Stop)

	cfg := config.DefaultConfig()
	cfg.DownloadFolder = t.TempDir()
	eng := NewEngine(cm, nil, cfg)

	eng.mu.Lock()
	eng.downloads["a"] = &storage.DownloadItem{ID: "a", Status: "paused", CreatedAt: 100}
	eng.downloads["b"] = &storage.DownloadItem{ID: "b", Status: "paused", CreatedAt: 200}
	// c y d comparten CreatedAt (rango encolado de golpe): el empate debe romperse.
	eng.downloads["c"] = &storage.DownloadItem{ID: "c", Status: "paused", CreatedAt: 300}
	eng.downloads["d"] = &storage.DownloadItem{ID: "d", Status: "paused", CreatedAt: 300}
	eng.downloads["done"] = &storage.DownloadItem{ID: "done", Status: "completed", CreatedAt: 50}
	eng.downloads["other"] = &storage.DownloadItem{ID: "other", Status: "paused", CreatedAt: 900}
	eng.mu.Unlock()

	// "d" al tope; "done" (terminal), "ghost" (inexistente) y el duplicado se ignoran.
	n := eng.ReorderDownloads([]string{"d", "a", "ghost", "done", "c", "b", "a"})
	if n != 4 {
		t.Fatalf("reordered = %d, want 4", n)
	}

	eng.mu.RLock()
	defer eng.mu.RUnlock()
	order := []string{"d", "a", "c", "b"}
	for i := 1; i < len(order); i++ {
		prev, cur := eng.downloads[order[i-1]], eng.downloads[order[i]]
		if !eng.shouldGoBefore(prev, cur) {
			t.Errorf("%s debería ir antes que %s (CreatedAt %v vs %v)", order[i-1], order[i], prev.CreatedAt, cur.CreatedAt)
		}
	}
	if eng.downloads["done"].CreatedAt != 50 {
		t.Errorf("una tarea terminada no debe cambiar: CreatedAt = %v", eng.downloads["done"].CreatedAt)
	}
	if eng.downloads["other"].CreatedAt != 900 {
		t.Errorf("una tarea fuera de la lista no debe cambiar: CreatedAt = %v", eng.downloads["other"].CreatedAt)
	}
}

// Una tarea movida al tope de la cola debe ser la que tome el siguiente hueco
// libre, aunque se hubiera encolado la última.
func TestReorderDownloadsChangesNextInQueue(t *testing.T) {
	cm := telegram.NewClientManager()
	if err := cm.InitClient("1234567", "0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("InitClient: %v", err)
	}
	t.Cleanup(cm.Stop)

	cfg := config.DefaultConfig()
	cfg.DownloadFolder = t.TempDir()
	cfg.MaxConcurrentDownloads = 1
	eng := NewEngine(cm, nil, cfg)

	eng.QueueItem(storage.DownloadItem{ID: "a", ChatID: 10, MessageID: 1, FileName: "a.bin"})
	waitForCondition(t, "a en curso", func() bool { return eng.statusOf("a") == "downloading" })
	eng.QueueItem(storage.DownloadItem{ID: "b", ChatID: 10, MessageID: 2, FileName: "b.bin"})
	eng.QueueItem(storage.DownloadItem{ID: "c", ChatID: 10, MessageID: 3, FileName: "c.bin"})

	// c pasa por delante de b.
	if n := eng.ReorderDownloads([]string{"a", "c", "b"}); n != 3 {
		t.Fatalf("reordered = %d, want 3", n)
	}

	if err := eng.CancelDownload("a"); err != nil {
		t.Fatal(err)
	}
	waitForCondition(t, "c toma el hueco antes que b", func() bool {
		return eng.statusOf("c") == "downloading"
	})
	if got := eng.statusOf("b"); got != "queued" {
		t.Errorf("b debería seguir en cola, está %q", got)
	}
}
