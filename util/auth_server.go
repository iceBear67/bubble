package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"golang.org/x/crypto/ssh"
)

func main() {
	root := flag.String("root", "", "root directory containing user public key files")
	addr := flag.String("addr", ":2334", "listen address")
	flag.Parse()
	if *root == "" {
		log.Fatal("root directory is required")
	}

	users, err := loadUsers(*root)
	if err != nil {
		log.Fatalf("failed to load users: %v", err)
	}
	log.Printf("loaded %d users from %s", len(users), *root)

	// start file watcher for hot-reload
	mu := &sync.RWMutex{}
	startWatcher(*root, users, mu)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var req struct {
			Key  string `json:"key"`
			User string `json:"user"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid JSON", http.StatusBadRequest)
			return
		}

		// decode the base64-encoded public key wire format
		keyBytes, err := base64.StdEncoding.DecodeString(req.Key)
		if err != nil {
			http.Error(w, "invalid key encoding", http.StatusBadRequest)
			return
		}

		incomingKey, err := ssh.ParsePublicKey(keyBytes)
		if err != nil {
			http.Error(w, "invalid public key", http.StatusBadRequest)
			return
		}

		mu.RLock()
		user, ok := matchKey(users, incomingKey)
		mu.RUnlock()

		if ok {
			resp := map[string]string{"user": user}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			log.Printf("auth ok: user=%s key=%s...", user, req.Key[:16])
			return
		}

		log.Printf("auth failed: user=%s key=%s...", req.User, req.Key[:16])
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})

	log.Printf("auth server listening on %s, root=%s", *addr, *root)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// matchKey iterates the users map looking for a matching public key.
func matchKey(users map[string][]ssh.PublicKey, incomingKey ssh.PublicKey) (string, bool) {
	for name, allowedKeys := range users {
		for _, allowedKey := range allowedKeys {
			if bytes.Equal(allowedKey.Marshal(), incomingKey.Marshal()) {
				return name, true
			}
		}
	}
	return "", false
}

// startWatcher monitors rootDir for file changes and hot-reloads the users map.
func startWatcher(rootDir string, users map[string][]ssh.PublicKey, mu *sync.RWMutex) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("warning: failed to create file watcher (hot-reload disabled): %v", err)
		return
	}

	if err := watcher.Add(rootDir); err != nil {
		log.Printf("warning: failed to watch root directory (hot-reload disabled): %v", err)
		watcher.Close()
		return
	}

	// debounce timer — coalesce bursts of writes (e.g. scp/editor saves)
	var debounce *time.Timer

	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				// only care about writes/creates/deletes/renames of regular files
				if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) != 0 {
					// skip directories
					if fi, err := os.Stat(event.Name); err == nil && fi.IsDir() {
						continue
					}

					if debounce != nil {
						debounce.Stop()
					}
					debounce = time.AfterFunc(500*time.Millisecond, func() {
						mu.Lock()
						fresh, err := loadUsers(rootDir)
						if err != nil {
							log.Printf("warning: failed to reload users: %v", err)
						} else {
							// swap in-place
							for k := range users {
								delete(users, k)
							}
							for k, v := range fresh {
								users[k] = v
							}
							log.Printf("hot-reload: users updated (%d total)", len(users))
						}
						mu.Unlock()
					})
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("watcher error: %v", err)
			}
		}
	}()

	log.Println("file watcher started for hot-reload")
}

// loadUsers scans rootDir for files (one per user), where each file
// contains one SSH public key per line. The filename becomes the identity name.
func loadUsers(rootDir string) (map[string][]ssh.PublicKey, error) {
	users := make(map[string][]ssh.PublicKey)

	entries, err := os.ReadDir(rootDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read root directory %s: %w", rootDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(rootDir, name)

		if !strings.HasSuffix(name, ".key") {
			continue
		}
		name = strings.TrimSuffix(name, ".key")

		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("warning: failed to read %s: %v", path, err)
			continue
		}

		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		var keys []ssh.PublicKey
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parsedKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
			if err != nil {
				log.Printf("warning: failed to parse key in user file %s: %v", name, err)
				continue
			}
			keys = append(keys, parsedKey)
		}

		if len(keys) > 0 {
			users[name] = keys
			log.Printf("loaded user %s with %d key(s)", name, len(keys))
		} else {
			log.Printf("warning: user file %s has no valid keys, skipped", name)
		}
	}

	return users, nil
}
