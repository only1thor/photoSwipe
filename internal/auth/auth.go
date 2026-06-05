package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	cookieName   = "photoswipe_session"
	sessionTTL   = 30 * 24 * time.Hour
	cleanupEvery = 1 * time.Hour
	loginPath    = "/login"
	logoutPath   = "/logout"
	staticPrefix = "/static/"
)

type Auth struct {
	password []byte
	mu       sync.Mutex
	tokens   map[string]time.Time
	stopGC   chan struct{}
}

func New(password string) *Auth {
	a := &Auth{
		password: []byte(password),
		tokens:   map[string]time.Time{},
		stopGC:   make(chan struct{}),
	}
	go a.gcLoop()
	return a
}

func (a *Auth) Close() {
	close(a.stopGC)
}

func (a *Auth) gcLoop() {
	t := time.NewTicker(cleanupEvery)
	defer t.Stop()
	for {
		select {
		case <-a.stopGC:
			return
		case <-t.C:
			a.gc()
		}
	}
}

func (a *Auth) gc() {
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for tok, exp := range a.tokens {
		if now.After(exp) {
			delete(a.tokens, tok)
		}
	}
}

// Middleware gates all requests except /login and /static/*.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == loginPath || strings.HasPrefix(r.URL.Path, staticPrefix) {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == logoutPath {
			a.handleLogout(w, r)
			return
		}
		if !a.authenticated(r) {
			http.Redirect(w, r, loginPath, http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Auth) authenticated(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.tokens[c.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(a.tokens, c.Value)
		return false
	}
	return true
}

// HandleLogin processes both GET (form) and POST (submission).
func (a *Auth) HandleLogin(renderForm func(w http.ResponseWriter, errMsg string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			renderForm(w, "")
		case http.MethodPost:
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			pw := r.PostFormValue("password")
			if subtle.ConstantTimeCompare([]byte(pw), a.password) != 1 {
				w.WriteHeader(http.StatusUnauthorized)
				renderForm(w, "Wrong password")
				return
			}
			tok, err := newToken()
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			a.mu.Lock()
			a.tokens[tok] = time.Now().Add(sessionTTL)
			a.mu.Unlock()

			http.SetCookie(w, &http.Cookie{
				Name:     cookieName,
				Value:    tok,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
				Secure:   r.TLS != nil,
				Expires:  time.Now().Add(sessionTTL),
			})
			http.Redirect(w, r, "/", http.StatusSeeOther)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.mu.Lock()
		delete(a.tokens, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:    cookieName,
		Value:   "",
		Path:    "/",
		Expires: time.Unix(0, 0),
	})
	http.Redirect(w, r, loginPath, http.StatusSeeOther)
}

func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
