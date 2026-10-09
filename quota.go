package main

import (
	"encoding/json"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"math"
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
	if a.AuthKind == "apikey" {
		var envelope individualQuotaEnvelope
		err := upstreamJSON(r.HostCallbackID, "GET", "https://api.z.ai/api/monitor/usage/quota/limit", sourceHeaders(a.APIKey, a.DeviceMid), nil, &envelope)
		if err != nil {
			return nil, err
		}
		report, err := normalizeIndividualQuota(envelope)
		if err != nil {
			return nil, err
		}
		return okEnvelope(report)
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

type individualQuotaEnvelope struct {
	Code    int  `json:"code"`
	Success bool `json:"success"`
	Data    struct {
		Limits []struct {
			Type          string   `json:"type"`
			Unit          int      `json:"unit"`
			Number        int      `json:"number"`
			Percentage    *float64 `json:"percentage"`
			NextResetTime int64    `json:"nextResetTime"`
		} `json:"limits"`
	} `json:"data"`
}

func normalizeIndividualQuota(envelope individualQuotaEnvelope) (pluginapi.QuotaFetchResponse, error) {
	report := pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "Z.ai Individual Coding Plan"}}
	if !envelope.Success || envelope.Code != 200 {
		return report, fmt.Errorf("individual Coding Plan quota query failed")
	}
	group := pluginapi.QuotaGroup{DisplayName: "Z.ai Individual Coding Plan"}
	for _, limit := range envelope.Data.Limits {
		if limit.Type != "CREDIT_LIMIT" && limit.Type != "TOKENS_LIMIT" {
			continue
		}
		if limit.Percentage == nil || math.IsNaN(*limit.Percentage) || math.IsInf(*limit.Percentage, 0) || *limit.Percentage < 0 || *limit.Percentage > 100 {
			return report, fmt.Errorf("invalid individual Coding Plan quota percentage")
		}
		window := fmt.Sprintf("Allowance (%d x unit %d)", limit.Number, limit.Unit)
		switch limit.Unit {
		case 3:
			window = fmt.Sprintf("%d-hour", limit.Number)
		case 6:
			window = fmt.Sprintf("%d-week", limit.Number)
		case 5:
			window = fmt.Sprintf("%d-month", limit.Number)
		}
		bucket := pluginapi.QuotaBucket{Window: window, RemainingFraction: 1 - *limit.Percentage/100, Description: "Provider-reported allowance remaining"}
		if limit.NextResetTime > 0 {
			bucket.ResetTime = time.UnixMilli(limit.NextResetTime).UTC().Format(time.RFC3339)
		}
		group.Buckets = append(group.Buckets, bucket)
	}
	if len(group.Buckets) == 0 {
		return report, fmt.Errorf("individual Coding Plan returned no valid allowance windows")
	}
	report.Groups = []pluginapi.QuotaGroup{group}
	return report, nil
}
