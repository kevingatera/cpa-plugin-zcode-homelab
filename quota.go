package main

import (
	"encoding/json"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"time"
)

type balanceEnvelope struct {
	Code int `json:"code"`
	Data struct {
		ServerTime int64 `json:"server_time"`
		Plans      []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
			EndsAt int64  `json:"ends_at"`
		} `json:"plans"`
		Balances []struct {
			Name      string  `json:"show_name"`
			Total     float64 `json:"total_units"`
			Used      float64 `json:"used_units"`
			Remaining float64 `json:"remaining_units"`
			Available float64 `json:"available_units"`
			End       int64   `json:"period_end"`
		} `json:"balances"`
	} `json:"data"`
}

func handleQuota(raw []byte) ([]byte, error) {
	var r struct {
		pluginapi.QuotaFetchRequest
		HostCallbackID string `json:"host_callback_id"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	var a account
	if err := json.Unmarshal(r.StorageJSON, &a); err != nil {
		return nil, err
	}
	if _, err := authRecord(a, ""); err != nil {
		return nil, err
	}
	var b balanceEnvelope
	err := upstreamJSON(r.HostCallbackID, "GET", zcodeOrigin+"/api/v1/zcode-plan/billing/balance?app_version="+currentConfig().Mimic.AppVersion, sourceHeaders(a.Token, a.DeviceMid), nil, &b)
	if err != nil {
		return nil, err
	}
	if b.Code != 0 {
		return nil, fmt.Errorf("ZCode billing query failed (%d)", b.Code)
	}
	return okEnvelope(normalizeBalance(b))
}

func normalizeBalance(b balanceEnvelope) pluginapi.QuotaFetchResponse {
	out := pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "ZCode Start Plan"}, ServerTimeOffsetMs: b.Data.ServerTime*1000 - time.Now().UnixMilli()}
	for _, p := range b.Data.Plans {
		if p.Status == "active" {
			out.Subscription.Plan = p.Name
			break
		}
	}
	for _, v := range b.Data.Balances {
		fraction := 0.0
		if v.Total > 0 {
			fraction = v.Available / v.Total
		}
		if fraction < 0 {
			fraction = 0
		}
		if fraction > 1 {
			fraction = 1
		}
		out.Groups = append(out.Groups, pluginapi.QuotaGroup{DisplayName: v.Name, Buckets: []pluginapi.QuotaBucket{{Window: "grant", RemainingFraction: fraction, ResetTime: time.Unix(v.End, 0).UTC().Format(time.RFC3339), Description: fmt.Sprintf("%.0f available / %.0f tokens; grant expiry, renewal is not assumed", v.Available, v.Total)}}})
		out.Summary = append(out.Summary, pluginapi.QuotaMetric{Key: "available_tokens", Label: "Available tokens", Value: v.Available, Unit: "tokens", Format: "number"}, pluginapi.QuotaMetric{Key: "used_tokens", Label: "Used tokens", Value: v.Used, Unit: "tokens", Format: "number"})
	}
	return out
}
