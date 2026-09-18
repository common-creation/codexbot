package runtimemanager

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func collaborationSender(path string) (string, bool) {
	parts := strings.Split(path, "/")
	if len(parts) < 6 || parts[0] != "" || parts[1] != "v1" || parts[2] != "agents" || !validID(parts[3]) || parts[4] != "collaboration" {
		return "", false
	}
	return parts[3], true
}

func (m *Manager) collaborationProxy(w http.ResponseWriter, r *http.Request) {
	if m.cfg.ControlPlaneURL == "" {
		http.Error(w, "collaboration unavailable", 503)
		return
	}
	id := r.PathValue("agentID")
	if !validID(id) {
		http.Error(w, "invalid agent id", 400)
		return
	}
	rest := r.PathValue("rest")
	// Reject path normalization tricks before attaching the privileged token.
	for _, part := range strings.Split(rest, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\%") {
			http.Error(w, "invalid collaboration path", 400)
			return
		}
	}
	target := strings.TrimRight(m.cfg.ControlPlaneURL, "/") + "/internal/agents/" + url.PathEscape(id) + "/collaboration/" + rest
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "invalid collaboration request", 400)
		return
	}
	req.Header.Set("Authorization", "Bearer "+m.cfg.RuntimeToken)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "control plane unavailable", 502)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil {
		http.Error(w, "could not read collaboration response", 502)
		return
	}
	if len(body) > 8<<20 {
		http.Error(w, "collaboration response exceeds 8 MiB limit", 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
