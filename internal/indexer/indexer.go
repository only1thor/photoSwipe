// Package indexer hashes photos in the background so the duplicates view
// has data to work with. It walks the store's pool, picks one unhashed
// photo at a time, decodes its header, computes the dHash, and writes the
// result back.
package indexer

import (
	"image"
	"log"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"

	"photoSwipe/internal/dhash"
	"photoSwipe/internal/img"
	"photoSwipe/internal/store"
)

// Indexer is a single-goroutine worker that drains unhashed photos.
type Indexer struct {
	store    *store.Store
	photoDir string
	stop     chan struct{}
	done     chan struct{}
	idleNap  time.Duration

	// hashedThisRun is exposed for log/telemetry and tests.
	hashedThisRun atomic.Int64
}

func New(st *store.Store, photoDir string) *Indexer {
	return &Indexer{
		store:    st,
		photoDir: photoDir,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		idleNap:  30 * time.Second,
	}
}

// Start launches the worker. Safe to call once.
func (ix *Indexer) Start() {
	go ix.run()
}

// Stop signals the worker and waits for it to exit.
func (ix *Indexer) Stop() {
	close(ix.stop)
	<-ix.done
}

func (ix *Indexer) run() {
	defer close(ix.done)
	log.Print("indexer: started")
	for {
		select {
		case <-ix.stop:
			log.Printf("indexer: stopped (hashed %d this run)", ix.hashedThisRun.Load())
			return
		default:
		}

		p := ix.store.NextUnhashed()
		if p == nil {
			select {
			case <-ix.stop:
				return
			case <-time.After(ix.idleNap):
				continue
			}
		}

		if err := ix.hashOne(p); err != nil {
			log.Printf("indexer: %s: %v", p.Path, err)
			_ = ix.store.MarkHashFailed(p.ID)
			continue
		}
		ix.hashedThisRun.Add(1)
	}
}

func (ix *Indexer) hashOne(p *store.Photo) error {
	abs := filepath.Join(ix.photoDir, filepath.FromSlash(p.Path))
	exif := img.ReadExifFile(abs)
	if !exif.DateTimeOriginal.IsZero() {
		_ = ix.store.SetCaptureTime(p.ID, exif.DateTimeOriginal)
	}
	f, err := os.Open(abs)
	if err != nil {
		return err
	}
	defer f.Close()
	decoded, _, err := image.Decode(f)
	if err != nil {
		return err
	}
	oriented := img.ApplyOrientation(decoded, exif.Orientation)
	h := dhash.Compute(oriented)
	return ix.store.SetHash(p.ID, h.H, h.V)
}
