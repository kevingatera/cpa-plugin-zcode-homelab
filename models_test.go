package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDynamicModelsFromUpstream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("x-api-key") != "test-key" || r.Header.Get("x-zcode-agent") != "glm" {
			http.Error(w, "fingerprint", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"upstream-a"},{"id":"upstream-b"},{"id":"upstream-a"}]}`))
	}))
	defer server.Close()
	cfg := defaultConfig()
	cfg.APIKey = "test-key"
	cfg.BaseURL = server.URL
	activeConfig.Store(cfg)
	resetModelCache()

	ids := modelIDs(context.Background())
	models := modelsFromIDs(normalizedModelIDs(ids))
	if len(models) != 2 || models[0].ID != "upstream-a" || models[1].ID != "upstream-b" {
		t.Fatalf("models = %#v", models)
	}
}

func TestDynamicModelsFallbackOnUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	cfg := defaultConfig()
	cfg.APIKey = "test-key"
	cfg.BaseURL = server.URL
	activeConfig.Store(cfg)
	resetModelCache()

	ids := modelIDs(context.Background())
	if len(ids) == 0 || ids[0] != "GLM-5.3" {
		t.Fatalf("fallback IDs = %#v", ids)
	}
}

func TestDynamicModelsDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"should-not-load"}]}`))
	}))
	defer server.Close()
	cfg := defaultConfig()
	cfg.APIKey = "test-key"
	cfg.BaseURL = server.URL
	disabled := false
	cfg.DynamicModels = &disabled
	cfg.Models = []string{"fallback-model"}
	activeConfig.Store(cfg)
	resetModelCache()

	ids := modelIDs(context.Background())
	if len(ids) != 1 || ids[0] != "fallback-model" {
		t.Fatalf("IDs = %#v", ids)
	}
}
