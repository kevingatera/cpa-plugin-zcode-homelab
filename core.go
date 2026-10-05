package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/rensumo/cpa-plugin-zcode/internal/mimic"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var (
	pluginVersion = "0.1.0"
	activeConfig  atomic.Value
)

const (
	pluginID                 = "zcode"
	defaultBaseURL           = "https://open.bigmodel.cn/api/anthropic"
	defaultAppVersion        = "3.14.4"
	maxAttempts              = 3
	responseLimit            = 64 << 20
	defaultModelCacheSeconds = 300
)

func init() {
	activeConfig.Store(defaultConfig())
}

func main() {}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	QuotaProvider         bool                         `json:"quota_provider"`
	AuthProvider          bool                         `json:"auth_provider"`
	ModelRegistrar        bool                         `json:"model_registrar"`
	ModelProvider         bool                         `json:"model_provider"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type rpcStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

type pluginConfig struct {
	APIKey         string            `yaml:"api_key"`
	BaseURL        string            `yaml:"base_url"`
	ModelMap       map[string]string `yaml:"model_map"`
	Models         []string          `yaml:"models"`
	DynamicModels  *bool             `yaml:"dynamic_models"`
	ModelCacheSecs int               `yaml:"model_cache_seconds"`
	TimeoutSeconds int               `yaml:"timeout_seconds"`
	Mimic          mimic.Config      `yaml:"mimic"`
}

// Config is exported for the vendored mimic package.
type Config = pluginConfig

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodQuotaIdentifier:
		return okEnvelope(identifierResponse{Identifier: pluginID})
	case pluginabi.MethodQuotaDescribe:
		return okEnvelope(pluginapi.QuotaDescribeResponse{SupportedProviders: []string{pluginID}, DisplayName: "ZCode Flash grant"})
	case pluginabi.MethodQuotaFetch:
		return handleQuota(request)
	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: pluginID})
	case pluginabi.MethodAuthParse, pluginabi.MethodAuthLoginStart, pluginabi.MethodAuthLoginPoll, pluginabi.MethodAuthRefresh:
		return handleAuth(method, request)
	case pluginabi.MethodExecutorExecute, pluginabi.MethodExecutorExecuteStream:
		return handleExecute(method, request)
	case pluginabi.MethodModelRegister:
		return okEnvelope(pluginapi.ModelRegistrationResponse{Provider: pluginID, Models: configuredModels()})
	case pluginabi.MethodModelStatic:
		if currentConfig().APIKey == "" {
			return okEnvelope(pluginapi.ModelResponse{Provider: pluginID})
		}
		return okEnvelope(pluginapi.ModelResponse{Provider: pluginID, Models: configuredModels()})
	case pluginabi.MethodModelForAuth:
		return okEnvelope(pluginapi.ModelResponse{Provider: pluginID, Models: modelsFromIDs([]string{"GLM-5.3-Flash"})})
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: pluginID})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, 0), nil
	}
}
func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg := defaultConfig()
	if len(req.ConfigYAML) > 0 {
		var decoded pluginConfig
		if errUnmarshal := yaml.Unmarshal(req.ConfigYAML, &decoded); errUnmarshal != nil {
			return errUnmarshal
		}
		cfg = mergeConfig(cfg, decoded)
	}
	activeConfig.Store(normalizeConfig(cfg))
	return nil
}

func defaultConfig() pluginConfig {
	return pluginConfig{
		BaseURL:        defaultBaseURL,
		Models:         []string{"GLM-5.3", "GLM-5.3-Flash"},
		ModelCacheSecs: defaultModelCacheSeconds,
		Mimic:          mimic.DefaultConfig(),
	}
}

func mergeConfig(base, override pluginConfig) pluginConfig {
	if strings.TrimSpace(override.APIKey) != "" {
		base.APIKey = strings.TrimSpace(override.APIKey)
	}
	if strings.TrimSpace(override.BaseURL) != "" {
		base.BaseURL = override.BaseURL
	}
	if len(override.ModelMap) > 0 {
		base.ModelMap = override.ModelMap
	}
	if len(override.Models) > 0 {
		base.Models = override.Models
	}
	if override.DynamicModels != nil {
		base.DynamicModels = override.DynamicModels
	}
	if override.ModelCacheSecs > 0 {
		base.ModelCacheSecs = override.ModelCacheSecs
	}
	if override.TimeoutSeconds > 0 {
		base.TimeoutSeconds = override.TimeoutSeconds
	}
	if override.Mimic.AppVersion != "" {
		base.Mimic.AppVersion = override.Mimic.AppVersion
	}
	if override.Mimic.SessionID != "" {
		base.Mimic.SessionID = override.Mimic.SessionID
	}
	if override.Mimic.TraceID != "" {
		base.Mimic.TraceID = override.Mimic.TraceID
	}
	if override.Mimic.UserID != "" {
		base.Mimic.UserID = override.Mimic.UserID
	}
	return base
}

func normalizeConfig(cfg pluginConfig) pluginConfig {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 300
	}
	if cfg.ModelCacheSecs <= 0 {
		cfg.ModelCacheSecs = defaultModelCacheSeconds
	}
	if cfg.Mimic.AppVersion == "" {
		cfg.Mimic.AppVersion = defaultAppVersion
	}
	if len(cfg.Models) == 0 {
		cfg.Models = []string{"GLM-5.3", "GLM-5.3-Flash"}
	}
	return cfg
}

func currentConfig() pluginConfig {
	raw := activeConfig.Load()
	if cfg, ok := raw.(pluginConfig); ok {
		return cfg
	}
	return defaultConfig()
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "ZCode",
			Version:          pluginVersion,
			Author:           "rensumo",
			GitHubRepository: "https://github.com/kevingatera/cpa-plugin-zcode-homelab",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "api_key", Type: pluginapi.ConfigFieldTypeString, Description: "Fixed Coding Plan API key for static models. Leave empty for OAuth-only accounts."},
				{Name: "base_url", Type: pluginapi.ConfigFieldTypeString, Description: "BigModel Anthropic-compatible upstream base URL."},
				{Name: "model_map", Type: pluginapi.ConfigFieldTypeObject, Description: "Requested model to upstream model mapping."},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Fallback/static model IDs, used when upstream model discovery is disabled or unavailable."},
				{Name: "dynamic_models", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Fetch model IDs from GET <base_url>/v1/models. Default true."},
				{Name: "model_cache_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Upstream model list cache TTL in seconds. Default 300."},
				{Name: "timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Non-streaming request timeout in seconds."},
				{Name: "mimic", Type: pluginapi.ConfigFieldTypeObject, Description: "ZCode client fingerprint settings."},
			},
		},
		Capabilities: registrationCapabilities{
			AuthProvider:          true,
			QuotaProvider:         true,
			ModelRegistrar:        false,
			ModelProvider:         true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeBoth,
			ExecutorInputFormats:  []string{"anthropic"},
			ExecutorOutputFormats: []string{"anthropic"},
		},
	}
}

func configuredModels() []pluginapi.ModelInfo {
	return modelsFromIDs(modelIDs(context.Background()))
}

func modelsFromIDs(ids []string) []pluginapi.ModelInfo {
	cfg := currentConfig()
	models := make([]pluginapi.ModelInfo, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		models = append(models, pluginapi.ModelInfo{
			ID:                         id,
			Object:                     "model",
			OwnedBy:                    pluginID,
			DisplayName:                id,
			Name:                       mappedModel(id, cfg),
			SupportedGenerationMethods: []string{"chat"},
			ContextLength:              200000,
			MaxCompletionTokens:        128000,
			UserDefined:                true,
		})
	}
	return models
}

func mappedModel(model string, cfg pluginConfig) string {
	if upstream, exists := cfg.ModelMap[model]; exists && strings.TrimSpace(upstream) != "" {
		return strings.TrimSpace(upstream)
	}
	return model
}

func statusError(err error, status int) error {
	if err == nil {
		return nil
	}
	if status < 400 || status > 599 {
		status = http.StatusBadGateway
	}
	return pluginabi.NewError("upstream_error", err.Error(), status)
}

func sleepBackoff(ctx context.Context, attempt int) bool {
	delay := time.Duration(1<<(attempt-1)) * time.Second
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(reader, limit+1)
	data, errRead := io.ReadAll(limited)
	if errRead != nil {
		return nil, errRead
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func keyID(apiKey string) string {
	if index := strings.Index(apiKey, "."); index > 0 {
		return apiKey[:index]
	}
	return apiKey
}

func cloneHeader(source http.Header) http.Header {
	out := make(http.Header, len(source))
	for key, values := range source {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func okEnvelope(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string, status int) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{
		Code:       code,
		Message:    message,
		HTTPStatus: status,
	}})
	return raw
}

func mutateModel(raw []byte, model string) []byte {
	var body map[string]any
	if errUnmarshal := json.Unmarshal(raw, &body); errUnmarshal != nil || body == nil {
		return append([]byte(nil), raw...)
	}
	if model != "" {
		body["model"] = model
	}
	out, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return append([]byte(nil), raw...)
	}
	return out
}
