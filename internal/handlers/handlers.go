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

	tplLogin        *template.Template
	tplSessionStart *template.Template
	tplPhoto        *template.Template
	tplSummary      *template.Template
	tplSettings     *template.Template
	tplCard         *template.Template
	tplMeta         *template.Template
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
	mux.HandleFunc("POST /session/start", h.handleSessionStart)
	mux.HandleFunc("POST /session/end", h.handleSessionEnd)
	mux.HandleFunc("POST /session/extend", h.handleSessionExtend)
	mux.HandleFunc("GET /next", h.handleNext)
	mux.HandleFunc("POST /decision", h.handleDecision)
	mux.HandleFunc("POST /undo", h.handleUndo)
	mux.HandleFunc("GET /photo/{id}", h.handleServePhoto)
	mux.HandleFunc("GET /meta/{id}", h.handleMeta)
	mux.HandleFunc("GET /settings", h.handleSettings)
	mux.HandleFunc("POST /settings", h.handleUpdateSettings)
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
		// Cards may be embedded in pages
		cardBytes, _ := fs.ReadFile(h.deps.Templates, "frag_card.html")
		if len(cardBytes) > 0 {
			if _, err := t.Parse(string(cardBytes)); err != nil {
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
	if h.tplSessionStart, err = makePage("session_start.html"); err != nil {
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
	if h.tplCard, err = makeFragment("frag_card.html", "card"); err != nil {
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
}

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
		h.renderPage(w, h.tplSessionStart, pageData{
			BodyClass: "session-start-body",
			Counts:    h.counts(),
		})
		return
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
	h.renderPage(w, h.tplPhoto, pageData{
		BodyClass: "photo-body",
		Session:   sess,
		Counts:    h.counts(),
		Photo:     photo,
	})
}

func (h *handler) handleSessionStart(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	target := 50
	if v := r.PostFormValue("target_custom"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			target = n
		}
	} else if v := r.PostFormValue("target"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			target = n
		}
	}
	mix := store.CompositionMix(r.PostFormValue("mix"))
	if !mix.Valid() {
		mix = store.MixMixed
	}
	if _, err := h.deps.Store.StartSession(target, mix); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *handler) handleSessionEnd(w http.ResponseWriter, r *http.Request) {
	if err := h.deps.Store.EndSession(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *handler) handleSessionExtend(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	delta, _ := strconv.Atoi(r.PostFormValue("delta"))
	if err := h.deps.Store.SessionExtend(delta); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *handler) handleNext(w http.ResponseWriter, r *http.Request) {
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
	h.renderFragment(w, h.tplCard, "card", pageData{Photo: photo})
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

	var newState store.PhotoState
	var trashFrom, trashTo string
	switch action {
	case "keep":
		newState = store.StateKept
	case "unsure":
		newState = store.StateUnsure
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

	sess := h.deps.Store.Session()
	if sess == nil || sess.Complete() {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	next, err := h.pickNext(sess)
	if errors.Is(err, queue.ErrNoCandidate) {
		w.Header().Set("HX-Redirect", "/")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// We swap #photo-area innerHTML; deliver the card fragment.
	h.renderFragment(w, h.tplCard, "card", pageData{Photo: next})
}

func (h *handler) handleUndo(w http.ResponseWriter, r *http.Request) {
	d, err := h.deps.Store.Undo()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if d.NewState == store.StateTrashed && d.TrashTo != "" && d.TrashFrom != "" {
		if err := img.RestoreFromTrash(d.TrashTo, d.TrashFrom); err != nil {
			log.Printf("restore failed: %v", err)
		}
	}
	photo, ok := h.deps.Store.GetPhoto(d.PhotoID)
	if !ok {
		http.Error(w, "photo missing", http.StatusInternalServerError)
		return
	}
	h.renderFragment(w, h.tplCard, "card", pageData{Photo: photo})
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
	h.renderPage(w, h.tplSettings, pageData{
		BodyClass: "settings-body",
		Session:   h.deps.Store.Session(),
		Settings:  h.deps.Store.Settings(),
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
	if err := h.deps.Store.UpdateSettings(cur); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
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

// resolvePhotoPath joins relPath under photoDir, rejecting traversal.
func (h *handler) resolvePhotoPath(relPath string) (string, error) {
	abs := filepath.Join(h.deps.PhotoDir, filepath.FromSlash(relPath))
	clean := filepath.Clean(abs)
	if !strings.HasPrefix(clean, filepath.Clean(h.deps.PhotoDir)+string(filepath.Separator)) && clean != filepath.Clean(h.deps.PhotoDir) {
		return "", errors.New("path traversal blocked")
	}
	return clean, nil
}
