package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestRegistrationAndModels(t *testing.T) {
	raw, err := handleMethod(pluginabi.MethodPluginRegister, []byte(`{"config_yaml":"bW9kZWxzOgotIHRlc3QtbW9kZWwK"}`))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if !env.OK {
		t.Fatalf("registration failed: %#v", env.Error)
	}

	modelRaw, err := handleMethod(pluginabi.MethodModelStatic, nil)
	if err != nil {
		t.Fatalf("models: %v", err)
	}
	var modelEnv envelope
	if err := json.Unmarshal(modelRaw, &modelEnv); err != nil {
		t.Fatalf("decode models: %v", err)
	}
	var models pluginapi.ModelResponse
	if err := json.Unmarshal(modelEnv.Result, &models); err != nil {
		t.Fatalf("decode model result: %v", err)
	}
	if len(models.Models) != 1 || models.Models[0].ID != "test-model" {
		t.Fatalf("models = %#v", models)
	}
}

func TestModelMappingAndConfig(t *testing.T) {
	_, err := handleMethod(pluginabi.MethodPluginRegister, []byte(`{"config_yaml":"bW9kZWxzOiBbY2xpZW50LW1vZGVsXQptb2RlbF9tYXA6CiAgY2xpZW50LW1vZGVsOiB1cHN0cmVhbS1tb2RlbAphcGlfa2V5OiB0ZXN0LWtleQo="}`))
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	req := pluginapi.ExecutorRequest{Model: "client-model", Headers: map[string][]string{"Content-Type": {"application/json"}}, Payload: []byte(`{"model":"client-model"}`)}
	out := mutateModel(req.Payload, mappedModel(req.Model, currentConfig()))
	var body map[string]any
	if err := json.Unmarshal(out, &body); err != nil {
		t.Fatal(err)
	}
	if body["model"] != "upstream-model" {
		t.Fatalf("mapped model = %#v", body["model"])
	}
	if currentConfig().APIKey != "test-key" {
		t.Fatalf("api key was not configured")
	}
}

func TestAPIKeyFallback(t *testing.T) {
	_, _ = handleMethod(pluginabi.MethodPluginRegister, []byte(`{}`))
	req := pluginapi.ExecutorRequest{Headers: map[string][]string{"Authorization": {"Bearer client-key"}}}
	key, err := requestAPIKey(req, currentConfig())
	if err != nil {
		t.Fatalf("requestAPIKey: %v", err)
	}
	if key != "client-key" {
		t.Fatalf("key = %q", key)
	}
}
