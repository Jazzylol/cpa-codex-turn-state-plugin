package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct {
    uint32_t abi_version;
    void* host_ctx;
    cliproxy_host_call_fn call;
    cliproxy_host_free_fn free_buffer;
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
static cliproxy_host_api host_api;
static void store_host(const cliproxy_host_api* host) { host_api = *host; }
static int call_host(const char* method, const uint8_t* req, size_t len, cliproxy_buffer* response) {
    if (!host_api.call) return 1;
    return host_api.call(host_api.host_ctx, method, req, len, response);
}
static void free_host(void* ptr, size_t len) {
    if (ptr && host_api.free_buffer) host_api.free_buffer(ptr, len);
}
*/
import "C"

import (
	"errors"
	"unsafe"

	"cpa-codex-turn-state/internal/plugin"
)

var app = plugin.NewApp(callHost)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, api *C.cliproxy_plugin_api) C.int {
	if host == nil || api == nil || host.abi_version != 1 {
		return 1
	}
	C.store_host(host)
	api.abi_version = 1
	api.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	api.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	api.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, length C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	if method == nil || uint64(length) > 256<<20 || (length > 0 && request == nil) {
		writeResponse(response, plugin.ErrorEnvelope("invalid plugin call"))
		return 1
	}
	var raw []byte
	if length > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(length))
	}
	result, err := app.Handle(C.GoString(method), raw)
	if err != nil {
		writeResponse(response, plugin.ErrorEnvelope(err.Error()))
		return 1
	}
	writeResponse(response, result)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if len(raw) == 0 {
		return
	}
	response.ptr = C.CBytes(raw)
	response.len = C.size_t(len(raw))
}

func callHost(method string, raw []byte) ([]byte, error) {
	cm := C.CString(method)
	defer C.free(unsafe.Pointer(cm))
	var req *C.uint8_t
	if len(raw) > 0 {
		req = (*C.uint8_t)(C.CBytes(raw))
		defer C.free(unsafe.Pointer(req))
	}
	var response C.cliproxy_buffer
	code := C.call_host(cm, req, C.size_t(len(raw)), &response)
	if response.ptr != nil {
		defer C.free_host(response.ptr, response.len)
	}
	if code != 0 || response.ptr == nil || uint64(response.len) > 256<<20 {
		return nil, errors.New("host callback failed")
	}
	return C.GoBytes(response.ptr, C.int(response.len)), nil
}
