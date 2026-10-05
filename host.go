package main

import (
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"net/http"
)

type hostRequest struct {
	pluginapi.HTTPRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type hostStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers"`
	StreamID   string      `json:"stream_id"`
}

type hostStreamRead struct {
	Payload []byte `json:"payload"`
	Error   string `json:"error"`
	Done    bool   `json:"done"`
}

func upstreamJSON(callback, method, url string, headers http.Header, body any, result any) error {
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	resp, err := callHost[pluginapi.HTTPResponse](pluginabi.MethodHostHTTPDo, hostRequest{
		HTTPRequest: pluginapi.HTTPRequest{Method: method, URL: url, Headers: headers, Body: raw}, HostCallbackID: callback,
	})
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return pluginabi.NewError("zcode_http_error", http.StatusText(resp.StatusCode), resp.StatusCode)
	}
	return json.Unmarshal(resp.Body, result)
}

var hostRPC = nativeHostRPC

func callHost[T any](method string, request any) (T, error) {
	var out T
	raw, err := hostRPC(method, request)
	if err != nil {
		return out, err
	}
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &out)
	}
	return out, err
}
