package config

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watch calls onChange after any of the given files changes on disk.
// It watches the parent folders, because many editors replace a file with a rename.
func Watch(ctx context.Context, files []string, onChange func()) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	targets := map[string]bool{}
	dirs := map[string]bool{}
	for _, f := range files {
		if f == "" {
			continue
		}
		abs, err := filepath.Abs(f)
		if err != nil {
			w.Close()
			return err
		}
		targets[abs] = true
		dirs[filepath.Dir(abs)] = true
	}
	for d := range dirs {
		if err := w.Add(d); err != nil {
			w.Close()
			return err
		}
	}
	go func() {
		defer w.Close()
		// Editors often write a file in several steps, so wait for the events to stop.
		const debounce = 200 * time.Millisecond
		var timer <-chan time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if targets[filepath.Clean(ev.Name)] && ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
					timer = time.After(debounce)
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				slog.Warn("config watcher error", "err", err)
			case <-timer:
				timer = nil
				onChange()
			}
		}
	}()
	return nil
}
