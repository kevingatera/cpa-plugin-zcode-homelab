package main

import (
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"strings"
	"testing"
	"time"
)

func TestOAuthAccountExecutionDoesNotForwardConsumerKey(t *testing.T) {
	activeConfig.Store(defaultConfig())
	a := account{Type: pluginID, Token: "account-jwt", DeviceMid: "device-identity"}
	raw, _ := json.Marshal(a)
	req, err := executionRequest(rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{StorageJSON: raw, Model: "GLM-5.3-Flash", Stream: true, Headers: map[string][]string{"Authorization": {"Bearer consumer-secret"}}, Payload: []byte(`{"model":"other","messages":[{"role":"user","content":"hello"}]}`)}, HostCallbackID: "scope"})
	if err != nil {
		t.Fatal(err)
	}
	if req.URL != zcodeOrigin+"/api/v1/zcode-plan/anthropic/v1/messages" || req.Headers.Get("Authorization") != "Bearer account-jwt" || req.Headers.Get("x-api-key") != "" || req.Headers.Get("X-Device-Mid") != "device-identity" || req.HostCallbackID != "scope" {
		t.Fatalf("incorrect account request")
	}
	if strings.Contains(string(req.Body), "consumer-secret") {
		t.Fatal("consumer secret forwarded")
	}
	var body map[string]any
	_ = json.Unmarshal(req.Body, &body)
	if body["model"] != "GLM-5.3-Flash" || body["stream"] != true {
		t.Fatal("incorrect model/stream")
	}
}

func TestOAuthAccountValidationAndIsolation(t *testing.T) {
	for _, a := range []account{{Type: "other", Token: "jwt", DeviceMid: "device"}, {Type: pluginID, DeviceMid: "device"}, {Type: pluginID, Token: "jwt"}} {
		if _, err := authRecord(a, ""); err == nil {
			t.Fatal("invalid account accepted")
		}
	}
	a, err := authRecord(account{Type: pluginID, Token: "jwt", DeviceMid: "device"}, "account.json")
	if err != nil || a.Prefix != "zcode" || a.Provider != pluginID || a.FileName != "account.json" {
		t.Fatal("account routing is not isolated")
	}
}

func TestOAuthNativePollingFlow(t *testing.T) {
	activeConfig.Store(defaultConfig())
	old := hostRPC
	t.Cleanup(func() { hostRPC = old })
	var initToken string
	hostRPC = func(method string, request any) (json.RawMessage, error) {
		if method != pluginabi.MethodHostHTTPDo {
			t.Fatal(method)
		}
		r := request.(hostRequest)
		if r.HostCallbackID != "callback" {
			t.Fatal("scope missing")
		}
		var response any
		if strings.HasSuffix(r.URL, "/init") {
			initToken = r.Headers.Get("Authorization")
			if len(initToken) != 71 {
				t.Fatal("poll token entropy")
			}
			response = map[string]any{"code": 0, "data": map[string]any{"flow_id": "flow-id", "poll_token": "server-poll-token", "authorize_url": "https://chat.z.ai/authorize", "expires_at": time.Now().Add(time.Minute).Unix()}}
		} else {
			if r.Headers.Get("Authorization") != "Bearer server-poll-token" {
				t.Fatal("wrong polling token")
			}
			response = map[string]any{"code": 0, "data": map[string]any{"status": "ready", "token": "new-account-jwt", "user": map[string]any{"email": "test@example.invalid"}}}
		}
		body, _ := json.Marshal(response)
		return json.Marshal(pluginapi.HTTPResponse{StatusCode: 200, Body: body})
	}
	startRaw, err := handleAuth(pluginabi.MethodAuthLoginStart, []byte(`{"host_callback_id":"callback"}`))
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	_ = json.Unmarshal(startRaw, &env)
	var start pluginapi.AuthLoginStartResponse
	_ = json.Unmarshal(env.Result, &start)
	if start.State != "flow-id" || initToken == "" {
		t.Fatal("login not initialized")
	}
	raw, _ := json.Marshal(struct {
		pluginapi.AuthLoginPollRequest
		Callback string `json:"host_callback_id"`
	}{pluginapi.AuthLoginPollRequest{State: start.State, Metadata: start.Metadata}, "callback"})
	result, err := handleAuth(pluginabi.MethodAuthLoginPoll, raw)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(result, &env)
	var poll pluginapi.AuthLoginPollResponse
	_ = json.Unmarshal(env.Result, &poll)
	if poll.Status != pluginapi.AuthLoginStatusSuccess || poll.Auth.Metadata["access_token"] != "new-account-jwt" || poll.Auth.Prefix != "zcode" {
		t.Fatal("account not persisted")
	}
}

func TestExecutionRejectsUnauthenticatedStaticRequests(t *testing.T) {
	activeConfig.Store(defaultConfig())
	_, err := executionRequest(rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{Headers: map[string][]string{"Authorization": {"Bearer consumer-secret"}}}})
	if err == nil {
		t.Fatal("consumer key used as upstream credential")
	}
}

func TestGrantQuotaDoesNotClaimDailyRenewal(t *testing.T) {
	var b balanceEnvelope
	_ = json.Unmarshal([]byte(`{"code":0,"data":{"balances":[{"show_name":"GLM-5.3-Flash","total_units":100000000,"used_units":30000000,"remaining_units":70000000,"available_units":69000000,"period_end":1791216000}]}}`), &b)
	o := normalizeBalance(b)
	if len(o.Groups) != 1 || o.Groups[0].Buckets[0].Window != "grant" || o.Groups[0].Buckets[0].RemainingFraction != 0.69 || strings.Contains(o.Groups[0].Buckets[0].Description, "daily") {
		t.Fatal("incorrect grant quota")
	}
}

func TestStreamExecutionPreservesChunksAndClosesHostStreams(t *testing.T) {
	activeConfig.Store(defaultConfig())
	old := hostRPC
	t.Cleanup(func() { hostRPC = old })
	done := make(chan string, 1)
	var emitted []byte
	reads := 0
	hostRPC = func(method string, request any) (json.RawMessage, error) {
		switch method {
		case pluginabi.MethodHostHTTPDoStream:
			return json.Marshal(hostStreamResponse{StatusCode: 200, Headers: map[string][]string{"Content-Type": {"text/event-stream"}}, StreamID: "upstream"})
		case pluginabi.MethodHostHTTPStreamRead:
			reads++
			if reads == 1 {
				return json.Marshal(hostStreamRead{Payload: []byte("event: message_start\ndata: {}\n\n")})
			}
			return json.Marshal(hostStreamRead{Payload: []byte("event: message_stop\ndata: {}\n\n"), Done: true})
		case pluginabi.MethodHostStreamEmit:
			emitted = append(emitted, request.(rpcStreamEmitRequest).Payload...)
		case pluginabi.MethodHostHTTPStreamClose:
			if request.(map[string]string)["stream_id"] != "upstream" {
				t.Error("wrong upstream stream")
			}
		case pluginabi.MethodHostStreamClose:
			done <- request.(rpcStreamCloseRequest).Error
		default:
			t.Error("unexpected host method", method)
		}
		return json.RawMessage(`{}`), nil
	}
	stored, _ := json.Marshal(account{Type: pluginID, Token: "jwt", DeviceMid: "device"})
	raw, _ := json.Marshal(rpcExecutorRequest{ExecutorRequest: pluginapi.ExecutorRequest{StorageJSON: stored, Model: "GLM-5.3-Flash", Payload: []byte(`{"messages":[]}`)}, StreamID: "downstream", HostCallbackID: "callback"})
	_, err := handleExecute(pluginabi.MethodExecutorExecuteStream, raw)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-done:
		if message != "" {
			t.Fatal(message)
		}
	case <-time.After(time.Second):
		t.Fatal("stream did not close")
	}
	if string(emitted) != "event: message_start\ndata: {}\n\nevent: message_stop\ndata: {}\n\n" {
		t.Fatal("stream chunks were lost")
	}
}
