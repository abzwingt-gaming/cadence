// fs_monitor.go — watches the music directory and triggers a DB rescan on changes.

package main

import (
	"log/slog"
	"time"

	"github.com/fsnotify/fsnotify"
)

func filesystemMonitor() {
	conf := c()
	if conf.MusicDir == "" {
		slog.Info("[fsmonitor] CSERVER_MUSIC_DIR not set, filesystem monitor disabled.")
		return
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		slog.Error("[fsmonitor] Failed to create watcher.", "error", err)
		return
	}
	defer watcher.Close()

	if err := watcher.Add(conf.MusicDir); err != nil {
		slog.Error("[fsmonitor] Failed to watch music dir.", "dir", conf.MusicDir, "error", err)
		return
	}
	slog.Info("[fsmonitor] Watching music directory.", "dir", conf.MusicDir)

	var debounce <-chan time.Time

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			slog.Debug("[fsmonitor] FS event.", "op", event.Op, "name", event.Name)
			debounce = time.After(c().FsnotifyDebounce)

		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			slog.Warn("[fsmonitor] Watcher error.", "error", err)

		case <-debounce:
			if sighupReloading.CompareAndSwap(false, true) {
				go func() {
					defer sighupReloading.Store(false)
					slog.Info("[fsmonitor] Change detected, rescanning library.")
					if err := dbPopulate(); err != nil {
						slog.Error("[fsmonitor] Rescan failed.", "error", err)
					}
				}()
			} else {
				slog.Debug("[fsmonitor] Rescan already in progress, skipping.")
			}
			debounce = nil
		}
	}
}
