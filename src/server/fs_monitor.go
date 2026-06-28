// fs_monitor.go — watches the music directory (recursively) and triggers
// a debounced DB rescan on any filesystem change.

package main

import (
	"io/fs"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

func filesystemMonitor() {
	conf := c()
	if conf.MusicDir == "" {
		slog.Info("[fsmonitor] CSERVER_MUSIC_DIR not set — filesystem monitor disabled.")
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("[fsmonitor] Failed to create watcher.", "error", err)
		return
	}
	defer watcher.Close()

	// Watch the root and every subdirectory so new folders are caught.
	var watched int
	err = filepath.WalkDir(conf.MusicDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Warn("[fsmonitor] Walk error, skipping.", "path", path, "error", err)
			return nil
		}
		if d.IsDir() {
			if watchErr := watcher.Add(path); watchErr != nil {
				slog.Warn("[fsmonitor] Failed to watch directory.", "path", path, "error", watchErr)
			} else {
				watched++
			}
		}
		return nil
	})
	if err != nil {
		slog.Error("[fsmonitor] Failed to walk music dir.", "dir", conf.MusicDir, "error", err)
		return
	}
	slog.Info("[fsmonitor] Watching music directory.",
		"root", conf.MusicDir,
		"dirs_watched", watched,
		"debounce", conf.FsnotifyDebounce,
	)

	var debounce <-chan time.Time

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				slog.Warn("[fsmonitor] Events channel closed, monitor exiting.")
				return
			}
			slog.Debug("[fsmonitor] Event.", "op", event.Op, "name", event.Name)
			debounce = time.After(c().FsnotifyDebounce)

		case err, ok := <-watcher.Errors:
			if !ok {
				slog.Warn("[fsmonitor] Errors channel closed, monitor exiting.")
				return
			}
			slog.Warn("[fsmonitor] Watcher error.", "error", err)

		case <-debounce:
			debounce = nil
			if !sighupReloading.CompareAndSwap(false, true) {
				slog.Debug("[fsmonitor] Rescan already in progress, skipping.")
				continue
			}
			go func() {
				defer sighupReloading.Store(false)
				slog.Info("[fsmonitor] Change detected, rescanning library.")
				if err := dbPopulate(); err != nil {
					slog.Error("[fsmonitor] Rescan failed.", "error", err)
				}
			}()
		}
	}
}
