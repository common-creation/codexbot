// Package modelcatalog loads the model choices published by Codex.
package modelcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
)

const URL = "https://raw.githubusercontent.com/openai/codex/main/codex-rs/models-manager/models.json"
const maxBytes = 8 << 20

type Model struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Efforts []string `json:"efforts"`
}

// Fetch always bounds the full request, including reading the response body.
// Callers may supply a shorter deadline or a custom transport for testing.
func Fetch(ctx context.Context, client *http.Client, source string) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model catalog HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBytes {
		return nil, fmt.Errorf("model catalog exceeds %d bytes", maxBytes)
	}
	var catalog struct {
		Models []struct {
			Slug            string `json:"slug"`
			DisplayName     string `json:"display_name"`
			Visibility      string `json:"visibility"`
			ReasoningLevels []struct {
				Effort string `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		return nil, fmt.Errorf("decode model catalog: %w", err)
	}
	models := []Model{}
	seen := map[string]bool{}
	for _, entry := range catalog.Models {
		id := strings.TrimSpace(entry.Slug)
		if entry.Visibility != "list" || !validIdentifier(id, 200) || seen[id] {
			continue
		}
		name := strings.TrimSpace(entry.DisplayName)
		if name == "" {
			name = id
		}
		if len(name) > 200 || strings.ContainsFunc(name, unicode.IsControl) {
			continue
		}
		efforts := []string{}
		seenEffort := map[string]bool{}
		for _, level := range entry.ReasoningLevels {
			effort := strings.TrimSpace(level.Effort)
			if !validIdentifier(effort, 32) || seenEffort[effort] {
				continue
			}
			seenEffort[effort] = true
			efforts = append(efforts, effort)
		}
		if len(efforts) == 0 {
			continue
		}
		seen[id] = true
		models = append(models, Model{ID: id, Name: name, Efforts: efforts})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("model catalog contains no usable models")
	}
	return models, nil
}

func validIdentifier(value string, limit int) bool {
	return value != "" && len(value) <= limit && !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
}

// Fallback returns independent slices so callers cannot mutate later defaults.
func Fallback() []Model {
	ids := []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4-mini", "gpt-5.3-codex-spark"}
	models := make([]Model, 0, len(ids))
	for i, id := range ids {
		efforts := []string{"low", "medium", "high", "xhigh"}
		if i <= 3 {
			efforts = append(efforts, "max")
		}
		if i <= 2 {
			efforts = append(efforts, "ultra")
		}
		models = append(models, Model{ID: id, Name: id, Efforts: efforts})
	}
	return models
}
