package watcher

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"

	"github.com/dector/gust/internal/logger"
)

// Event is a filesystem change that should trigger a rerun.
type Event struct {
	Path string
	Op   fsnotify.Op
}

// Watcher recursively watches a project root and publishes triggering events.
type Watcher struct {
	root         string
	excludes     []string
	excludeGlobs []string
	log          *logger.Logger

	fsw    *fsnotify.Watcher
	events chan Event
	done   chan struct{}

	mu      sync.Mutex
	watched map[string]struct{}
	once    sync.Once
}

var defaultExcludeDirs = map[string]struct{}{
	".git":         {},
	"node_modules": {},
	"vendor":       {},
	"tmp":          {},
	"dist":         {},
	"build":        {},
	".cache":       {},
}

// Start creates a recursive watcher rooted at root. The returned watcher is
// stopped when ctx is canceled or Close is called.
func Start(ctx context.Context, root string, excludes []string, excludeGlobs []string, log *logger.Logger) (*Watcher, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		root:         filepath.Clean(absRoot),
		excludes:     cleanExcludePrefixes(excludes),
		excludeGlobs: cleanExcludeGlobs(excludeGlobs),
		log:          log,
		fsw:          fsw,
		events:       make(chan Event, 32),
		done:         make(chan struct{}),
		watched:      make(map[string]struct{}),
	}
	if err := w.addRootAndExistingDirs(); err != nil {
		_ = fsw.Close()
		return nil, err
	}
	go w.run(ctx)
	return w, nil
}

// Events returns filesystem events that should trigger a rerun.
func (w *Watcher) Events() <-chan Event { return w.events }

// Done is closed after the watcher event loop exits.
func (w *Watcher) Done() <-chan struct{} { return w.done }

// Close stops the watcher.
func (w *Watcher) Close() error {
	var err error
	w.once.Do(func() { err = w.fsw.Close() })
	return err
}

func (w *Watcher) run(ctx context.Context) {
	defer close(w.done)
	defer close(w.events)
	defer w.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handle(event)
		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			if w.log != nil {
				w.log.Printf("watcher error: %v", err)
			}
		}
	}
}

func (w *Watcher) handle(event fsnotify.Event) {
	path := filepath.Clean(event.Name)
	w.verbosef("watcher event: %s %s", event.Op.String(), path)

	if event.Op&fsnotify.Chmod != 0 && event.Op&^(fsnotify.Chmod) == 0 {
		w.verbosef("watcher ignored chmod: %s", path)
		return
	}
	triggerOp := event.Op & (fsnotify.Write | fsnotify.Create | fsnotify.Remove | fsnotify.Rename)
	if triggerOp == 0 {
		return
	}
	if w.isExcluded(path) {
		w.verbosef("watcher ignored excluded path: %s", path)
		return
	}
	if event.Op&fsnotify.Create != 0 {
		w.addCreatedDirectory(path)
	}
	select {
	case w.events <- Event{Path: path, Op: triggerOp}:
	default:
		w.verbosef("watcher dropped event because queue is full: %s", path)
	}
}

func (w *Watcher) addCreatedDirectory(path string) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return
	}
	if err := w.addDirRecursive(path, false); err != nil && w.log != nil {
		w.log.Printf("warning: cannot watch directory %s: %v", path, err)
	}
}

func (w *Watcher) addRootAndExistingDirs() error {
	if err := w.addDir(w.root); err != nil {
		return fmt.Errorf("cannot watch root %s: %w", w.root, err)
	}
	return filepath.WalkDir(w.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == w.root {
				return err
			}
			if w.log != nil {
				w.log.Printf("warning: cannot inspect directory %s: %v", path, err)
			}
			return filepath.SkipDir
		}
		if path == w.root {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || w.isExcluded(path) {
			return filepath.SkipDir
		}
		if err := w.addDir(path); err != nil && w.log != nil {
			w.log.Printf("warning: cannot watch directory %s: %v", path, err)
		}
		return nil
	})
}

func (w *Watcher) addDirRecursive(root string, rootRequired bool) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if rootRequired && path == root {
				return err
			}
			return filepath.SkipDir
		}
		if !d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || w.isExcluded(path) {
			return filepath.SkipDir
		}
		if err := w.addDir(path); err != nil {
			if rootRequired && path == root {
				return err
			}
			if w.log != nil {
				w.log.Printf("warning: cannot watch directory %s: %v", path, err)
			}
		}
		return nil
	})
}

func (w *Watcher) addDir(path string) error {
	path = filepath.Clean(path)
	w.mu.Lock()
	_, exists := w.watched[path]
	w.mu.Unlock()
	if exists {
		return nil
	}
	if err := w.fsw.Add(path); err != nil {
		return err
	}
	w.mu.Lock()
	w.watched[path] = struct{}{}
	w.mu.Unlock()
	w.verbosef("watcher added directory: %s", path)
	return nil
}

func (w *Watcher) isExcluded(path string) bool {
	rel, err := filepath.Rel(w.root, filepath.Clean(path))
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return false
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if _, ok := defaultExcludeDirs[part]; ok {
			return true
		}
	}
	for _, ex := range w.excludes {
		if rel == ex || strings.HasPrefix(rel, ex+string(filepath.Separator)) {
			return true
		}
	}
	for _, glob := range w.excludeGlobs {
		if matchesGlob(glob, rel) || matchesGlob(glob, filepath.Base(rel)) {
			return true
		}
	}
	return false
}

func cleanExcludePrefixes(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = filepath.Clean(value)
		if value == "." || filepath.IsAbs(value) {
			continue
		}
		cleaned = append(cleaned, value)
	}
	return cleaned
}

func cleanExcludeGlobs(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = filepath.Clean(value)
		if value == "." || filepath.IsAbs(value) {
			continue
		}
		if _, err := filepath.Match(value, ""); err != nil {
			continue
		}
		cleaned = append(cleaned, value)
	}
	return cleaned
}

func matchesGlob(pattern string, name string) bool {
	matched, err := filepath.Match(pattern, name)
	return err == nil && matched
}

func (w *Watcher) verbosef(format string, args ...any) {
	if w.log != nil {
		w.log.Verbosef(format, args...)
	}
}
