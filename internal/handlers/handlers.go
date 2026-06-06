package handlers

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"photoSwipe/internal/auth"
	"photoSwipe/internal/dupes"
	"photoSwipe/internal/img"
	"photoSwipe/internal/queue"
	"photoSwipe/internal/store"
)

type Deps struct {
	Store     *store.Store
	PhotoDir  string
	TrashDir  string
	Password  string
	Templates fs.FS
	Static    fs.FS
}

type handler struct {
	deps     Deps
	auth     *auth.Auth
	selector *queue.Selector

	tplLogin    *template.Template
	tplPhoto    *template.Template
	tplSummary  *template.Template
	tplSettings *template.Template
	tplStats    *template.Template
	tplCard     *template.Template
	tplCluster  *template.Template
	tplMeta     *template.Template
}

// Counts mirrors store.Counts as a struct for template use.
type Counts struct {
	Unsorted, Kept, Unsure, Trashed int
}

func New(d Deps) (http.Handler, error) {
	h := &handler{
		deps:     d,
		auth:     auth.New(d.Password),
		selector: queue.New(),
	}
	if err := h.loadTemplates(); err != nil {
		return nil, fmt.Errorf("templates: %w", err)
	}

	added, total, err := img.Scan(d.PhotoDir, d.TrashDir, d.Store)
	if err != nil {
		return nil, fmt.Errorf("initial scan: %w", err)
	}
	log.Printf("scan: %d new, %d total", added, total)

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(d.Static))))
	mux.Handle("/login", h.auth.HandleLogin(h.renderLogin))
	mux.HandleFunc("GET /{$}", h.handleHome)
	mux.HandleFunc("POST /session/end", h.handleSessionEnd)
	mux.HandleFunc("POST /session/extend", h.handleSessionExtend)
	mux.HandleFunc("GET /next", h.handleNext)
	mux.HandleFunc("POST /decision", h.handleDecision)
	mux.HandleFunc("POST /cluster/resolve", h.handleClusterResolve)
	mux.HandleFunc("POST /undo", h.handleUndo)
	mux.HandleFunc("GET /photo/{id}", h.handleServePhoto)
	mux.HandleFunc("GET /thumb/{id}", h.handleThumb)
	mux.HandleFunc("GET /meta/{id}", h.handleMeta)
	mux.HandleFunc("GET /settings", h.handleSettings)
	mux.HandleFunc("POST /settings", h.handleUpdateSettings)
	mux.HandleFunc("GET /stats", h.handleStats)
	mux.HandleFunc("POST /rescan", h.handleRescan)

	return h.auth.Middleware(mux), nil
}

func (h *handler) loadTemplates() error {
	var err error
	layoutBytes, err := fs.ReadFile(h.deps.Templates, "layout.html")
	if err != nil {
		return err
	}
	makePage := func(file string) (*template.Template, error) {
		t := template.New(file)
		if _, err := t.Parse(string(layoutBytes)); err != nil {
			return nil, err
		}
		b, err := fs.ReadFile(h.deps.Templates, file)
		if err != nil {
			return nil, err
		}
		// Card + cluster fragments may be embedded in pages.
		for _, frag := range []string{"frag_card.html", "frag_cluster.html"} {
			fb, _ := fs.ReadFile(h.deps.Templates, frag)
			if len(fb) == 0 {
				continue
			}
			if _, err := t.Parse(string(fb)); err != nil {
				return nil, err
			}
		}
		if _, err := t.Parse(string(b)); err != nil {
			return nil, err
		}
		return t, nil
	}
	makeFragment := func(file, name string) (*template.Template, error) {
		b, err := fs.ReadFile(h.deps.Templates, file)
		if err != nil {
			return nil, err
		}
		return template.New(name).Parse(string(b))
	}

	if h.tplLogin, err = makeFragment("login.html", "login"); err != nil {
		return err
	}
	if h.tplPhoto, err = makePage("photo.html"); err != nil {
		return err
	}
	if h.tplSummary, err = makePage("summary.html"); err != nil {
		return err
	}
	if h.tplSettings, err = makePage("settings.html"); err != nil {
		return err
	}
	if h.tplStats, err = makePage("stats.html"); err != nil {
		return err
	}
	if h.tplCard, err = makeFragment("frag_card.html", "card"); err != nil {
		return err
	}
	if h.tplCluster, err = makeFragment("frag_cluster.html", "cluster"); err != nil {
		return err
	}
	if h.tplMeta, err = makeFragment("frag_meta.html", "meta"); err != nil {
		return err
	}
	return nil
}

// --- helpers ---

type pageData struct {
	BodyClass        string
	Session          *store.Session
	Counts           Counts
	Photo            *store.Photo
	Settings         store.Settings
	Done             int
	ShowFatigueNudge bool
	TodayCount       int
	Error            string
	Width, Height    int
	Path, PathShort  string
	SizeKB           int64
	ModTime          string
	Indexing         bool
	Indexed          int
	Total            int

	// Stats page
	WeekCount     int
	LastBatchDone int
	Encouragement string
	DailyBars     []DailyBar

	// Cluster card fragment
	ClusterID string
	Photos    []ClusterMember
}

// DailyBar is one bar in the 7-day decision sparkline on /stats.
type DailyBar struct {
	Day    string // "Mon", "Tue", …
	Count  int
	Height int // 0–100, for CSS height %
	IsToday bool
}

// ClusterMember is a view-model for one entry in the inline cluster grid.
type ClusterMember struct {
	ID, PathShort, TimeStr string
	SizeKB                 int64
}

// Counts mirrors store.Counts. Unsure is preserved as a struct field name
// for back-compat with template references; surfaced as "Skipped" in UI.
func (h *handler) counts() Counts {
	u, k, m, t := h.deps.Store.Counts()
	return Counts{Unsorted: u, Kept: k, Unsure: m, Trashed: t}
}

func (h *handler) renderPage(w http.ResponseWriter, t *template.Template, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		log.Printf("render: %v", err)
	}
}

func (h *handler) renderFragment(w http.ResponseWriter, t *template.Template, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		log.Printf("fragment %s: %v", name, err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	_, _ = buf.WriteTo(w)
}

func (h *handler) renderLogin(w http.ResponseWriter, errMsg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tplLogin.ExecuteTemplate(w, "login", struct{ Error string }{errMsg}); err != nil {
		log.Printf("login render: %v", err)
	}
}

// --- route handlers ---

func (h *handler) handleHome(w http.ResponseWriter, r *http.Request) {
	sess := h.deps.Store.Session()
	if sess == nil {
		newSess, err := h.deps.Store.AutoStartSession()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sess = newSess
	}
	if sess.Complete() {
		h.renderPage(w, h.tplSummary, h.summaryData(sess))
		return
	}
	photo, err := h.pickNext(sess)
	if errors.Is(err, queue.ErrNoCandidate) {
		h.renderPage(w, h.tplSummary, h.summaryData(sess))
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := pageData{
		BodyClass: "photo-body",
		Session:   sess,
		Counts:    h.counts(),
		Photo:     photo,
		Settings:  h.deps.Store.Settings(),
	}
	if cluster := h.openClusterFor(photo); cluster != nil {
		data.ClusterID = cluster.ID
		data.Photos = clusterMembers(cluster)
	}
	h.renderPage(w, h.tplPhoto, data)
}

func (h *handler) handleSessionEnd(w http.ResponseWriter, r *http.Request) {
	if err := h.deps.Store.EndSession(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/stats", http.StatusSeeOther)
}

func (h *handler) handleSessionExtend(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	delta, _ := strconv.Atoi(r.PostFormValue("delta"))
	if delta == 0 {
		delta = h.deps.Store.Settings().DefaultBatchSize
	}
	if err := h.deps.Store.SessionExtend(delta); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *handler) handleNext(w http.ResponseWriter, r *http.Request) {
	h.renderNext(w)
}

// renderForPhoto renders the swipe view for a specific photo — either as
// a single card or, if the photo is part of an unresolved cluster, as
// the cluster fragment. Used by handleUndo so undo brings back the photo
// the user just acted on, not a fresh weighted-random pick.
// triggerSessionUpdate emits an HX-Trigger event so the client can refresh
// the header progress chip without a full page reload. The detail carries
// the current Done / Target so the JS handler just sets textContent.
func (h *handler) triggerSessionUpdate(w http.ResponseWriter) {
	sess := h.deps.Store.Session()
	if sess == nil {
		w.Header().Set("HX-Trigger", `{"session-updated":{"done":0,"target":0,"active":false}}`)
		return
	}
	w.Header().Set("HX-Trigger",
		fmt.Sprintf(`{"session-updated":{"done":%d,"target":%d,"active":true}}`,
			sess.Done, sess.Target))
}

func (h *handler) renderForPhoto(w http.ResponseWriter, p *store.Photo) {
	settings := h.deps.Store.Settings()
	if cluster := h.openClusterFor(p); cluster != nil {
		h.renderFragment(w, h.tplCluster, "cluster", pageData{
			ClusterID: cluster.ID,
			Photos:    clusterMembers(cluster),
			Settings:  settings,
		})
		return
	}
	h.renderFragment(w, h.tplCard, "card", pageData{Photo: p, Settings: settings})
}

// renderClusterByID renders the cluster fragment for the cluster whose
// ID matches. Used by undo of a cluster decision so the same cluster
// (now reformed after the trash-undo) is presented for redecision.
// Returns false if no such cluster exists right now.
func (h *handler) renderClusterByID(w http.ResponseWriter, id string) bool {
	settings := h.deps.Store.Settings()
	window := time.Duration(settings.DupeTimeWindowHours * float64(time.Hour))
	clusters := dupes.Find(h.deps.Store.AllPhotos(), settings.DupeThreshold, window)
	for i := range clusters {
		if clusters[i].ID != id {
			continue
		}
		h.renderFragment(w, h.tplCluster, "cluster", pageData{
			ClusterID: clusters[i].ID,
			Photos:    clusterMembers(&clusters[i]),
			Settings:  settings,
		})
		return true
	}
	return false
}

// renderNext picks the next photo and writes either the single-photo card
// fragment or, if the picked photo is part of an unresolved cluster, the
// cluster card fragment. Used by handleNext, handleDecision, and
// handleClusterResolve when they need to swap #photo-area.
func (h *handler) renderNext(w http.ResponseWriter) {
	sess := h.deps.Store.Session()
	if sess == nil || sess.Complete() {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	photo, err := h.pickNext(sess)
	if errors.Is(err, queue.ErrNoCandidate) {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	settings := h.deps.Store.Settings()
	if cluster := h.openClusterFor(photo); cluster != nil {
		h.renderFragment(w, h.tplCluster, "cluster", pageData{
			ClusterID: cluster.ID,
			Photos:    clusterMembers(cluster),
			Settings:  settings,
		})
		return
	}
	h.renderFragment(w, h.tplCard, "card", pageData{Photo: photo, Settings: settings})
}

func (h *handler) handleDecision(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	action := r.PostFormValue("action")
	photoID := r.PostFormValue("photo_id")

	photo, ok := h.deps.Store.GetPhoto(photoID)
	if !ok {
		http.Error(w, "photo not found", http.StatusNotFound)
		return
	}

	// Skip: photo is left as if never shown. Back-compat: also accept
	// "unsure" from any pre-rename client.
	if action == "skip" || action == "unsure" {
		if _, err := h.deps.Store.RecordSkip(photoID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// HX-Trigger so the header progress chip updates live.
		h.triggerSessionUpdate(w)
		h.renderNext(w)
		return
	}

	var newState store.PhotoState
	var trashFrom, trashTo string
	switch action {
	case "keep":
		newState = store.StateKept
	case "trash":
		newState = store.StateTrashed
		src, err := h.resolvePhotoPath(photo.Path)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dst, err := img.MoveToTrash(src, h.deps.PhotoDir, h.deps.TrashDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		trashFrom, trashTo = src, dst
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	if _, err := h.deps.Store.RecordDecision(photoID, newState, trashFrom, trashTo); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.triggerSessionUpdate(w)
	h.renderNext(w)
}

func (h *handler) handleUndo(w http.ResponseWriter, r *http.Request) {
	d, err := h.deps.Store.Undo()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.triggerSessionUpdate(w)
	switch {
	case d.Cluster != nil:
		// Restore every trashed member's file, then re-render the cluster
		// so the user can redo the decision on the same group.
		for _, op := range d.Cluster {
			if op.NewState == store.StateTrashed && op.TrashTo != "" && op.TrashFrom != "" {
				if err := img.RestoreFromTrash(op.TrashTo, op.TrashFrom); err != nil {
					log.Printf("cluster restore %s: %v", op.PhotoID, err)
				}
			}
		}
		// d.PhotoID was the cluster ID at apply time. Try to find it again.
		if !h.renderClusterByID(w, d.PhotoID) {
			h.renderNext(w)
		}
	case d.Skipped:
		// Show the just-unskipped photo (now back in the pool, no file move).
		if p, ok := h.deps.Store.GetPhoto(d.PhotoID); ok {
			h.renderForPhoto(w, p)
			return
		}
		h.renderNext(w)
	default:
		if d.NewState == store.StateTrashed && d.TrashTo != "" && d.TrashFrom != "" {
			if err := img.RestoreFromTrash(d.TrashTo, d.TrashFrom); err != nil {
				log.Printf("restore failed: %v", err)
			}
		}
		if p, ok := h.deps.Store.GetPhoto(d.PhotoID); ok {
			h.renderForPhoto(w, p)
			return
		}
		h.renderNext(w)
	}
}

func (h *handler) handleServePhoto(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	photo, ok := h.deps.Store.GetPhoto(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	abs, err := h.resolvePhotoPath(photo.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeFile(w, r, abs)
}

func (h *handler) handleMeta(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	photo, ok := h.deps.Store.GetPhoto(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	abs, err := h.resolvePhotoPath(photo.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m, err := img.Inspect(abs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	short := photo.Path
	if i := strings.LastIndex(short, "/"); i >= 0 {
		short = short[i+1:]
	}
	h.renderFragment(w, h.tplMeta, "meta", pageData{
		Path:      photo.Path,
		PathShort: short,
		Width:     m.Width,
		Height:    m.Height,
		SizeKB:    m.SizeKB,
		ModTime:   m.Modified.Format("2006-01-02"),
	})
}

func (h *handler) handleSettings(w http.ResponseWriter, r *http.Request) {
	hashed, total := h.deps.Store.HashProgress()
	h.renderPage(w, h.tplSettings, pageData{
		BodyClass: "settings-body",
		Session:   h.deps.Store.Session(),
		Settings:  h.deps.Store.Settings(),
		Counts:    h.counts(),
		Indexing:  hashed < total,
		Indexed:   hashed,
		Total:     total,
	})
}

func (h *handler) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cur := h.deps.Store.Settings()
	parseF := func(name string, fallback float64) float64 {
		v, err := strconv.ParseFloat(r.PostFormValue(name), 64)
		if err != nil {
			return fallback
		}
		return v
	}
	parseI := func(name string, fallback int) int {
		v, err := strconv.Atoi(r.PostFormValue(name))
		if err != nil {
			return fallback
		}
		return v
	}
	cur.BaseRate = parseF("base_rate", cur.BaseRate)
	cur.Decay = parseF("decay", cur.Decay)
	cur.UnsureBaseRate = parseF("unsure_base_rate", cur.UnsureBaseRate)
	cur.CooldownHours = parseF("cooldown_hours", cur.CooldownHours)
	cur.LockThreshold = parseI("lock_threshold", cur.LockThreshold)
	cur.FatigueNudge = r.PostFormValue("fatigue_nudge") != ""
	cur.FatigueThreshold = parseI("fatigue_threshold", cur.FatigueThreshold)
	cur.DupeThreshold = parseI("dupe_threshold", cur.DupeThreshold)
	cur.DupeTimeWindowHours = parseF("dupe_time_window_hours", cur.DupeTimeWindowHours)
	cur.DefaultBatchSize = parseI("default_batch_size", cur.DefaultBatchSize)
	if mix := store.CompositionMix(r.PostFormValue("default_mix")); mix.Valid() {
		cur.DefaultMix = mix
	}
	cur.SkipAdvancesCounter = r.PostFormValue("skip_advances_counter") != ""
	cur.InfoOverlay = r.PostFormValue("info_overlay") != ""
	cur.ClusterCountsAsOne = r.PostFormValue("cluster_counts_as_one") != ""
	if err := h.deps.Store.UpdateSettings(cur); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (h *handler) handleStats(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	today := now.Format("2006-01-02")
	todayCount := h.deps.Store.DailyCount(today)
	var weekCount int
	bars := make([]DailyBar, 7)
	var maxCount int
	for i := 6; i >= 0; i-- {
		d := now.AddDate(0, 0, -i)
		key := d.Format("2006-01-02")
		c := h.deps.Store.DailyCount(key)
		weekCount += c
		bars[6-i] = DailyBar{
			Day:     d.Format("Mon"),
			Count:   c,
			IsToday: i == 0,
		}
		if c > maxCount {
			maxCount = c
		}
	}
	for i := range bars {
		if maxCount > 0 {
			bars[i].Height = bars[i].Count * 100 / maxCount
		}
	}
	lastBatch := h.deps.Store.LastBatchDone()

	h.renderPage(w, h.tplStats, pageData{
		BodyClass:     "stats-body",
		Counts:        h.counts(),
		TodayCount:    todayCount,
		WeekCount:     weekCount,
		LastBatchDone: lastBatch,
		DailyBars:     bars,
		Encouragement: encouragementFor(lastBatch, todayCount),
	})
}

// encouragementFor returns a short, varied affirming line. Kept here
// rather than in the template so the template stays a clean view layer.
func encouragementFor(batch, today int) string {
	switch {
	case batch == 0 && today == 0:
		return "Nothing decided yet — fresh slate whenever you're ready."
	case batch == 0:
		return "Done for now. Come back any time."
	case batch >= 50:
		return "Big batch — that's real progress. Take a breather."
	case batch >= 20:
		return "Solid work. Your future self will thank you."
	case batch >= 10:
		return "Nice batch. Small steps, real progress."
	default:
		return "Every decision counts. Well done."
	}
}

func (h *handler) handleRescan(w http.ResponseWriter, r *http.Request) {
	added, total, err := img.Scan(h.deps.PhotoDir, h.deps.TrashDir, h.deps.Store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<span class="ok">scan: %d new · %d total</span>`, added, total)
}

// --- internals ---

func (h *handler) pickNext(sess *store.Session) (*store.Photo, error) {
	photos := h.deps.Store.AllPhotos()
	return h.selector.Next(photos, sess, h.deps.Store.Settings(), time.Now())
}

func (h *handler) summaryData(sess *store.Session) pageData {
	settings := h.deps.Store.Settings()
	today := time.Now().Format("2006-01-02")
	todayCount := h.deps.Store.DailyCount(today)
	done := 0
	if sess != nil {
		done = sess.Done
	}
	return pageData{
		BodyClass:        "summary-body",
		Session:          sess,
		Counts:           h.counts(),
		Done:             done,
		ShowFatigueNudge: settings.FatigueNudge && todayCount >= settings.FatigueThreshold,
		TodayCount:       todayCount,
	}
}

func (h *handler) handleThumb(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	width := 600
	if s := r.URL.Query().Get("w"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			width = n
		}
	}
	photo, ok := h.deps.Store.GetPhoto(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	abs, err := h.resolvePhotoPath(photo.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if err := img.ServeThumb(abs, h.deps.PhotoDir, id, width, w); err != nil {
		log.Printf("thumb %s: %v", id, err)
	}
}

// handleClusterResolve applies a "keep these, trash the rest" decision to
// a near-duplicate cluster surfaced inline from the swipe view. If no
// keep[] values are posted, the entire cluster is trashed (the user's
// "dismiss the entire set" default). If action=skip is posted, the
// cluster's members are added to RecentlySkipped instead.
func (h *handler) handleClusterResolve(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	clusterID := r.PostFormValue("cluster_id")
	action := r.PostFormValue("action")

	settings := h.deps.Store.Settings()
	window := time.Duration(settings.DupeTimeWindowHours * float64(time.Hour))
	clusters := dupes.Find(h.deps.Store.AllPhotos(), settings.DupeThreshold, window)

	var target *dupes.Cluster
	for i := range clusters {
		if clusters[i].ID == clusterID {
			target = &clusters[i]
			break
		}
	}
	if target == nil {
		// Cluster vanished (already resolved by another tab); just move on.
		h.triggerSessionUpdate(w)
		h.renderNext(w)
		return
	}

	// Skip cluster: push every member onto RecentlySkipped and record a
	// single Decision so the whole cluster costs one batch slot (matches
	// keep/trash behavior; honors Settings.ClusterCountsAsOne).
	if action == "skip" {
		var ids []string
		for _, p := range target.Photos {
			if p.State == store.StateUnsorted {
				ids = append(ids, p.ID)
			}
		}
		if _, err := h.deps.Store.SkipCluster(clusterID, ids); err != nil {
			log.Printf("cluster skip %s: %v", clusterID, err)
		}
		h.triggerSessionUpdate(w)
		h.renderNext(w)
		return
	}

	keepSet := map[string]bool{}
	for _, id := range r.PostForm["keep"] {
		keepSet[id] = true
	}

	var ops []store.ClusterMemberOp
	for _, p := range target.Photos {
		if p.State != store.StateUnsorted {
			continue // already decided; leave it alone
		}
		if keepSet[p.ID] {
			ops = append(ops, store.ClusterMemberOp{
				PhotoID:  p.ID,
				NewState: store.StateKept,
			})
			continue
		}
		// Trash: move the file before we record the decision.
		src, err := h.resolvePhotoPath(p.Path)
		if err != nil {
			log.Printf("resolve %s: %v", p.ID, err)
			continue
		}
		dst, err := img.MoveToTrash(src, h.deps.PhotoDir, h.deps.TrashDir)
		if err != nil {
			log.Printf("trash %s: %v", p.ID, err)
			continue
		}
		ops = append(ops, store.ClusterMemberOp{
			PhotoID:   p.ID,
			NewState:  store.StateTrashed,
			TrashFrom: src,
			TrashTo:   dst,
		})
	}
	if len(ops) == 0 {
		h.triggerSessionUpdate(w)
		h.renderNext(w)
		return
	}
	if _, err := h.deps.Store.RecordClusterDecision(clusterID, ops); err != nil {
		log.Printf("record cluster %s: %v", clusterID, err)
	}
	h.triggerSessionUpdate(w)
	h.renderNext(w)
}

// openClusterFor returns the cluster containing p iff that cluster has at
// least two still-unsorted members. Otherwise nil — meaning "render the
// normal single-photo card".
func (h *handler) openClusterFor(p *store.Photo) *dupes.Cluster {
	if p == nil {
		return nil
	}
	settings := h.deps.Store.Settings()
	window := time.Duration(settings.DupeTimeWindowHours * float64(time.Hour))
	clusters := dupes.Find(h.deps.Store.AllPhotos(), settings.DupeThreshold, window)
	for i := range clusters {
		var contains bool
		var unsorted int
		for _, m := range clusters[i].Photos {
			if m.ID == p.ID {
				contains = true
			}
			if m.State == store.StateUnsorted {
				unsorted++
			}
		}
		if contains && unsorted >= 2 {
			return &clusters[i]
		}
	}
	return nil
}

// clusterMembers returns a view-model of all unsorted members in a cluster.
func clusterMembers(c *dupes.Cluster) []ClusterMember {
	out := make([]ClusterMember, 0, len(c.Photos))
	for _, p := range c.Photos {
		if p.State != store.StateUnsorted {
			continue
		}
		short := p.Path
		if i := strings.LastIndex(short, "/"); i >= 0 {
			short = short[i+1:]
		}
		var ts string
		if !p.Time.IsZero() {
			ts = p.Time.Format("2006-01-02 15:04")
		}
		out = append(out, ClusterMember{
			ID:        p.ID,
			PathShort: short,
			TimeStr:   ts,
			SizeKB:    (p.SizeBytes + 1023) / 1024,
		})
	}
	return out
}

// resolvePhotoPath joins relPath under photoDir, rejecting traversal.
func (h *handler) resolvePhotoPath(relPath string) (string, error) {
	abs := filepath.Join(h.deps.PhotoDir, filepath.FromSlash(relPath))
	clean := filepath.Clean(abs)
	if !strings.HasPrefix(clean, filepath.Clean(h.deps.PhotoDir)+string(filepath.Separator)) && clean != filepath.Clean(h.deps.PhotoDir) {
		return "", errors.New("path traversal blocked")
	}
	return clean, nil
}
