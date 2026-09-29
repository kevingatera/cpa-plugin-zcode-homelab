package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rensumo/cpa-plugin-zcode/internal/mimic"
)

var modelHTTPClient = &http.Client{Timeout: 15 * time.Second}

var modelCache struct {
	sync.Mutex
	ids       []string
	expiresAt time.Time
}

type upstreamModelList struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func resetModelCache() {
	modelCache.Lock()
	defer modelCache.Unlock()
	modelCache.ids = nil
	modelCache.expiresAt = time.Time{}
}

func dynamicModelsEnabled(cfg pluginConfig) bool {
	return cfg.DynamicModels == nil || *cfg.DynamicModels
}

func modelIDs(ctx context.Context) []string {
	cfg := currentConfig()
	fallback := normalizedModelIDs(cfg.Models)
	if !dynamicModelsEnabled(cfg) || strings.TrimSpace(cfg.APIKey) == "" {
		return fallback
	}

	ttl := time.Duration(cfg.ModelCacheSecs) * time.Second
	if ttl <= 0 {
		ttl = defaultModelCacheSeconds
	}
	now := time.Now()
	modelCache.Lock()
	if now.Before(modelCache.expiresAt) && len(modelCache.ids) > 0 {
		ids := append([]string(nil), modelCache.ids...)
		modelCache.Unlock()
		return ids
	}
	modelCache.Unlock()

	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, cfg.BaseURL+"/v1/models", nil)
	if errRequest != nil {
		return fallback
	}
	fingerprint := mimic.New(cfg.Mimic, keyID(cfg.APIKey))
	fingerprint.ApplyRequest(req.Header, cfg.APIKey, mimic.UUID4())
	resp, errDo := modelHTTPClient.Do(req)
	if errDo != nil {
		return fallback
	}
	body, errRead := readLimited(resp.Body, 1<<20)
	_ = resp.Body.Close()
	if errDo != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 || errRead != nil {
		return fallback
	}

	var upstream upstreamModelList
	if errDecode := json.Unmarshal(body, &upstream); errDecode != nil {
		return fallback
	}
	ids := normalizedModelIDs(make([]string, 0, len(upstream.Data)))
	for _, item := range upstream.Data {
		if id := strings.TrimSpace(item.ID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return fallback
	}

	modelCache.Lock()
	modelCache.ids = append([]string(nil), ids...)
	modelCache.expiresAt = time.Now().Add(ttl)
	modelCache.Unlock()
	return ids
}

func normalizedModelIDs(raw []string) []string {
	ids := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		id := strings.TrimSpace(item)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func upstreamModelError(status int, body []byte) error {
	return fmt.Errorf("upstream models %d: %s", status, truncate(string(body), 1024))
}
