package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/rensumo/cpa-plugin-zcode/internal/mimic"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const zcodeOrigin = "https://zcode.z.ai"

type account struct {
	Type      string `json:"type"`
	Token     string `json:"access_token"`
	DeviceMid string `json:"device_mid"`
	Email     string `json:"email,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
}

type businessEnvelope struct {
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

func sourceHeaders(token, device string) http.Header {
	h := make(http.Header)
	cfg := currentConfig().Mimic
	mimic.New(cfg, "").ApplyRequest(h, "", mimic.UUID4())
	h.Del("x-api-key")
	h.Set("Content-Type", "application/json")
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	if device != "" {
		h.Set("X-Device-Mid", device)
	}
	return h
}

func authRecord(a account, name string) (pluginapi.AuthData, error) {
	if a.Type != pluginID || strings.TrimSpace(a.Token) == "" {
		return pluginapi.AuthData{}, fmt.Errorf("ZCode OAuth token is required")
	}
	if a.DeviceMid == "" {
		return pluginapi.AuthData{}, fmt.Errorf("ZCode device identity is required")
	}
	if a.Prefix == "" {
		a.Prefix = "zcode"
	}
	raw, err := json.Marshal(a)
	return pluginapi.AuthData{Provider: pluginID, FileName: name, Label: a.Email, Prefix: a.Prefix, Disabled: a.Disabled, StorageJSON: raw, Metadata: map[string]any{"type": pluginID, "access_token": a.Token, "device_mid": a.DeviceMid, "email": a.Email, "prefix": a.Prefix}, NextRefreshAfter: time.Now().Add(24 * time.Hour)}, err
}

func handleAuth(method string, raw []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodAuthParse:
		var r pluginapi.AuthParseRequest
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		var a account
		if json.Unmarshal(r.RawJSON, &a) != nil || a.Type != pluginID {
			return okEnvelope(pluginapi.AuthParseResponse{})
		}
		data, err := authRecord(a, r.FileName)
		if err != nil {
			return nil, err
		}
		return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: data})
	case pluginabi.MethodAuthLoginStart:
		var r struct {
			pluginapi.AuthLoginStartRequest
			HostCallbackID string `json:"host_callback_id"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		pollToken := hex.EncodeToString(b)
		device := mimic.UUID4()
		var env businessEnvelope
		err := upstreamJSON(r.HostCallbackID, "POST", zcodeOrigin+"/api/v1/oauth/cli/init", sourceHeaders(pollToken, device), map[string]string{"provider": "zai"}, &env)
		if err != nil {
			return nil, err
		}
		if env.Code != 0 {
			return nil, fmt.Errorf("ZCode OAuth initialization failed (%d)", env.Code)
		}
		var d struct {
			Flow    string `json:"flow_id"`
			Poll    string `json:"poll_token"`
			URL     string `json:"authorize_url"`
			Expires int64  `json:"expires_at"`
		}
		if err = json.Unmarshal(env.Data, &d); err != nil {
			return nil, err
		}
		u, err := url.Parse(d.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() != "chat.z.ai" || d.Flow == "" || d.Poll == "" || d.Expires <= time.Now().Unix() {
			return nil, fmt.Errorf("invalid ZCode OAuth initialization response")
		}
		return okEnvelope(pluginapi.AuthLoginStartResponse{Provider: pluginID, URL: d.URL, State: d.Flow, ExpiresAt: time.Unix(d.Expires, 0), Metadata: map[string]any{"poll_token": d.Poll, "device_mid": device}})
	case pluginabi.MethodAuthLoginPoll:
		var r struct {
			pluginapi.AuthLoginPollRequest
			HostCallbackID string `json:"host_callback_id"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		token, _ := r.Metadata["poll_token"].(string)
		device, _ := r.Metadata["device_mid"].(string)
		if token == "" || device == "" {
			return nil, fmt.Errorf("missing ZCode OAuth polling context")
		}
		var env businessEnvelope
		err := upstreamJSON(r.HostCallbackID, "GET", zcodeOrigin+"/api/v1/oauth/cli/poll/"+url.PathEscape(r.State), sourceHeaders(token, device), nil, &env)
		if err != nil {
			return nil, err
		}
		if env.Code != 0 {
			return nil, fmt.Errorf("ZCode OAuth polling failed (%d)", env.Code)
		}
		var d struct {
			Status string `json:"status"`
			Token  string `json:"token"`
			User   struct {
				Email string `json:"email"`
			} `json:"user"`
		}
		if err = json.Unmarshal(env.Data, &d); err != nil {
			return nil, err
		}
		if d.Status == "pending" {
			return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusPending})
		}
		if d.Status != "ready" {
			return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "ZCode authorization failed"})
		}
		data, err := authRecord(account{Type: pluginID, Token: d.Token, DeviceMid: device, Email: d.User.Email}, "")
		if err != nil {
			return nil, err
		}
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusSuccess, Auth: data})
	case pluginabi.MethodAuthRefresh:
		var r pluginapi.AuthRefreshRequest
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		var a account
		if err := json.Unmarshal(r.StorageJSON, &a); err != nil {
			return nil, err
		}
		// The native login provides a persistent ZCode JWT, with no refresh token or expiry.
		// Preserve it; server revocation requires another browser authorization.
		data, err := authRecord(a, "")
		if err != nil {
			return nil, err
		}
		data.ID = r.AuthID
		return okEnvelope(pluginapi.AuthRefreshResponse{Auth: data, NextRefreshAfter: data.NextRefreshAfter})
	}
	return nil, fmt.Errorf("unknown auth method")
}
