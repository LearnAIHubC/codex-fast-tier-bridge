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

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"gopkg.in/yaml.v3"
)

const auditMarkerPath = "__cpa_fast_tier_bridge"
const routingHintHeader = "X-Codex-Routing-Hint"

var responseTierPattern = regexp.MustCompile(`"service_tier"\s*:\s*"([^"]+)"`)

var state = struct {
	sync.Mutex
	audit   bool
	records map[string]tierRecord
}{records: make(map[string]tierRecord)}

type tierRecord struct {
	Requested    string
	Outbound     string
	Upstream     string
	ResponseTail []byte
}

type pluginConfig struct {
	Audit bool `yaml:"audit"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	RequestInterceptor     bool `json:"request_interceptor"`
	RequestNormalizer      bool `json:"request_normalizer"`
	RequestLifecyclePlugin bool `json:"request_lifecycle_plugin"`
	ResponseInterceptor    bool `json:"response_interceptor"`
	StreamChunkInterceptor bool `json:"response_stream_interceptor"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
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
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	state.Lock()
	defer state.Unlock()
	state.records = make(map[string]tierRecord)
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if err := configure(request); err != nil {
			return nil, err
		}
		return okEnvelope(registration{
			SchemaVersion: pluginabi.SchemaVersion,
			Metadata: pluginapi.Metadata{
				Name:             "codex-fast-tier-bridge",
				Version:          "1.4.0",
				Author:           "learnAIHubC",
				GitHubRepository: "https://github.com/learnAIHubC/codex-fast-tier-bridge",
			},
			Capabilities: pluginCapabilities(),
		})
	case pluginabi.MethodRequestInterceptBefore:
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	case pluginabi.MethodRequestInterceptAfter:
		return interceptAfterAuth(request)
	case pluginabi.MethodRequestNormalize:
		return normalizeRequest(request)
	case pluginabi.MethodRequestComplete:
		return completeRequest(request)
	case pluginabi.MethodResponseInterceptAfter:
		return interceptResponse(request)
	case pluginabi.MethodResponseInterceptStreamChunk:
		return interceptStreamChunk(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
	}
	var cfg pluginConfig
	if len(req.ConfigYAML) > 0 {
		if err := yaml.Unmarshal(req.ConfigYAML, &cfg); err != nil {
			return err
		}
	}
	state.Lock()
	state.audit = cfg.Audit
	state.records = make(map[string]tierRecord)
	state.Unlock()
	return nil
}

func pluginCapabilities() registrationCapability {
	state.Lock()
	audit := state.audit
	state.Unlock()
	return registrationCapability{
		RequestInterceptor:     true,
		RequestNormalizer:      true,
		RequestLifecyclePlugin: audit,
		ResponseInterceptor:    audit,
		StreamChunkInterceptor: audit,
	}
}

func interceptAfterAuth(raw []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(req.ToFormat), "codex") {
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	}
	requestedTier := strings.TrimSpace(gjson.GetBytes(req.Body, "service_tier").String())
	fastRequested := strings.EqualFold(requestedTier, "fast") || strings.EqualFold(requestedTier, "priority")
	state.Lock()
	audit := state.audit
	state.Unlock()
	if !audit && !fastRequested {
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	}
	response := pluginapi.RequestInterceptResponse{}
	if fastRequested {
		model := strings.TrimSpace(req.Model)
		if model != "" && !strings.ContainsAny(model, "\r\n;") {
			response.Headers = http.Header{
				routingHintHeader: {"model=" + model + ";tier=priority"},
			}
		}
	}
	if !audit && strings.EqualFold(requestedTier, "priority") {
		return okEnvelope(response)
	}
	marker, err := json.Marshal(map[string]string{
		"request_id":     req.RequestID,
		"requested_tier": requestedTier,
	})
	if err != nil {
		return nil, err
	}
	updated, err := sjson.SetRawBytes(req.Body, auditMarkerPath, marker)
	if err != nil {
		return nil, err
	}
	response.Body = updated
	return okEnvelope(response)
}

func normalizeRequest(raw []byte) ([]byte, error) {
	var req pluginapi.RequestTransformRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(req.ToFormat), "codex") {
		return okEnvelope(pluginapi.PayloadResponse{})
	}
	marker := gjson.GetBytes(req.Body, auditMarkerPath)
	if !marker.Exists() {
		return okEnvelope(pluginapi.PayloadResponse{})
	}
	requestID := marker.Get("request_id").String()
	requestedTier := strings.TrimSpace(marker.Get("requested_tier").String())
	updated, err := sjson.DeleteBytes(req.Body, auditMarkerPath)
	if err != nil {
		return nil, err
	}
	if strings.EqualFold(requestedTier, "fast") {
		updated, err = sjson.SetBytes(updated, "service_tier", "priority")
		if err != nil {
			return nil, err
		}
	}
	outboundTier := strings.TrimSpace(gjson.GetBytes(updated, "service_tier").String())
	state.Lock()
	if state.audit && requestID != "" {
		state.records[requestID] = tierRecord{Requested: requestedTier, Outbound: outboundTier}
	}
	state.Unlock()
	return okEnvelope(pluginapi.PayloadResponse{Body: updated})
}

func interceptResponse(raw []byte) ([]byte, error) {
	var req pluginapi.ResponseInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	observeResponseTier(req.RequestID, req.Body)
	return okEnvelope(pluginapi.ResponseInterceptResponse{})
}

func interceptStreamChunk(raw []byte) ([]byte, error) {
	var req pluginapi.StreamChunkInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	observeResponseTier(req.RequestID, req.Body)
	return okEnvelope(pluginapi.StreamChunkInterceptResponse{})
}

func observeResponseTier(requestID string, body []byte) {
	if requestID == "" || len(body) == 0 {
		return
	}
	state.Lock()
	defer state.Unlock()
	record, exists := state.records[requestID]
	if !exists {
		return
	}
	combined := make([]byte, 0, len(record.ResponseTail)+len(body))
	combined = append(combined, record.ResponseTail...)
	combined = append(combined, body...)
	matches := responseTierPattern.FindAllSubmatch(combined, -1)
	if len(matches) > 0 {
		record.Upstream = string(matches[len(matches)-1][1])
	}
	const tailSize = 256
	if len(combined) > tailSize {
		record.ResponseTail = append(record.ResponseTail[:0], combined[len(combined)-tailSize:]...)
	} else {
		record.ResponseTail = append(record.ResponseTail[:0], combined...)
	}
	state.records[requestID] = record
}

func completeRequest(raw []byte) ([]byte, error) {
	var completion pluginapi.RequestCompletion
	if err := json.Unmarshal(raw, &completion); err != nil {
		return nil, err
	}
	state.Lock()
	record, exists := state.records[completion.RequestID]
	delete(state.records, completion.RequestID)
	audit := state.audit
	state.Unlock()
	if exists && audit {
		requested := record.Requested
		if requested == "" {
			requested = "(none)"
		}
		outbound := record.Outbound
		if outbound == "" {
			outbound = "(none)"
		}
		upstream := record.Upstream
		if upstream == "" {
			upstream = "(none)"
		}
		log.Printf("service-tier-audit requested=%s outbound=%s upstream=%s outcome=%s", requested, outbound, upstream, completion.Outcome)
	}
	return okEnvelope(struct{}{})
}

func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, err := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"encode error"}}`)
	}
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
