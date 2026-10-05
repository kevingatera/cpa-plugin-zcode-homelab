package main

import (
	"encoding/json"
	"fmt"
	"github.com/rensumo/cpa-plugin-zcode/internal/mimic"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"net/http"
	"strings"
)

func executionRequest(r rpcExecutorRequest) (hostRequest, error) {
	cfg := currentConfig()
	var a account
	_ = json.Unmarshal(r.StorageJSON, &a)
	base := strings.TrimRight(cfg.BaseURL, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	var h http.Header
	if a.Type == pluginID {
		if _, err := authRecord(a, ""); err != nil {
			return hostRequest{}, err
		}
		base = zcodeOrigin + "/api/v1/zcode-plan/anthropic/v1"
		h = sourceHeaders(a.Token, a.DeviceMid)
		// Start Plan Anthropic uses the same bearer JWT as billing/balance.
	} else {
		if cfg.APIKey == "" {
			return hostRequest{}, pluginabi.NewError("missing_api_key", "configure a Coding Plan API key or sign in with ZCode OAuth", 401)
		}
		h = make(http.Header)
		mimic.New(cfg.Mimic, keyID(cfg.APIKey)).ApplyRequest(h, cfg.APIKey, mimic.UUID4())
	}
	payload := mutateModel(r.Payload, mappedModel(r.Model, cfg))
	var obj map[string]any
	if err := json.Unmarshal(payload, &obj); err != nil {
		return hostRequest{}, err
	}
	obj["stream"] = r.Stream
	payload, err := json.Marshal(obj)
	if err != nil {
		return hostRequest{}, err
	}
	return hostRequest{HTTPRequest: pluginapi.HTTPRequest{Method: "POST", URL: strings.TrimRight(base, "/") + "/messages", Headers: h, Body: payload}, HostCallbackID: r.HostCallbackID}, nil
}

func handleExecute(method string, raw []byte) ([]byte, error) {
	var r rpcExecutorRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	r.Stream = method == pluginabi.MethodExecutorExecuteStream
	req, err := executionRequest(r)
	if err != nil {
		return nil, err
	}
	if !r.Stream {
		resp, err := callHost[pluginapi.HTTPResponse](pluginabi.MethodHostHTTPDo, req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, upstreamFailure(resp.StatusCode, resp.Body)
		}
		return okEnvelope(pluginapi.ExecutorResponse{Payload: resp.Body, Headers: resp.Headers})
	}
	if r.StreamID == "" {
		return nil, fmt.Errorf("host stream ID is required")
	}
	resp, err := callHost[hostStreamResponse](pluginabi.MethodHostHTTPDoStream, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := readStreamError(resp.StreamID)
		if readErr != nil {
			return nil, readErr
		}
		return nil, upstreamFailure(resp.StatusCode, body)
	}
	if resp.StreamID == "" {
		return nil, fmt.Errorf("upstream stream ID is missing")
	}
	go forwardStream(resp.StreamID, r.StreamID)
	return okEnvelope(struct {
		Headers http.Header `json:"headers"`
	}{resp.Headers})
}

func upstreamFailure(status int, body []byte) error {
	// Include provider error type, never raw bodies that may contain credentials.
	var obj struct {
		Code  int `json:"code"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &obj)
	if obj.Code == 3012 {
		return pluginabi.NewError("zcode_activity_block", "ZCode blocked this request due to unusual activity; complete the provider verification in the native ZCode app", status)
	}
	return pluginabi.NewError("zcode_upstream_error", fmt.Sprintf("ZCode upstream HTTP %d (%s)", status, obj.Error.Type), status)
}

func readStreamError(id string) ([]byte, error) {
	defer closeUpstreamStream(id)
	var body []byte
	for len(body) < 65536 {
		c, err := callHost[hostStreamRead](pluginabi.MethodHostHTTPStreamRead, map[string]string{"stream_id": id})
		if err != nil {
			return nil, err
		}
		body = append(body, c.Payload...)
		if c.Error != "" {
			return nil, fmt.Errorf("upstream stream failed")
		}
		if c.Done {
			return body, nil
		}
	}
	return body, nil
}

func closeUpstreamStream(id string) {
	_, _ = callHost[struct{}](pluginabi.MethodHostHTTPStreamClose, map[string]string{"stream_id": id})
}

func forwardStream(upstream, downstream string) {
	message := ""
	defer func() {
		closeUpstreamStream(upstream)
		_, _ = callHost[struct{}](pluginabi.MethodHostStreamClose, rpcStreamCloseRequest{StreamID: downstream, Error: message})
	}()
	for {
		c, err := callHost[hostStreamRead](pluginabi.MethodHostHTTPStreamRead, map[string]string{"stream_id": upstream})
		if err != nil || c.Error != "" {
			message = "ZCode upstream stream failed"
			return
		}
		if len(c.Payload) > 0 {
			if _, err = callHost[struct{}](pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{StreamID: downstream, Payload: c.Payload}); err != nil {
				message = "ZCode downstream stream closed"
				return
			}
		}
		if c.Done {
			return
		}
	}
}
