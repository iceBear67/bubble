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
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ---------- CLI flags ----------

var (
	flagOrg      = flag.String("org", "", "Gitea organization name (required)")
	flagTeam     = flag.String("team", "", "Gitea team name (required)")
	flagAddr     = flag.String("addr", ":2334", "listen address")
	flagGiteaURL = flag.String("gitea-url", "", "Gitea base URL, e.g. https://gitea.example.com (required)")
	flagCacheTTL = flag.Duration("cache-ttl", 30*time.Second, "cache TTL for team membership and SSH keys")
)

func main() {
	flag.Parse()

	if *flagOrg == "" {
		log.Fatal("-org is required")
	}
	if *flagTeam == "" {
		log.Fatal("-team is required")
	}
	if *flagGiteaURL == "" {
		log.Fatal("-gitea-url is required")
	}

	token := os.Getenv("GITEA_TOKEN")
	if token == "" {
		log.Fatal("GITEA_TOKEN environment variable is required")
	}

	giteaURL := strings.TrimRight(*flagGiteaURL, "/")

	client := &giteaClient{
		baseURL:  giteaURL,
		token:    token,
		http:     &http.Client{Timeout: 10 * time.Second},
		cacheTTL: *flagCacheTTL,
	}

	// Resolve team ID once at startup (and periodically refresh).
	teamID, err := client.resolveTeamID(*flagOrg, *flagTeam)
	if err != nil {
		log.Fatalf("failed to resolve team %q in org %q: %v", *flagTeam, *flagOrg, err)
	}
	log.Printf("resolved team %q in org %q → team ID %d", *flagTeam, *flagOrg, teamID)

	client.mu.Lock()
	client.cachedTeamID = teamID
	client.mu.Unlock()

	// Background goroutine to refresh team ID periodically.
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			id, err := client.resolveTeamID(*flagOrg, *flagTeam)
			if err != nil {
				log.Printf("warning: failed to refresh team ID: %v", err)
			} else {
				client.mu.Lock()
				client.cachedTeamID = id
				client.mu.Unlock()
				log.Printf("refreshed team ID: %d", id)
			}
		}
	}()

	http.HandleFunc("/", client.handleAuth)

	log.Printf("gitea-auth server listening on %s, org=%s team=%s gitea=%s",
		*flagAddr, *flagOrg, *flagTeam, giteaURL)
	if err := http.ListenAndServe(*flagAddr, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

// ---------- Gitea API client ----------

type cacheEntry struct {
	data      any
	expiresAt time.Time
}

type giteaClient struct {
	baseURL  string
	token    string
	http     *http.Client
	cacheTTL time.Duration

	mu           sync.Mutex
	cachedTeamID int64
	cache        map[string]*cacheEntry // key → cached result
}

// resolveTeamID looks up the numeric team ID by org + team name.
func (c *giteaClient) resolveTeamID(org, team string) (int64, error) {
	// GET /api/v1/orgs/{org}/teams/search?q={name}
	endpoint := fmt.Sprintf("/api/v1/orgs/%s/teams/search?q=%s", url.PathEscape(org), url.QueryEscape(team))

	var result struct {
		Data []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := c.doAPI("GET", endpoint, nil, &result); err != nil {
		return 0, fmt.Errorf("search teams: %w", err)
	}

	for _, t := range result.Data {
		if strings.EqualFold(t.Name, team) {
			return t.ID, nil
		}
	}

	return 0, fmt.Errorf("team %q not found in org %q", team, org)
}

// listTeamMembers returns all usernames in the configured team.
func (c *giteaClient) listTeamMembers() ([]string, error) {
	cacheKey := "team-members"

	c.mu.Lock()
	if entry, ok := c.cache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.data.([]string), nil
	}
	c.mu.Unlock()

	c.mu.Lock()
	teamID := c.cachedTeamID
	c.mu.Unlock()

	// Paginate through /api/v1/teams/{id}/members
	var members []string
	page := 1
	for {
		endpoint := fmt.Sprintf("/api/v1/teams/%d/members?page=%d&limit=50", teamID, page)

		var pageMembers []struct {
			Login string `json:"login"`
		}
		if err := c.doAPI("GET", endpoint, nil, &pageMembers); err != nil {
			return nil, fmt.Errorf("list team members: %w", err)
		}

		if len(pageMembers) == 0 {
			break
		}

		for _, m := range pageMembers {
			members = append(members, m.Login)
		}
		page++
	}

	c.mu.Lock()
	if c.cache == nil {
		c.cache = make(map[string]*cacheEntry)
	}
	c.cache[cacheKey] = &cacheEntry{
		data:      members,
		expiresAt: time.Now().Add(c.cacheTTL),
	}
	c.mu.Unlock()

	return members, nil
}

// getUserKeys fetches the SSH public keys for a Gitea user.
func (c *giteaClient) getUserKeys(username string) ([]ssh.PublicKey, error) {
	cacheKey := "keys:" + username

	c.mu.Lock()
	if entry, ok := c.cache[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		c.mu.Unlock()
		return entry.data.([]ssh.PublicKey), nil
	}
	c.mu.Unlock()

	// GET /api/v1/users/{username}/keys
	endpoint := fmt.Sprintf("/api/v1/users/%s/keys", url.PathEscape(username))

	var keys []struct {
		Key string `json:"key"`
	}
	if err := c.doAPI("GET", endpoint, nil, &keys); err != nil {
		return nil, fmt.Errorf("fetch user keys: %w", err)
	}

	var parsed []ssh.PublicKey
	for _, k := range keys {
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(k.Key))
		if err != nil {
			log.Printf("warning: failed to parse key for user %s: %v", username, err)
			continue
		}
		parsed = append(parsed, pk)
	}

	c.mu.Lock()
	if c.cache == nil {
		c.cache = make(map[string]*cacheEntry)
	}
	c.cache[cacheKey] = &cacheEntry{
		data:      parsed,
		expiresAt: time.Now().Add(c.cacheTTL),
	}
	c.mu.Unlock()

	return parsed, nil
}

// doAPI performs an HTTP request to the Gitea API and unmarshals the JSON response.
func (c *giteaClient) doAPI(method, endpoint string, body io.Reader, result any) error {
	req, err := http.NewRequest(method, c.baseURL+endpoint, body)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "token "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("API returned %d: %s", resp.StatusCode, string(respBody))
	}

	if result != nil {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("unmarshal response: %w (body: %s)", err, string(respBody))
		}
	}

	return nil
}

// ---------- HTTP handler ----------

func (c *giteaClient) handleAuth(w http.ResponseWriter, r *http.Request) {
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

	if req.Key == "" {
		http.Error(w, "key is required", http.StatusBadRequest)
		return
	}

	// Decode the base64-encoded SSH public key wire format.
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

	// Find the team member who owns this key (does not trust req.User).
	username, err := c.findKeyOwner(incomingKey)
	if err != nil {
		log.Printf("key lookup failed: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if username == "" {
		log.Printf("auth failed: key=%s... (no team member owns this key)",
			req.Key[:min(16, len(req.Key))])
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	resp := map[string]string{"user": username}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
	log.Printf("auth ok: user=%s key=%s...", username, req.Key[:min(16, len(req.Key))])
}

// findKeyOwner queries the team member list then checks each member's Gitea
// SSH keys for a match.  Returns the username on success, or "" if no match.
func (c *giteaClient) findKeyOwner(incomingKey ssh.PublicKey) (string, error) {
	members, err := c.listTeamMembers()
	if err != nil {
		return "", fmt.Errorf("list team members: %w", err)
	}

	for _, username := range members {
		keys, err := c.getUserKeys(username)
		if err != nil {
			log.Printf("warning: failed to fetch keys for %s: %v", username, err)
			continue
		}
		for _, k := range keys {
			if bytes.Equal(k.Marshal(), incomingKey.Marshal()) {
				return username, nil
			}
		}
	}

	return "", nil
}
