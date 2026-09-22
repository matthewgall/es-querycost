package config

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watch watches the config file for changes and calls onChange with the new
// configuration whenever the file is modified. It returns a cancel function that
// stops the watcher. The watcher handles common editor behaviours such as
// atomic renames and truncations by debouncing events.
func Watch(path string, onChange func(Config)) func() {
	var mu sync.Mutex
	cancelled := false
	pending := int32(0)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return func() {}
	}

	load := func() {
		mu.Lock()
		defer mu.Unlock()
		if cancelled {
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		expanded := os.Expand(string(data), func(key string) string {
			if val, ok := os.LookupEnv(key); ok {
				return val
			}
			return ""
		})
		cfg, err := loadFromReader(strings.NewReader(expanded))
		if err != nil {
			return
		}
		onChange(cfg)
	}

	scheduleLoad := func() {
		if atomic.CompareAndSwapInt32(&pending, 0, 1) {
			go func() {
				time.Sleep(200 * time.Millisecond)
				atomic.StoreInt32(&pending, 0)
				load()
			}()
		}
	}

	_ = watcher.Add(path)

	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
					scheduleLoad()
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			}
		}
	}()

	return func() {
		mu.Lock()
		cancelled = true
		mu.Unlock()
		_ = watcher.Close()
	}
}
