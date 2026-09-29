package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return ((int (*)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*))stored_host->call)(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		((void (*)(void*, size_t))stored_host->free_buffer)(ptr, len);
	}
}

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/rensumo/cpa-plugin-zcode/internal/mimic"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pluginID          = "zcode"
	defaultBaseURL    = "https://open.bigmodel.cn/api/anthropic"
	defaultAppVersion = "3.14.4"
	maxAttempts       = 3
	responseLimit     = 64 << 20
)

var (
	pluginVersion = "0.1.0"
	activeConfig  atomic.Value
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
	TimeoutSeconds int               `yaml:"timeout_seconds"`
	Mimic          mimic.Config      `yaml:"mimic"`
}

// Config is exported for the vendored mimic package.
type Config = pluginConfig

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required", 0))
		return 1
	}

	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}

	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		var pluginErr *pluginabi.Error
		status := 0
		if errors.As(errHandle, &pluginErr) {
			status = pluginErr.HTTPStatus
		}
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error(), status))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodModelRegister:
		return okEnvelope(pluginapi.ModelRegistrationResponse{Provider: pluginID, Models: configuredModels()})
	case pluginabi.MethodModelStatic, pluginabi.MethodModelForAuth:
		return okEnvelope(pluginapi.ModelResponse{Provider: pluginID, Models: configuredModels()})
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: pluginID})
	case pluginabi.MethodExecutorExecute:
		return execute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return executeStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return countTokens(request)
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
		BaseURL: defaultBaseURL,
		Models:  []string{"GLM-5.3", "GLM-5.3-Flash"},
		Mimic:   mimic.DefaultConfig(),
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
			GitHubRepository: "https://github.com/rensumo/cpa-plugin-zcode",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "api_key", Type: pluginapi.ConfigFieldTypeString, Description: "Fixed BigModel API key. Leave empty to pass each CPA client key through to upstream."},
				{Name: "base_url", Type: pluginapi.ConfigFieldTypeString, Description: "BigModel Anthropic-compatible upstream base URL."},
				{Name: "model_map", Type: pluginapi.ConfigFieldTypeObject, Description: "Requested model to upstream model mapping."},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Models exposed by this plugin."},
				{Name: "timeout_seconds", Type: pluginapi.ConfigFieldTypeInteger, Description: "Non-streaming request timeout in seconds."},
				{Name: "mimic", Type: pluginapi.ConfigFieldTypeObject, Description: "ZCode client fingerprint settings."},
			},
		},
		Capabilities: registrationCapabilities{
			ModelRegistrar:        true,
			ModelProvider:         true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeStatic,
			ExecutorInputFormats:  []string{"anthropic"},
			ExecutorOutputFormats: []string{"anthropic"},
		},
	}
}

func configuredModels() []pluginapi.ModelInfo {
	cfg := currentConfig()
	models := make([]pluginapi.ModelInfo, 0, len(cfg.Models))
	for _, id := range cfg.Models {
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

func execute(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, pluginabi.NewError("invalid_request", errUnmarshal.Error(), http.StatusBadRequest)
	}
	body, status, errSend := sendUpstream(context.Background(), req.ExecutorRequest, nil)
	if errSend != nil {
		return nil, statusError(errSend, status)
	}
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: body,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	})
}

func executeStream(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, pluginabi.NewError("invalid_request", errUnmarshal.Error(), http.StatusBadRequest)
	}
	if strings.TrimSpace(req.StreamID) == "" {
		return nil, pluginabi.NewError("invalid_stream", "plugin stream id is required", http.StatusInternalServerError)
	}

	headers, chunks, errOpen := openUpstreamStream(context.Background(), req.ExecutorRequest)
	if errOpen != nil {
		return nil, errOpen
	}
	go forwardStream(req.StreamID, headers, chunks)
	return okEnvelope(struct {
		Headers http.Header `json:"headers,omitempty"`
	}{Headers: headers})
}

func countTokens(_ []byte) ([]byte, error) {
	return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"total_tokens":0}`)})
}

func requestAPIKey(req pluginapi.ExecutorRequest, cfg pluginConfig) (string, error) {
	if cfg.APIKey != "" {
		return cfg.APIKey, nil
	}
	authorization := req.Headers.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(authorization), "bearer ") {
		if key := strings.TrimSpace(authorization[7:]); key != "" {
			return key, nil
		}
	}
	if key := strings.TrimSpace(req.Headers.Get("x-api-key")); key != "" {
		return key, nil
	}
	return "", pluginabi.NewError("missing_api_key", "BigModel API key is required", http.StatusUnauthorized)
}

func mutateModel(raw []byte, model string) []byte {
	var body map[string]any
	if errUnmarshal := json.Unmarshal(raw, &body); errUnmarshal != nil || body == nil {
		return bytes.Clone(raw)
	}
	if model != "" {
		body["model"] = model
	}
	out, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return bytes.Clone(raw)
	}
	return out
}

func prepareUpstreamRequest(ctx context.Context, req pluginapi.ExecutorRequest) (*http.Request, pluginConfig, string, error) {
	cfg := currentConfig()
	apiKey, errKey := requestAPIKey(req, cfg)
	if errKey != nil {
		return nil, pluginConfig{}, "", errKey
	}
	model := mappedModel(req.Model, cfg)
	payload := mutateModel(req.Payload, model)
	upstream, errNew := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+"/v1/messages", bytes.NewReader(payload))
	if errNew != nil {
		return nil, pluginConfig{}, "", pluginabi.NewError("invalid_upstream", errNew.Error(), http.StatusBadGateway)
	}
	fingerprint := mimic.New(cfg.Mimic, keyID(apiKey))
	fingerprint.ApplyRequest(upstream.Header, apiKey, mimic.UUID4())
	return upstream, cfg, apiKey, nil
}

func sendUpstream(ctx context.Context, req pluginapi.ExecutorRequest, client *http.Client) ([]byte, int, error) {
	if client == nil {
		client = &http.Client{Timeout: time.Duration(currentConfig().TimeoutSeconds) * time.Second}
	}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		upstream, _, _, errPrepare := prepareUpstreamRequest(ctx, req)
		if errPrepare != nil {
			return nil, 0, errPrepare
		}
		resp, errDo := client.Do(upstream)
		if errDo != nil {
			lastErr = errDo
			if attempt < maxAttempts {
				if !sleepBackoff(ctx, attempt) {
					break
				}
			}
			continue
		}
		body, errRead := readLimited(resp.Body, responseLimit)
		_ = resp.Body.Close()
		if errRead != nil {
			lastErr = errRead
			continue
		}
		if resp.StatusCode >= 500 && attempt < maxAttempts {
			lastErr = fmt.Errorf("upstream %d: %s", resp.StatusCode, truncate(string(body), 512))
			if !sleepBackoff(ctx, attempt) {
				break
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, resp.StatusCode, fmt.Errorf("upstream %d: %s", resp.StatusCode, truncate(string(body), 1024))
		}
		return body, resp.StatusCode, nil
	}
	if lastErr == nil {
		lastErr = errors.New("upstream request failed")
	}
	return nil, http.StatusBadGateway, lastErr
}

func openUpstreamStream(ctx context.Context, req pluginapi.ExecutorRequest) (http.Header, <-chan streamChunk, error) {
	client := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}}
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		upstream, _, _, errPrepare := prepareUpstreamRequest(ctx, req)
		if errPrepare != nil {
			return nil, nil, errPrepare
		}
		resp, errDo := client.Do(upstream)
		if errDo != nil {
			lastErr = errDo
			if attempt < maxAttempts && sleepBackoff(ctx, attempt) {
				continue
			}
			return nil, nil, statusError(lastErr, http.StatusBadGateway)
		}
		if resp.StatusCode >= 500 && attempt < maxAttempts {
			body, _ := readLimited(resp.Body, responseLimit)
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("upstream %d: %s", resp.StatusCode, truncate(string(body), 512))
			if sleepBackoff(ctx, attempt) {
				continue
			}
			return nil, nil, statusError(lastErr, http.StatusBadGateway)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := readLimited(resp.Body, responseLimit)
			_ = resp.Body.Close()
			status := resp.StatusCode
			return nil, nil, pluginabi.NewError("upstream_error", fmt.Sprintf("upstream %d: %s", status, truncate(string(body), 1024)), status)
		}

		chunks := make(chan streamChunk)
		go copyStream(resp.Body, chunks)
		return cloneHeader(resp.Header), chunks, nil
	}
	if lastErr == nil {
		lastErr = errors.New("upstream stream failed")
	}
	return nil, nil, statusError(lastErr, http.StatusBadGateway)
}

type streamChunk struct {
	payload []byte
	err     error
}

func copyStream(body io.ReadCloser, out chan<- streamChunk) {
	defer func() {
		_ = body.Close()
		close(out)
	}()
	buffer := make([]byte, 32*1024)
	for {
		n, errRead := body.Read(buffer)
		if n > 0 {
			out <- streamChunk{payload: append([]byte(nil), buffer[:n]...)}
		}
		if errRead != nil {
			if !errors.Is(errRead, io.EOF) && errRead != nil {
				out <- streamChunk{err: errRead}
			}
			return
		}
	}
}

func forwardStream(streamID string, headers http.Header, chunks <-chan streamChunk) {
	contentType := headers.Get("Content-Type")
	if contentType == "" {
		contentType = "text/event-stream; charset=utf-8"
	}
	for chunk := range chunks {
		if chunk.err != nil {
			closePluginStream(streamID, chunk.err.Error())
			return
		}
		if len(chunk.payload) > 0 {
			if errEmit := emitPluginStreamChunk(streamID, chunk.payload); errEmit != nil {
				return
			}
		}
	}
	closePluginStream(streamID, "")
}

func emitPluginStreamChunk(streamID string, payload []byte) error {
	raw, errCall := callHost(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{
		StreamID: streamID,
		Payload:  payload,
	})
	_ = raw
	return errCall
}

func closePluginStream(streamID, message string) {
	_, _ = callHost(pluginabi.MethodHostStreamClose, rpcStreamCloseRequest{
		StreamID: streamID,
		Error:    strings.TrimSpace(message),
	})
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host envelope %s: %w", method, errUnmarshal)
	}
	if !env.OK || env.Error != nil {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	return append(json.RawMessage(nil), env.Result...), nil
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

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
