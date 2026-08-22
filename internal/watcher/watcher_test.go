//go:build linux

package watcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestStartFailsWhenRootCannotBeWatched(t *testing.T) {
	_, err := Start(context.Background(), filepath.Join(t.TempDir(), "missing"), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestExcludesDefaultAndUserPrefixes(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "frontend"))
	w := startTestWatcher(t, root, []string{"frontend/generated"})

	mustMkdir(t, filepath.Join(root, "node_modules"))
	mustWrite(t, filepath.Join(root, "node_modules", "dep.js"), "dep")
	mustMkdir(t, filepath.Join(root, "frontend", "generated"))
	mustWrite(t, filepath.Join(root, "frontend", "generated", "out.txt"), "out")

	assertNoEvent(t, w.Events(), 200*time.Millisecond)
}

func TestExcludeGlobsMatchRelativePathAndBaseName(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "views"))
	mustMkdir(t, filepath.Join(root, "assets"))
	w := startTestWatcherWithGlobs(t, root, nil, []string{"*_templ.go", "assets/*.tmp"})

	mustWrite(t, filepath.Join(root, "views", "home_templ.go"), "templ")
	mustWrite(t, filepath.Join(root, "assets", "cache.tmp"), "tmp")
	assertNoEvent(t, w.Events(), 200*time.Millisecond)

	file := filepath.Join(root, "views", "home.go")
	mustWrite(t, file, "go")
	waitForEvent(t, w.Events(), file, fsnotify.Create|fsnotify.Write)
}

func TestRecursiveWatchingExistingDirectories(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b")
	mustMkdir(t, nested)
	w := startTestWatcher(t, root, nil)

	file := filepath.Join(nested, "file.txt")
	mustWrite(t, file, "hello")

	ev := waitForEvent(t, w.Events(), file, fsnotify.Create|fsnotify.Write)
	if ev.Path != file {
		t.Fatalf("event path = %q, want %q", ev.Path, file)
	}
}

func TestNewDirectoriesAreWatched(t *testing.T) {
	root := t.TempDir()
	w := startTestWatcher(t, root, nil)

	nested := filepath.Join(root, "newdir")
	mustMkdir(t, nested)
	waitForEvent(t, w.Events(), nested, fsnotify.Create)

	file := filepath.Join(nested, "file.txt")
	mustWrite(t, file, "hello")
	waitForEvent(t, w.Events(), file, fsnotify.Create|fsnotify.Write)
}

func TestEventFilteringIgnoresChmodAndSymlinkDirectories(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "target")
	mustMkdir(t, target)
	mustWrite(t, filepath.Join(target, "existing.txt"), "old")
	symlink := filepath.Join(root, "linked")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	w := startTestWatcher(t, root, nil)

	file := filepath.Join(root, "chmod.txt")
	mustWrite(t, file, "hello")
	waitForEvent(t, w.Events(), file, fsnotify.Create|fsnotify.Write)
	drainEvents(w.Events(), 100*time.Millisecond)
	if err := os.Chmod(file, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	assertNoEvent(t, w.Events(), 150*time.Millisecond)

	mustWrite(t, filepath.Join(symlink, "via-link.txt"), "link")
	assertNoEvent(t, w.Events(), 200*time.Millisecond)
}

func startTestWatcher(t *testing.T, root string, excludes []string) *Watcher {
	t.Helper()
	return startTestWatcherWithGlobs(t, root, excludes, nil)
}

func startTestWatcherWithGlobs(t *testing.T, root string, excludes []string, excludeGlobs []string) *Watcher {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w, err := Start(ctx, root, excludes, excludeGlobs, nil)
	if err != nil {
		cancel()
		t.Fatalf("start watcher: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = w.Close()
		<-w.Done()
	})
	return w
}

func waitForEvent(t *testing.T, events <-chan Event, path string, ops fsnotify.Op) Event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("events channel closed")
			}
			if ev.Path == path && ev.Op&ops != 0 {
				return ev
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for event %s with ops %s", path, ops)
		}
	}
}

func assertNoEvent(t *testing.T, events <-chan Event, duration time.Duration) {
	t.Helper()
	select {
	case ev, ok := <-events:
		if ok {
			t.Fatalf("unexpected event: %#v", ev)
		}
		t.Fatal("events channel closed")
	case <-time.After(duration):
	}
}

func drainEvents(events <-chan Event, quietFor time.Duration) {
	for {
		select {
		case <-events:
		case <-time.After(quietFor):
			return
		}
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
