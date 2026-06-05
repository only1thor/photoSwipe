package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"photoSwipe/internal/handlers"
	"photoSwipe/internal/store"
)

//go:embed all:web
var webFS embed.FS

type config struct {
	photoDir string
	addr     string
	password string
	trashDir string
}

func loadConfig() (config, error) {
	var c config
	flag.StringVar(&c.photoDir, "photo-dir", envOr("PHOTOSWIPE_PHOTO_DIR", "/photos"), "directory containing photos to sort")
	flag.StringVar(&c.addr, "addr", envOr("PHOTOSWIPE_ADDR", ":8080"), "HTTP listen address")
	flag.StringVar(&c.password, "password", os.Getenv("PHOTOSWIPE_PASSWORD"), "shared password (env PHOTOSWIPE_PASSWORD)")
	flag.StringVar(&c.trashDir, "trash-dir", envOr("PHOTOSWIPE_TRASH_DIR", ""), "trash directory (default: <photo-dir>/.trash)")
	flag.Parse()

	if c.photoDir == "" {
		return c, errors.New("photo-dir is required")
	}
	abs, err := filepath.Abs(c.photoDir)
	if err != nil {
		return c, err
	}
	c.photoDir = abs

	if c.trashDir == "" {
		c.trashDir = filepath.Join(c.photoDir, ".trash")
	}
	if err := os.MkdirAll(c.trashDir, 0o755); err != nil {
		return c, err
	}

	if len(c.password) < 6 {
		return c, errors.New("PHOTOSWIPE_PASSWORD must be set and at least 6 characters")
	}
	return c, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	log.Printf("photoSwipe starting — photos=%s trash=%s addr=%s", cfg.photoDir, cfg.trashDir, cfg.addr)

	st, err := store.Open(cfg.photoDir)
	if err != nil {
		log.Fatalf("store: %v", err)
	}

	templatesFS, err := fs.Sub(webFS, "web/templates")
	if err != nil {
		log.Fatalf("templates fs: %v", err)
	}
	staticFS, err := fs.Sub(webFS, "web/static")
	if err != nil {
		log.Fatalf("static fs: %v", err)
	}

	h, err := handlers.New(handlers.Deps{
		Store:     st,
		PhotoDir:  cfg.photoDir,
		TrashDir:  cfg.trashDir,
		Password:  cfg.password,
		Templates: templatesFS,
		Static:    staticFS,
	})
	if err != nil {
		log.Fatalf("handlers: %v", err)
	}

	srv := &http.Server{
		Addr:              cfg.addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Print("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
	if err := st.Close(); err != nil {
		log.Printf("store close: %v", err)
	}
}
