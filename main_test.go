package main

import (
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestInterceptAfterAuthServiceTier(t *testing.T) {
	state.Lock()
	state.audit = false
	state.records = make(map[string]tierRecord)
	state.Unlock()

	tests := []struct {
		name            string
		toFormat        string
		body            string
		wantTier        string
		wantInterceptor bool
		wantRoutingHint string
	}{
		{name: "codex fast normalizes to priority", toFormat: "codex", body: `{"service_tier":"fast"}`, wantTier: "priority", wantInterceptor: true, wantRoutingHint: "model=gpt-5.6-sol;tier=priority"},
		{name: "codex priority is unchanged", toFormat: "codex", body: `{"service_tier":"priority"}`, wantTier: "priority", wantRoutingHint: "model=gpt-5.6-sol;tier=priority"},
		{name: "codex ordinary is unchanged", toFormat: "codex", body: `{"model":"gpt-5.6-sol"}`},
		{name: "other provider fast is unchanged", toFormat: "claude", body: `{"service_tier":"fast"}`, wantTier: "fast"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(pluginapi.RequestInterceptRequest{
				RequestID: "request-1",
				ToFormat:  tt.toFormat,
				Model:     "gpt-5.6-sol",
				Body:      []byte(tt.body),
			})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := interceptAfterAuth(raw)
			if err != nil {
				t.Fatal(err)
			}
			var env envelope
			if err := json.Unmarshal(encoded, &env); err != nil {
				t.Fatal(err)
			}
			var response pluginapi.RequestInterceptResponse
			if err := json.Unmarshal(env.Result, &response); err != nil {
				t.Fatal(err)
			}
			if got := len(response.Body) > 0; got != tt.wantInterceptor {
				t.Fatalf("response body present = %v, want %v", got, tt.wantInterceptor)
			}
			if got := response.Headers.Get(routingHintHeader); got != tt.wantRoutingHint {
				t.Fatalf("routing hint = %q, want %q", got, tt.wantRoutingHint)
			}
			body := []byte(tt.body)
			if len(response.Body) > 0 {
				body = response.Body
			}
			if tt.toFormat == "codex" {
				if tier := gjson.GetBytes(body, "service_tier"); tier.Exists() && tier.String() != "priority" {
					body, err = sjson.DeleteBytes(body, "service_tier")
					if err != nil {
						t.Fatal(err)
					}
				}
				normalizeRaw, marshalErr := json.Marshal(pluginapi.RequestTransformRequest{
					ToFormat: "codex",
					Body:     body,
				})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				normalizedEnvelope, normalizeErr := normalizeRequest(normalizeRaw)
				if normalizeErr != nil {
					t.Fatal(normalizeErr)
				}
				var normalizeEnv envelope
				if unmarshalErr := json.Unmarshal(normalizedEnvelope, &normalizeEnv); unmarshalErr != nil {
					t.Fatal(unmarshalErr)
				}
				var normalized pluginapi.PayloadResponse
				if unmarshalErr := json.Unmarshal(normalizeEnv.Result, &normalized); unmarshalErr != nil {
					t.Fatal(unmarshalErr)
				}
				if len(normalized.Body) > 0 {
					body = normalized.Body
				}
			}
			if gjson.GetBytes(body, auditMarkerPath).Exists() {
				t.Fatal("audit marker reached outbound payload")
			}
			if got := gjson.GetBytes(body, "service_tier").String(); got != tt.wantTier {
				t.Fatalf("service_tier = %q, want %q", got, tt.wantTier)
			}
		})
	}
}

func TestPluginCapabilitiesFollowAuditSetting(t *testing.T) {
	state.Lock()
	state.audit = false
	state.records = map[string]tierRecord{"stale": {Requested: "priority"}}
	state.Unlock()

	caps := pluginCapabilities()
	if !caps.RequestInterceptor || !caps.RequestNormalizer {
		t.Fatal("request bridge capabilities must always be enabled")
	}
	if caps.RequestLifecyclePlugin || caps.ResponseInterceptor || caps.StreamChunkInterceptor {
		t.Fatal("audit capabilities must be disabled when audit=false")
	}

	if err := configure([]byte(`{"config_yaml":"YXVkaXQ6IHRydWUK"}`)); err != nil {
		t.Fatal(err)
	}
	caps = pluginCapabilities()
	if !caps.RequestLifecyclePlugin || !caps.ResponseInterceptor || !caps.StreamChunkInterceptor {
		t.Fatal("audit capabilities must be enabled when audit=true")
	}
	state.Lock()
	defer state.Unlock()
	if len(state.records) != 0 {
		t.Fatal("reconfigure must clear prior audit records")
	}
}

func TestObserveResponseTierAcrossChunks(t *testing.T) {
	state.Lock()
	state.records = map[string]tierRecord{
		"request-1": {Requested: "fast", Outbound: "fast"},
	}
	state.Unlock()

	observeResponseTier("request-1", []byte(`data: {"type":"response.completed","response":{"service_`))
	observeResponseTier("request-1", []byte(`tier":"priority"}}`))

	state.Lock()
	record := state.records["request-1"]
	state.Unlock()
	if record.Upstream != "priority" {
		t.Fatalf("upstream service tier = %q, want priority", record.Upstream)
	}
}
