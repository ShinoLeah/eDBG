package mcp

import (
	"context"
	"crypto/rand"
	"eDBG/cli"
	"eDBG/controller"
	"eDBG/module"
	"eDBG/utils"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultProtocolVersion = "2025-03-26"
	sessionHeaderName      = "Mcp-Session-Id"
	defaultContinueTimeout = 90 * time.Second
	maxContinueTimeout     = 90 * time.Second
	guideResourceURI       = "edbg://guide/mcp"
	statusResourceURI      = "edbg://runtime/status"
)

var ansiEscapePattern = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

type Server struct {
	client     *cli.Client
	httpServer *http.Server

	mu       sync.Mutex
	toolMu   sync.Mutex
	sessions map[string]*session
}

type session struct {
	id              string
	protocolVersion string
	initialized     bool
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Result  interface{} `json:"result,omitempty"`
	Error   *rpcError   `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func NewServer(client *cli.Client) *Server {
	return &Server{
		client:   client,
		sessions: make(map[string]*session),
	}
}

func (this *Server) Serve(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", this.handleMCP)

	this.httpServer = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	err := this.httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (this *Server) Shutdown(ctx context.Context) error {
	if this.httpServer == nil {
		return nil
	}
	return this.httpServer.Shutdown(ctx)
}

func (this *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if err := validateOrigin(r); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	this.setCommonHeaders(w)

	switch r.Method {
	case http.MethodPost:
		this.handlePost(w, r)
	case http.MethodDelete:
		this.handleDelete(w, r)
	case http.MethodOptions:
		w.WriteHeader(http.StatusNoContent)
	case http.MethodHead:
		this.handleHead(w, r)
	case http.MethodGet:
		this.handleGet(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (this *Server) handleHead(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get(sessionHeaderName)
	if sessionID != "" {
		if _, err := this.getSession(sessionID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	if sessionID != "" {
		w.Header().Set(sessionHeaderName, sessionID)
	}
	w.WriteHeader(http.StatusOK)
}

func (this *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get(sessionHeaderName)
	if sessionID != "" {
		if _, err := this.getSession(sessionID); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	if sessionID != "" {
		w.Header().Set(sessionHeaderName, sessionID)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(": stream opened\n\n"))
	flusher.Flush()

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		}
	}
}

func (this *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get(sessionHeaderName)
	if sessionID == "" {
		http.Error(w, "missing MCP session id", http.StatusBadRequest)
		return
	}

	this.mu.Lock()
	delete(this.sessions, sessionID)
	this.mu.Unlock()

	w.WriteHeader(http.StatusNoContent)
}

func (this *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON-RPC request body", http.StatusBadRequest)
		return
	}

	if req.JSONRPC == "" {
		req.JSONRPC = "2.0"
	}
	if req.JSONRPC != "2.0" {
		this.writeRPCError(w, req.ID, -32600, "only JSON-RPC 2.0 is supported")
		return
	}

	if req.Method == "" {
		this.writeRPCError(w, req.ID, -32600, "missing method")
		return
	}

	if req.Method == "initialize" {
		this.handleInitialize(w, req)
		return
	}

	session, err := this.getSession(r.Header.Get(sessionHeaderName))
	if err != nil {
		this.writeRPCError(w, req.ID, -32001, err.Error())
		return
	}

	w.Header().Set(sessionHeaderName, session.id)
	w.Header().Set("MCP-Protocol-Version", session.protocolVersion)

	if req.Method == "notifications/initialized" {
		session.initialized = true
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if !session.initialized {
		this.writeRPCError(w, req.ID, -32002, "session is not initialized")
		return
	}

	switch req.Method {
	case "ping":
		this.writeRPCResult(w, req.ID, map[string]interface{}{})
	case "tools/list":
		this.writeRPCResult(w, req.ID, map[string]interface{}{
			"tools": this.toolDefinitions(),
		})
	case "resources/list":
		this.writeRPCResult(w, req.ID, map[string]interface{}{
			"resources": this.resourceDefinitions(),
		})
	case "resources/read":
		this.handleResourceRead(w, req)
	case "resources/templates/list":
		this.writeRPCResult(w, req.ID, map[string]interface{}{
			"resourceTemplates": []map[string]interface{}{},
		})
	case "tools/call":
		this.handleToolCall(w, r, req)
	default:
		this.writeRPCError(w, req.ID, -32601, "method not found")
	}
}

func (this *Server) handleInitialize(w http.ResponseWriter, req rpcRequest) {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(req.Params, &params)

	version := chooseProtocolVersion(params.ProtocolVersion)
	sessionID, err := randomSessionID()
	if err != nil {
		this.writeRPCError(w, req.ID, -32000, fmt.Sprintf("failed to create session: %v", err))
		return
	}

	this.mu.Lock()
	this.sessions[sessionID] = &session{
		id:              sessionID,
		protocolVersion: version,
	}
	this.mu.Unlock()

	w.Header().Set(sessionHeaderName, sessionID)
	w.Header().Set("MCP-Protocol-Version", version)

	this.writeRPCResult(w, req.ID, map[string]interface{}{
		"protocolVersion": version,
		"capabilities": map[string]interface{}{
			"tools":     map[string]interface{}{},
			"resources": map[string]interface{}{},
		},
		"serverInfo": map[string]interface{}{
			"name":    "eDBG MCP",
			"version": "0.1.0",
		},
		"instructions": "Start eDBG on the device with only --mcp. Read the resource edbg://guide/mcp for workflow and tool restrictions. Use status to test connectivity and inspect the current state. Then call attach with package and library to select the target. Before attach, all other debug tools will return guidance instead of executing.",
	})
}

func (this *Server) handleToolCall(w http.ResponseWriter, httpReq *http.Request, req rpcRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		this.writeRPCError(w, req.ID, -32602, fmt.Sprintf("invalid tool call params: %v", err))
		return
	}
	if params.Name == "" {
		this.writeRPCError(w, req.ID, -32602, "missing tool name")
		return
	}

	args := make(map[string]interface{})
	if len(params.Arguments) > 0 {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			this.writeRPCError(w, req.ID, -32602, fmt.Sprintf("invalid tool arguments: %v", err))
			return
		}
	}

	this.toolMu.Lock()
	defer this.toolMu.Unlock()

	toolResult, err := this.callTool(httpReq.Context(), params.Name, args)
	if toolResult == nil {
		toolResult = this.errorToolResult(params.Name, "internal_error", "tool returned no result", nil, nil, nil)
	}

	payloadText := this.mustJSON(toolResult)
	result := map[string]interface{}{
		"structuredContent": toolResult,
		"content": []map[string]interface{}{
			{
				"type": "text",
				"text": payloadText,
			},
		},
	}
	if okValue, ok := toolResult["ok"].(bool); ok && !okValue {
		result["isError"] = true
	} else if err != nil {
		result["isError"] = true
	}

	this.writeRPCResult(w, req.ID, result)
}

func (this *Server) handleResourceRead(w http.ResponseWriter, req rpcRequest) {
	var params struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		this.writeRPCError(w, req.ID, -32602, fmt.Sprintf("invalid resources/read params: %v", err))
		return
	}

	var text string
	switch params.URI {
	case guideResourceURI:
		text = this.renderGuideResource()
	case statusResourceURI:
		text = this.renderStatus()
	default:
		this.writeRPCError(w, req.ID, -32002, fmt.Sprintf("resource not found: %s", params.URI))
		return
	}

	this.writeRPCResult(w, req.ID, map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"uri":      params.URI,
				"mimeType": "text/plain",
				"text":     text,
			},
		},
	})
}

func (this *Server) callTool(ctx context.Context, name string, args map[string]interface{}) (map[string]interface{}, error) {
	if !this.client.HasTarget() && name != "attach" && name != "status" {
		return this.errorToolResult(
			name,
			"requires_attach",
			fmt.Sprintf("%s is unavailable before attach", name),
			nil,
			nil,
			[]string{
				"Call status to confirm connectivity if needed.",
				"Call attach with package and library before using this tool.",
			},
		), fmt.Errorf("%s is unavailable before attach", name)
	}

	switch name {
	case "status":
		return this.successToolResult(name, "Current MCP status.", this.statusData(), nil, nil), nil
	case "attach":
		return this.handleAttach(args)
	case "break":
		if err := this.requireTarget("break"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		addressArg, err := requiredStringArg(args, "address")
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		address, err := this.client.ParseUserAddress(addressArg)
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		virtualOffset := address.Offset
		if address.LibInfo.LibPath == "" {
			err = fmt.Errorf("full path for library %s not found", address.LibInfo.LibName)
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		fileOffset, err := utils.ConvertVirtualOffsetToFileOffset(address.LibInfo.LibPath, virtualOffset)
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		address.Offset = fileOffset
		if err = this.client.BrkManager.CreateBreakPoint(address, true); err != nil {
			return this.wrapToolError(name, "breakpoint_error", err.Error(), nil, nil), err
		}
		id, brk := this.findBreakpoint(address)
		data := map[string]interface{}{
			"requested_address": addressArg,
			"breakpoints":       this.breakpointListData(),
		}
		if brk != nil {
			data["created"] = this.breakpointData(id, brk)
		}
		return this.successToolResult(name, "Breakpoint set.", data, nil, nil), nil
	case "enable_breakpoint":
		id, err := requiredIntArg(args, "id")
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		if err := this.changeBreakpointState(id, true); err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		return this.successToolResult(name, "Breakpoint enabled.", map[string]interface{}{
			"breakpoint_id": id,
			"action":        "enable",
			"breakpoints":   this.breakpointListData(),
		}, nil, nil), nil
	case "disable_breakpoint":
		id, err := requiredIntArg(args, "id")
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		if err := this.changeBreakpointState(id, false); err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		return this.successToolResult(name, "Breakpoint disabled.", map[string]interface{}{
			"breakpoint_id": id,
			"action":        "disable",
			"breakpoints":   this.breakpointListData(),
		}, nil, nil), nil
	case "delete_breakpoint":
		id, err := requiredIntArg(args, "id")
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		if err := this.deleteBreakpoint(id); err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		return this.successToolResult(name, "Breakpoint deleted.", map[string]interface{}{
			"breakpoint_id": id,
			"action":        "delete",
			"breakpoints":   this.breakpointListData(),
		}, nil, nil), nil
	case "info_break":
		return this.successToolResult(name, "Breakpoint list.", map[string]interface{}{
			"breakpoints": this.breakpointListData(),
		}, nil, nil), nil
	case "info_file":
		if err := this.requireTarget("info_file"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		return this.successToolResult(name, "Target library info.", this.fileInfoData(), nil, nil), nil
	case "info_register":
		if err := this.requireStopped("info_register"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		return this.successToolResult(name, "Register context.", map[string]interface{}{
			"registers": this.registerData(),
		}, nil, nil), nil
	case "info_thread":
		if err := this.requireStopped("info_thread"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		data, dataErr := this.threadData()
		if dataErr != nil {
			return this.wrapToolError(name, "runtime_error", dataErr.Error(), nil, nil), dataErr
		}
		return this.successToolResult(name, "Thread info.", data, nil, nil), nil
	case "run":
		return this.handleRun(ctx, args)
	case "continue":
		if err := this.requireTarget("continue"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		return this.handleContinue(ctx, args)
	case "wait_stop":
		if err := this.requireTarget("wait_stop"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		return this.handleWaitStop(ctx, args)
	case "cancel_run":
		if err := this.requireTarget("cancel_run"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		return this.handleCancelRun()
	case "examine":
		if err := this.requireStopped("examine"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		address, err := requiredStringArg(args, "address")
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		data, dataErr := this.examineData(address, optionalStringArg(args, "length_or_type", ""))
		if dataErr != nil {
			return this.wrapToolError(name, "runtime_error", dataErr.Error(), nil, nil), dataErr
		}
		return this.successToolResult(name, "Memory read completed.", data, nil, nil), nil
	case "list":
		if err := this.requireStopped("list"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		data, dataErr := this.disassemblyData(optionalStringArg(args, "address", ""), optionalIntArgValue(args, "count", 10))
		if dataErr != nil {
			return this.wrapToolError(name, "runtime_error", dataErr.Error(), nil, nil), dataErr
		}
		return this.successToolResult(name, "Disassembly generated.", data, nil, nil), nil
	case "backtrace":
		if err := this.requireStopped("backtrace"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		mode := optionalStringArg(args, "mode", "unwind")
		text := this.client.CaptureOutput(func() {
			if mode == "fp" {
				this.client.HandleBacktraceByFP(nil)
				return
			}
			this.client.HandleBacktraceByUnwind(nil)
		})
		return this.successToolResult(name, "Backtrace captured.", this.backtraceData(mode, text), nil, nil), nil
	case "thread":
		if err := this.requireStopped("thread"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		action := optionalStringArg(args, "action", "list")
		value := optionalStringArg(args, "value", "")
		params := []string{}
		switch action {
		case "list":
		case "add", "+", "name", "+n", "del", "-", "delete":
			if value == "" {
				err := fmt.Errorf("thread action %q requires value", action)
				return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
			}
			params = []string{action, value}
		case "all":
			params = []string{"all"}
		default:
			err := fmt.Errorf("unsupported thread action %q", action)
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		_ = this.client.CaptureOutput(func() {
			this.client.HandleThread(params)
		})
		data, dataErr := this.threadData()
		if dataErr != nil {
			return this.wrapToolError(name, "runtime_error", dataErr.Error(), nil, nil), dataErr
		}
		data["action"] = action
		if value != "" {
			data["value"] = value
		}
		return this.successToolResult(name, "Thread filter state updated.", data, nil, nil), nil
	case "set_symbol":
		if err := this.requireStopped("set_symbol"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		address, err := requiredStringArg(args, "address")
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		symbolName, err := requiredStringArg(args, "name")
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		absolute, addrErr := this.client.ParseUserAddressToAbsolute(address)
		if addrErr != nil {
			return this.wrapToolError(name, "invalid_arguments", addrErr.Error(), nil, nil), addrErr
		}
		this.client.Process.Symbols[absolute] = symbolName
		return this.successToolResult(name, "Symbol updated.", map[string]interface{}{
			"address": hexUint64(absolute),
			"name":    symbolName,
			"updated": true,
		}, nil, nil), nil
	case "write_memory":
		if err := this.requireStopped("write_memory"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		address, err := requiredStringArg(args, "address")
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		hexValue, err := requiredStringArg(args, "hex")
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		data, dataErr := this.writeMemoryData(address, hexValue)
		if dataErr != nil {
			return this.wrapToolError(name, "runtime_error", dataErr.Error(), nil, nil), dataErr
		}
		return this.successToolResult(name, "Memory updated.", data, nil, nil), nil
	case "dump":
		if err := this.requireStopped("dump"); err != nil {
			return this.wrapToolError(name, "invalid_state", err.Error(), nil, nil), err
		}
		address, err := requiredStringArg(args, "address")
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		length, err := requiredStringArg(args, "length")
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		fileName, err := requiredStringArg(args, "file")
		if err != nil {
			return this.wrapToolError(name, "invalid_arguments", err.Error(), nil, nil), err
		}
		data, dataErr := this.dumpData(address, length, fileName)
		if dataErr != nil {
			return this.wrapToolError(name, "runtime_error", dataErr.Error(), nil, nil), dataErr
		}
		return this.successToolResult(name, "Memory dump written.", data, nil, nil), nil
	case "quit":
		return this.handleQuit()
	default:
		err := fmt.Errorf("unknown tool %q", name)
		return this.wrapToolError(name, "unknown_tool", err.Error(), nil, nil), err
	}
}

func (this *Server) handleRun(ctx context.Context, args map[string]interface{}) (map[string]interface{}, error) {
	if optionalStringArg(args, "package", "") != "" ||
		optionalStringArg(args, "package_name", "") != "" ||
		optionalStringArg(args, "library", "") != "" ||
		optionalStringArg(args, "lib", "") != "" {
		err := fmt.Errorf("run no longer accepts package or library; use attach instead")
		return this.wrapToolError("run", "invalid_arguments", err.Error(), nil, nil), err
	}
	if err := this.requireTarget("run"); err != nil {
		return this.wrapToolError("run", "invalid_state", err.Error(), nil, nil), err
	}
	if this.client.IsStopped() {
		err := fmt.Errorf("run is unavailable while the target is stopped; use continue or quit first")
		return this.wrapToolError("run", "invalid_state", err.Error(), nil, nil), err
	}
	if !this.hasEnabledBreakpoints() {
		err := fmt.Errorf("run requires at least one enabled breakpoint")
		return this.wrapToolError("run", "invalid_state", err.Error(), nil, nil), err
	}

	forceStopOutput, forceStopErr := this.forceStopPackage()
	if forceStopErr != nil {
		return this.wrapToolError("run", "runtime_error", forceStopErr.Error(), nil, nil), forceStopErr
	}

	this.client.Process.UpdatePidList()
	if len(this.client.Process.PidList) > 0 {
		err := fmt.Errorf("target app %s is still running after force-stop", this.client.Process.PackageName)
		return this.wrapToolError("run", "invalid_state", err.Error(), nil, []string{"Call quit and retry if the app cannot be stopped cleanly."}), err
	}

	timeout := effectiveWaitTimeout(args)

	seq := this.client.CurrentStopSequence()
	var continueErr error
	continueOutput := this.client.CaptureOutput(func() {
		if !this.client.HandleContinue() {
			continueErr = fmt.Errorf("continue failed")
		}
	})
	if continueErr != nil {
		return this.wrapToolError("run", "runtime_error", continueErr.Error(), []string{cleanText(continueOutput)}, nil), continueErr
	}

	this.client.SetRunIssued(true)
	activity := optionalStringArg(args, "activity", "")
	output, err := this.launchApp(activity)
	if err != nil {
		if this.client.BrkManager != nil && this.client.BrkManager.Running {
			_ = this.client.BrkManager.Stop()
		}
		this.client.MarkStandby()
		return this.wrapToolError("run", "runtime_error", err.Error(), []string{cleanText(output)}, nil), err
	}

	launchData := map[string]interface{}{
		"launch": map[string]interface{}{
			"activity":      this.resolveActivityName(activity, output),
			"am_start":      this.parseAmStartOutput(output),
			"force_stopped": cleanText(forceStopOutput) == "",
		},
	}
	result, waitErr := this.waitForStop(ctx, "run", seq, timeout, launchData)
	if waitErr != nil {
		return result, waitErr
	}
	if okValue, ok := result["ok"].(bool); ok && okValue {
		if data, ok := result["data"].(map[string]interface{}); ok {
			if _, hasStop := data["stop"]; hasStop {
				result["message"] = "App launched and first breakpoint hit."
				data["first_stop"] = data["stop"]
				delete(data, "stop")
			}
		}
	}
	return result, nil
}

func (this *Server) handleAttach(args map[string]interface{}) (map[string]interface{}, error) {
	if this.client.IsStopped() {
		err := fmt.Errorf("attach is unavailable while the target is stopped; use continue or quit first")
		return this.wrapToolError("attach", "invalid_state", err.Error(), nil, nil), err
	}

	packageName := optionalStringArg(args, "package", "")
	libName := optionalStringArg(args, "library", "")
	if packageName == "" {
		packageName = optionalStringArg(args, "package_name", "")
	}
	if libName == "" {
		libName = optionalStringArg(args, "lib", "")
	}

	switchedTarget := this.client.HasTarget() &&
		(this.client.Process.PackageName != packageName || this.client.Library.LibName != libName)

	if err := this.ensureTarget(packageName, libName); err != nil {
		return this.wrapToolError("attach", "runtime_error", err.Error(), nil, nil), err
	}

	return this.successToolResult("attach", "Target attached.", map[string]interface{}{
		"package":         this.client.Process.PackageName,
		"library":         this.client.Library.LibName,
		"switched_target": switchedTarget,
	}, nil, nil), nil
}

func (this *Server) handleQuit() (map[string]interface{}, error) {
	if err := this.resetTarget(); err != nil {
		return this.wrapToolError("quit", "runtime_error", err.Error(), nil, nil), err
	}
	return this.successToolResult("quit", "MCP session reset. The server is still running.", map[string]interface{}{
		"cleared": true,
	}, nil, []string{"Call attach with package and library to start a new session."}), nil
}

func (this *Server) renderStatus() string {
	var builder strings.Builder

	builder.WriteString("eDBG MCP status\n")
	builder.WriteString("Server: reachable\n")
	fmt.Fprintf(&builder, "Protocol: %s\n", defaultProtocolVersion)

	targetSelected := this.client.HasTarget()
	fmt.Fprintf(&builder, "Target Selected: %t\n", targetSelected)
	fmt.Fprintf(&builder, "Target Stopped: %t\n", this.client.IsStopped())
	fmt.Fprintf(&builder, "Run Issued: %t\n", this.client.HasRunIssued())
	fmt.Fprintf(&builder, "Probe Running: %t\n", this.client.BrkManager != nil && this.client.BrkManager.Running)
	fmt.Fprintf(&builder, "Enabled Breakpoints: %d\n", this.enabledBreakpointCount())

	if targetSelected {
		processData := this.processRuntimeData()
		fmt.Fprintf(&builder, "Package: %s\n", this.client.Process.PackageName)
		fmt.Fprintf(&builder, "Library: %s\n", this.client.Library.LibName)
		if processStatus, ok := processData["status"].(string); ok {
			fmt.Fprintf(&builder, "Process Status: %s\n", processStatus)
		}
		if pidList, ok := processData["pid_list"].([]uint32); ok {
			fmt.Fprintf(&builder, "PIDs: %v\n", pidList)
		}
		if unexpectedExit, ok := processData["unexpected_exit"].(bool); ok && unexpectedExit {
			builder.WriteString("Process Exit: target appears to have exited unexpectedly while eDBG was waiting.\n")
		}
	} else {
		builder.WriteString("Package: (none)\n")
		builder.WriteString("Library: (none)\n")
	}

	builder.WriteString("\nWorkflow:\n")
	if !targetSelected {
		builder.WriteString("- First call attach with package and library.\n")
		builder.WriteString("- Before attach, all other debug tools will return a guidance message.\n")
		builder.WriteString("- Read resource edbg://guide/mcp for the full tool policy.\n")
		return strings.TrimSpace(builder.String())
	}

	if this.client.IsStopped() {
		builder.WriteString("- The target is currently stopped on a breakpoint.\n")
		builder.WriteString("- Stopped-only tools are now available: info_register, info_thread, examine, list, backtrace, thread, set_symbol, write_memory, dump.\n")
	} else if !this.client.HasRunIssued() {
		builder.WriteString("- Set breakpoints, then call run.\n")
		builder.WriteString("- run will arm the probes, launch the app, and wait for the first breakpoint hit.\n")
	} else if processStatus, ok := this.processRuntimeData()["status"].(string); ok && processStatus == "exited" {
		builder.WriteString("- The target process is no longer running.\n")
		builder.WriteString("- You can inspect status, then call cancel_run or quit before retrying.\n")
	} else {
		builder.WriteString("- The target is currently running under probes.\n")
		builder.WriteString("- Use wait_stop to keep waiting, cancel_run to stop waiting and return to standby, or quit to fully reset.\n")
	}

	return strings.TrimSpace(builder.String())
}

func (this *Server) handleContinue(ctx context.Context, args map[string]interface{}) (map[string]interface{}, error) {
	if !this.client.IsStopped() && !this.client.HasRunIssued() {
		err := fmt.Errorf("continue is unavailable in standby before run; set breakpoints, launch the app with run, then continue")
		return this.wrapToolError("continue", "invalid_state", err.Error(), nil, nil), err
	}
	if !this.client.IsStopped() && this.client.HasRunIssued() {
		err := fmt.Errorf("continue is unavailable while the target is already running")
		return this.wrapToolError("continue", "already_running", err.Error(), nil, []string{"Use wait_stop to keep waiting for the current run, cancel_run to return to standby, or quit to fully reset the session."}), err
	}
	if !this.hasEnabledBreakpoints() {
		err := fmt.Errorf("continue requires at least one enabled breakpoint")
		return this.wrapToolError("continue", "invalid_state", err.Error(), nil, nil), err
	}

	timeout := effectiveWaitTimeout(args)

	seq := this.client.CurrentStopSequence()

	var continueErr error
	output := this.client.CaptureOutput(func() {
		if !this.client.HandleContinue() {
			continueErr = fmt.Errorf("continue failed")
		}
	})
	if continueErr != nil {
		return this.wrapToolError("continue", "runtime_error", continueErr.Error(), []string{cleanText(output)}, nil), continueErr
	}

	return this.waitForStop(ctx, "continue", seq, timeout, nil)
}

func (this *Server) handleWaitStop(ctx context.Context, args map[string]interface{}) (map[string]interface{}, error) {
	if this.client.IsStopped() {
		err := fmt.Errorf("wait_stop is unavailable while the target is already stopped")
		return this.wrapToolError("wait_stop", "invalid_state", err.Error(), nil, []string{"Use the stopped-state inspection tools now, or call continue when you are ready to resume."}), err
	}
	if !this.client.HasRunIssued() {
		err := fmt.Errorf("wait_stop is unavailable because no run is currently in progress")
		return this.wrapToolError("wait_stop", "invalid_state", err.Error(), nil, []string{"Call run to launch the app, or call continue after a stopped breakpoint to resume execution."}), err
	}

	timeout := effectiveWaitTimeout(args)

	return this.waitForStop(ctx, "wait_stop", this.client.CurrentStopSequence(), timeout, nil)
}

func (this *Server) handleCancelRun() (map[string]interface{}, error) {
	if !this.client.HasRunIssued() && (this.client.BrkManager == nil || !this.client.BrkManager.Running) {
		return this.successToolResult("cancel_run", "No active run was in progress.", map[string]interface{}{
			"cancelled": false,
			"standby":   true,
		}, nil, nil), nil
	}

	if this.client.BrkManager != nil && this.client.BrkManager.Running {
		if err := this.client.BrkManager.Stop(); err != nil {
			return this.wrapToolError("cancel_run", "runtime_error", err.Error(), nil, nil), err
		}
	}
	if this.client.Process != nil {
		_ = this.client.Process.Continue()
	}
	this.client.MarkStandby()

	return this.successToolResult("cancel_run", "Current run cancelled. The session returned to standby.", map[string]interface{}{
		"cancelled": true,
		"standby":   true,
	}, nil, []string{"Breakpoints and attach target were preserved. Call run to relaunch, or adjust breakpoints before the next run."}), nil
}

func (this *Server) waitForStop(ctx context.Context, tool string, seq uint64, timeout time.Duration, extra map[string]interface{}) (map[string]interface{}, error) {
	info, err := this.client.WaitForStopAfterContext(ctx, seq, timeout)
	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return this.wrapToolError(tool, "request_cancelled", "the MCP client cancelled the request while waiting for a breakpoint", nil, nil), err
		}
		processData := this.processRuntimeData()
		processRunning, _ := processData["running"].(bool)
		processStatus, _ := processData["status"].(string)
		warnings := []string{"The wait window ended before a breakpoint was observed. If probes are still active, the app may still stop on a later breakpoint."}
		suggestions := []string{
			"Call wait_stop to continue waiting for the current run.",
			"Call cancel_run to stop waiting and return to attach standby while preserving the target and breakpoints.",
			"Call quit only if you want to fully reset the MCP debug session.",
		}
		message := "The wait window ended without a breakpoint hit. The target may still be running under eDBG."
		if processStatus == "exited" {
			message = "The wait window ended and the target process is no longer running."
			warnings = []string{"The target process no longer appears in the process list while eDBG was waiting for a breakpoint."}
			suggestions = []string{
				"Check whether the app crashed or exited normally.",
				"Call cancel_run to return to standby while preserving the current target and breakpoints.",
				"Call run again after you are ready to relaunch the app.",
			}
		}
		data := map[string]interface{}{
			"timed_out":      true,
			"still_running":  processRunning,
			"probe_running":  this.client.BrkManager != nil && this.client.BrkManager.Running,
			"timeout_ms":     timeout.Milliseconds(),
			"max_timeout_ms": maxContinueTimeout.Milliseconds(),
			"process":        processData,
			"recovery_tools": []string{"wait_stop", "cancel_run", "quit"},
		}
		for key, value := range extra {
			data[key] = value
		}
		return this.successToolResult(
			tool,
			message,
			data,
			warnings,
			suggestions,
		), nil
	}

	data := map[string]interface{}{
		"stop": this.stopInfoData(info),
	}
	for key, value := range extra {
		data[key] = value
	}
	return this.successToolResult(tool, "Breakpoint hit.", data, nil, nil), nil
}

func (this *Server) renderBreakpoints() string {
	var builder strings.Builder

	enabledCount := 0
	for _, brk := range this.client.BrkManager.BreakPoints {
		if !brk.Deleted && brk.Enable {
			enabledCount++
		}
	}

	fmt.Fprintf(&builder, "Breakpoints (%d enabled):\n", enabledCount)
	if len(this.client.BrkManager.BreakPoints) == 0 {
		builder.WriteString("(none)")
		return builder.String()
	}

	printed := 0
	for id, brk := range this.client.BrkManager.BreakPoints {
		if brk.Deleted {
			continue
		}
		status := "disabled"
		if brk.Enable {
			status = "enabled"
		}

		if brk.Hardware {
			fmt.Fprintf(&builder, "[%d] %s hardware @ 0x%x\n", id, status, brk.Addr.Absolute)
			printed++
			continue
		}

		virtualOffset := brk.Addr.Offset
		if converted, err := utils.ConvertFileOffsetToVirtualOffset(brk.Addr.LibInfo.LibPath, brk.Addr.Offset); err == nil {
			virtualOffset = converted
		}

		fmt.Fprintf(
			&builder,
			"[%d] %s %s+0x%x (file 0x%x)\n",
			id,
			status,
			brk.Addr.LibInfo.LibName,
			virtualOffset,
			brk.Addr.Offset,
		)
		printed++
	}

	if printed == 0 {
		builder.WriteString("(none)")
	}

	return strings.TrimSpace(builder.String())
}

func (this *Server) renderFileInfo() string {
	var builder strings.Builder

	if !this.client.HasTarget() {
		return "No active target. Call attach with package and library first."
	}

	fmt.Fprintf(&builder, "Library: %s\n", this.client.Library.LibName)
	fmt.Fprintf(&builder, "Resolved Path: %s\n", this.client.Library.RealFilePath)
	fmt.Fprintf(&builder, "Working Path: %s\n", this.client.Library.LibPath)
	if this.client.Library.NonElfOffset != 0 {
		fmt.Fprintf(&builder, "Library Offset In APK: 0x%x\n", this.client.Library.NonElfOffset)
	}
	if stat, err := os.Stat(this.client.Library.RealFilePath); err == nil {
		fmt.Fprintf(&builder, "File Size: 0x%x\n", stat.Size())
	}

	pid := this.client.Process.WorkPid
	if pid == 0 {
		this.client.Process.UpdatePidList()
		if len(this.client.Process.PidList) > 0 {
			pid = this.client.Process.PidList[0]
		}
	}

	if pid == 0 {
		builder.WriteString("Mapped Range: not available because the app is not stopped or running under eDBG yet.")
		return builder.String()
	}

	mapsContent, err := utils.ReadMapsByPid(pid)
	if err != nil {
		fmt.Fprintf(&builder, "Mapped Range: unavailable (%v)", err)
		return builder.String()
	}

	var minAddr uint64
	var maxAddr uint64
	found := false
	for _, line := range strings.Split(mapsContent, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[5] != this.client.Library.RealFilePath {
			continue
		}
		addressParts := strings.Split(fields[0], "-")
		if len(addressParts) != 2 {
			continue
		}
		start, errStart := strconv.ParseUint(addressParts[0], 16, 64)
		end, errEnd := strconv.ParseUint(addressParts[1], 16, 64)
		if errStart != nil || errEnd != nil {
			continue
		}
		if !found {
			minAddr = start
			found = true
		}
		if end > maxAddr {
			maxAddr = end
		}
	}

	if !found {
		builder.WriteString("Mapped Range: not currently mapped in the selected process.")
		return builder.String()
	}

	fmt.Fprintf(&builder, "Mapped Base: 0x%x\n", minAddr)
	fmt.Fprintf(&builder, "Mapped End: 0x%x\n", maxAddr)
	fmt.Fprintf(&builder, "Mapped Size: 0x%x", maxAddr-minAddr)

	return strings.TrimSpace(builder.String())
}

func (this *Server) renderStopInfo(info *cli.StopInfo) string {
	var builder strings.Builder

	fmt.Fprintf(&builder, "Breakpoint hit.\n")
	if info.BreakpointID >= 0 {
		fmt.Fprintf(&builder, "Breakpoint ID: %d\n", info.BreakpointID)
	}
	fmt.Fprintf(&builder, "PID/TID: %d/%d\n", info.Pid, info.Tid)
	fmt.Fprintf(&builder, "PC: 0x%x\n", info.PC)
	fmt.Fprintf(&builder, "LR: 0x%x\n", info.LR)
	fmt.Fprintf(&builder, "SP: 0x%x\n", info.SP)
	if info.Library != "" {
		fmt.Fprintf(&builder, "Location: %s+0x%x (file 0x%x)\n", info.Library, info.VirtualOffset, info.FileOffset)
	}

	return strings.TrimSpace(builder.String())
}

func (this *Server) mustJSON(value interface{}) string {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"ok":false,"message":"failed to encode JSON: %s"}`, cleanText(err.Error()))
	}
	return string(payload)
}

func cleanText(text string) string {
	text = ansiEscapePattern.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

func hexUint64(value uint64) string {
	return fmt.Sprintf("0x%x", value)
}

func (this *Server) successToolResult(tool string, message string, data map[string]interface{}, warnings []string, suggestions []string) map[string]interface{} {
	result := map[string]interface{}{
		"ok":      true,
		"tool":    tool,
		"message": message,
		"data":    data,
		"state":   this.stateSnapshot(),
	}
	if cleaned := cleanStringList(warnings); len(cleaned) > 0 {
		result["warnings"] = cleaned
	}
	if cleaned := cleanStringList(suggestions); len(cleaned) > 0 {
		result["suggestions"] = cleaned
	}
	return result
}

func (this *Server) errorToolResult(tool string, code string, message string, warnings []string, data map[string]interface{}, suggestions []string) map[string]interface{} {
	result := map[string]interface{}{
		"ok":      false,
		"tool":    tool,
		"message": message,
		"state":   this.stateSnapshot(),
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
	if data != nil {
		result["data"] = data
	}
	if cleaned := cleanStringList(warnings); len(cleaned) > 0 {
		result["warnings"] = cleaned
	}
	if cleaned := cleanStringList(suggestions); len(cleaned) > 0 {
		result["suggestions"] = cleaned
	}
	return result
}

func (this *Server) wrapToolError(tool string, code string, message string, warnings []string, suggestions []string) map[string]interface{} {
	return this.errorToolResult(tool, code, cleanText(message), warnings, nil, suggestions)
}

func cleanStringList(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = cleanText(value)
		if value == "" {
			continue
		}
		cleaned = append(cleaned, value)
	}
	return cleaned
}

func (this *Server) processRuntimeData() map[string]interface{} {
	if !this.client.HasTarget() || this.client.Process == nil {
		return map[string]interface{}{
			"available": false,
		}
	}

	process := this.client.Process
	process.UpdatePidList()

	pidList := make([]uint32, 0, len(process.PidList))
	for _, pid := range process.PidList {
		pidList = append(pidList, pid)
	}

	stoppedPIDs := make([]uint32, 0, len(process.StoppedPid))
	seenStopped := make(map[uint32]bool)
	for _, pid := range process.StoppedPid {
		if seenStopped[pid] {
			continue
		}
		seenStopped[pid] = true
		stoppedPIDs = append(stoppedPIDs, pid)
	}

	running := len(pidList) > 0
	probeRunning := this.client.BrkManager != nil && this.client.BrkManager.Running

	status := "standby"
	switch {
	case this.client.IsStopped() && running:
		status = "stopped"
	case this.client.IsStopped() && !running:
		status = "exited"
	case this.client.HasRunIssued() && running:
		status = "running"
	case this.client.HasRunIssued() && !running:
		status = "exited"
	case running:
		status = "running_unmanaged"
	}

	unexpectedExit := status == "exited" && this.client.HasRunIssued()

	data := map[string]interface{}{
		"available":        true,
		"package":          process.PackageName,
		"status":           status,
		"running":          running,
		"stopped":          this.client.IsStopped(),
		"run_issued":       this.client.HasRunIssued(),
		"probe_running":    probeRunning,
		"pid_list":         pidList,
		"pid_count":        len(pidList),
		"stopped_pid_list": stoppedPIDs,
		"work_pid":         process.WorkPid,
		"work_tid":         process.WorkTid,
		"unexpected_exit":  unexpectedExit,
	}
	if len(pidList) > 0 {
		data["primary_pid"] = pidList[0]
	}
	if unexpectedExit {
		data["exit_hint"] = "The target process is no longer running even though eDBG was waiting for a breakpoint. The app may have exited or crashed."
	}
	return data
}

func (this *Server) stateSnapshot() map[string]interface{} {
	phase := "idle"
	processData := this.processRuntimeData()
	if this.client.HasTarget() {
		switch {
		case this.client.IsStopped():
			phase = "stopped"
		case this.client.HasRunIssued():
			phase = "running"
		default:
			phase = "attached"
		}
	}

	state := map[string]interface{}{
		"phase":                    phase,
		"target_selected":          this.client.HasTarget(),
		"stopped":                  this.client.IsStopped(),
		"run_issued":               this.client.HasRunIssued(),
		"probe_running":            this.client.BrkManager != nil && this.client.BrkManager.Running,
		"breakpoint_count":         len(this.breakpointListData()),
		"enabled_breakpoint_count": this.enabledBreakpointCount(),
	}
	if this.client.HasTarget() {
		state["package"] = this.client.Process.PackageName
		state["library"] = this.client.Library.LibName
		state["process"] = processData
		if processStatus, ok := processData["status"].(string); ok {
			state["process_status"] = processStatus
			state["process_running"] = processData["running"]
			state["unexpected_exit"] = processData["unexpected_exit"]
		}
	}
	if lastStop := this.client.LastStopInfo(); lastStop != nil {
		state["last_stop"] = this.stopInfoData(lastStop)
	}
	return state
}

func (this *Server) statusData() map[string]interface{} {
	data := map[string]interface{}{
		"protocol_version":    defaultProtocolVersion,
		"guide_resource_uri":  guideResourceURI,
		"status_resource_uri": statusResourceURI,
		"probe_running":       this.client.BrkManager != nil && this.client.BrkManager.Running,
		"process":             this.processRuntimeData(),
		"breakpoints":         this.breakpointListData(),
	}
	if lastStop := this.client.LastStopInfo(); lastStop != nil {
		data["last_stop"] = this.stopInfoData(lastStop)
	}
	return data
}

func (this *Server) stopInfoData(info *cli.StopInfo) map[string]interface{} {
	if info == nil {
		return nil
	}
	data := map[string]interface{}{
		"sequence": info.Sequence,
		"pid":      info.Pid,
		"tid":      info.Tid,
		"pc":       hexUint64(info.PC),
		"lr":       hexUint64(info.LR),
		"sp":       hexUint64(info.SP),
	}
	if info.BreakpointID >= 0 {
		data["breakpoint_id"] = info.BreakpointID
	}
	if info.Library != "" {
		data["library"] = info.Library
		data["file_offset"] = hexUint64(info.FileOffset)
		data["virtual_offset"] = hexUint64(info.VirtualOffset)
	}
	data["text"] = this.renderStopInfo(info)
	return data
}

func (this *Server) breakpointData(id int, brk *module.BreakPoint) map[string]interface{} {
	if brk == nil || brk.Addr == nil || brk.Addr.LibInfo == nil {
		return nil
	}
	data := map[string]interface{}{
		"id":          id,
		"enabled":     brk.Enable,
		"deleted":     brk.Deleted,
		"hardware":    brk.Hardware,
		"pid":         brk.Pid,
		"type":        brk.Type,
		"library":     brk.Addr.LibInfo.LibName,
		"file_offset": hexUint64(brk.Addr.Offset),
	}
	if brk.Addr.LibInfo.LibPath != "" {
		data["library_path"] = brk.Addr.LibInfo.LibPath
	}
	virtualOffset := brk.Addr.Offset
	if brk.Addr.LibInfo.LibPath != "" {
		if converted, err := utils.ConvertFileOffsetToVirtualOffset(brk.Addr.LibInfo.LibPath, brk.Addr.Offset); err == nil {
			virtualOffset = converted
		}
	}
	data["virtual_offset"] = hexUint64(virtualOffset)
	if brk.Addr.Absolute != 0 {
		data["absolute_address"] = hexUint64(brk.Addr.Absolute)
	}
	return data
}

func (this *Server) breakpointListData() []map[string]interface{} {
	breakpoints := make([]map[string]interface{}, 0, len(this.client.BrkManager.BreakPoints))
	for id, brk := range this.client.BrkManager.BreakPoints {
		if brk == nil || brk.Deleted {
			continue
		}
		breakpoints = append(breakpoints, this.breakpointData(id, brk))
	}
	return breakpoints
}

func (this *Server) findBreakpoint(address *controller.Address) (int, *module.BreakPoint) {
	for id, brk := range this.client.BrkManager.BreakPoints {
		if brk == nil || brk.Deleted {
			continue
		}
		if controller.Equals(brk.Addr, address) {
			return id, brk
		}
	}
	return -1, nil
}

func (this *Server) changeBreakpointState(id int, enabled bool) error {
	if id < 0 || id >= len(this.client.BrkManager.BreakPoints) {
		return fmt.Errorf("invalid breakpoint id %d", id)
	}
	brk := this.client.BrkManager.BreakPoints[id]
	if brk == nil || brk.Deleted {
		return fmt.Errorf("breakpoint %d does not exist", id)
	}
	this.client.BrkManager.ChangeBreakPoint(id, enabled)
	return nil
}

func (this *Server) deleteBreakpoint(id int) error {
	if id < 0 || id >= len(this.client.BrkManager.BreakPoints) {
		return fmt.Errorf("invalid breakpoint id %d", id)
	}
	brk := this.client.BrkManager.BreakPoints[id]
	if brk == nil || brk.Deleted {
		return fmt.Errorf("breakpoint %d does not exist", id)
	}
	this.client.BrkManager.DeleteBreakPoint(id)
	return nil
}

func (this *Server) fileInfoData() map[string]interface{} {
	data := map[string]interface{}{
		"library":       this.client.Library.LibName,
		"resolved_path": this.client.Library.RealFilePath,
		"working_path":  this.client.Library.LibPath,
	}
	if this.client.Library.NonElfOffset != 0 {
		data["library_offset_in_apk"] = hexUint64(this.client.Library.NonElfOffset)
	}
	if stat, err := os.Stat(this.client.Library.RealFilePath); err == nil {
		data["file_size"] = hexUint64(uint64(stat.Size()))
	}

	pid := this.client.Process.WorkPid
	if pid == 0 {
		this.client.Process.UpdatePidList()
		if len(this.client.Process.PidList) > 0 {
			pid = this.client.Process.PidList[0]
		}
	}
	if pid == 0 {
		data["mapped_range"] = map[string]interface{}{
			"available": false,
			"reason":    "app is not stopped or running under eDBG yet",
		}
		return data
	}

	mapsContent, err := utils.ReadMapsByPid(pid)
	if err != nil {
		data["mapped_range"] = map[string]interface{}{
			"available": false,
			"reason":    cleanText(err.Error()),
		}
		return data
	}

	var minAddr uint64
	var maxAddr uint64
	found := false
	for _, line := range strings.Split(mapsContent, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 || fields[5] != this.client.Library.RealFilePath {
			continue
		}
		addressParts := strings.Split(fields[0], "-")
		if len(addressParts) != 2 {
			continue
		}
		start, errStart := strconv.ParseUint(addressParts[0], 16, 64)
		end, errEnd := strconv.ParseUint(addressParts[1], 16, 64)
		if errStart != nil || errEnd != nil {
			continue
		}
		if !found {
			minAddr = start
			found = true
		}
		if end > maxAddr {
			maxAddr = end
		}
	}
	if !found {
		data["mapped_range"] = map[string]interface{}{
			"available": false,
			"reason":    "selected library is not currently mapped in the selected process",
		}
		return data
	}

	data["mapped_range"] = map[string]interface{}{
		"available": true,
		"pid":       pid,
		"base":      hexUint64(minAddr),
		"end":       hexUint64(maxAddr),
		"size":      hexUint64(maxAddr - minAddr),
	}
	return data
}

func (this *Server) registerData() []map[string]interface{} {
	if this.client.Process == nil || this.client.Process.Context == nil {
		return []map[string]interface{}{}
	}
	registers := make([]map[string]interface{}, 0, len(this.client.Process.Context.Regs)+4)
	for idx, value := range this.client.Process.Context.Regs {
		entry := map[string]interface{}{
			"name":  fmt.Sprintf("x%d", idx),
			"value": hexUint64(value),
		}
		if value > 0x1000000000 && this.client.Process.WorkPid != 0 {
			if ok, deref := utils.TryRead(this.client.Process.WorkPid, uintptr(value)); ok {
				entry["dereference"] = deref
			}
		}
		registers = append(registers, entry)
	}
	registers = append(registers,
		map[string]interface{}{"name": "lr", "value": hexUint64(this.client.Process.Context.LR)},
		map[string]interface{}{"name": "sp", "value": hexUint64(this.client.Process.Context.SP)},
		map[string]interface{}{"name": "pc", "value": hexUint64(this.client.Process.Context.PC)},
		map[string]interface{}{"name": "pstate", "value": hexUint64(this.client.Process.Context.Pstate)},
	)
	return registers
}

func (this *Server) threadData() (map[string]interface{}, error) {
	threads, err := this.client.Process.GetCurrentThreads()
	if err != nil {
		return nil, err
	}
	threadItems := make([]map[string]interface{}, 0, len(threads))
	for idx, thread := range threads {
		threadItems = append(threadItems, map[string]interface{}{
			"index":      idx,
			"tid":        thread.Tid,
			"name":       thread.Name,
			"is_current": thread.Tid == this.client.Process.WorkTid,
		})
	}

	filters := make([]map[string]interface{}, 0, len(this.client.Config.ThreadFilters))
	for idx, filter := range this.client.Config.ThreadFilters {
		if filter == nil || filter.Thread == nil {
			continue
		}
		entry := map[string]interface{}{
			"index":   idx,
			"enabled": filter.Enable,
		}
		if filter.Thread.Tid != 0 {
			entry["tid"] = filter.Thread.Tid
		}
		if filter.Thread.Name != "" {
			entry["name"] = filter.Thread.Name
		}
		filters = append(filters, entry)
	}

	return map[string]interface{}{
		"current_tid": this.client.Process.WorkTid,
		"threads":     threadItems,
		"filters":     filters,
	}, nil
}

func (this *Server) examineData(addressArg string, lengthOrType string) (map[string]interface{}, error) {
	address, err := this.parseRuntimeAddress(addressArg)
	if err != nil {
		return nil, err
	}

	length := 16
	format := "hexdump"
	if lengthOrType != "" {
		if value, err := utils.GetExprValue(lengthOrType, this.client.Process.Context); err == nil {
			if value > 0x100000 {
				return nil, fmt.Errorf("invalid length")
			}
			length = int(value)
		} else {
			switch lengthOrType {
			case "ptr":
				format = "ptr"
				length = 8
			case "str":
				format = "str"
			case "int":
				format = "int"
				length = 4
			default:
				return nil, fmt.Errorf("invalid type or length: %v", err)
			}
		}
	}

	data := map[string]interface{}{
		"address":        hexUint64(address),
		"requested":      addressArg,
		"length_or_type": lengthOrType,
		"format":         format,
	}

	switch format {
	case "str":
		builder := &strings.Builder{}
		remoteAddr := uintptr(address)
		for i := 0; i < 4096; i++ {
			buf := make([]byte, 1)
			n, err := utils.ReadProcessMemory(this.client.Process.WorkPid, remoteAddr, buf)
			if n < 1 || err != nil || !strconv.IsPrint(rune(buf[0])) {
				break
			}
			builder.WriteByte(buf[0])
			remoteAddr++
		}
		data["value"] = builder.String()
		return data, nil
	case "ptr":
		buf := make([]byte, length)
		n, err := utils.ReadProcessMemory(this.client.Process.WorkPid, uintptr(address), buf)
		if err != nil {
			return nil, err
		}
		if n < length {
			return nil, fmt.Errorf("short read: expected %d bytes, got %d", length, n)
		}
		value := binary.LittleEndian.Uint64(buf)
		data["value"] = hexUint64(value)
		data["bytes"] = hex.EncodeToString(buf[:n])
		return data, nil
	case "int":
		buf := make([]byte, length)
		n, err := utils.ReadProcessMemory(this.client.Process.WorkPid, uintptr(address), buf)
		if err != nil {
			return nil, err
		}
		if n < length {
			return nil, fmt.Errorf("short read: expected %d bytes, got %d", length, n)
		}
		data["value"] = binary.LittleEndian.Uint32(buf)
		data["bytes"] = hex.EncodeToString(buf[:n])
		return data, nil
	default:
		buf := make([]byte, length)
		n, err := utils.ReadProcessMemory(this.client.Process.WorkPid, uintptr(address), buf)
		if err != nil {
			return nil, err
		}
		data["bytes"] = hex.EncodeToString(buf[:n])
		data["length"] = n
		data["text"] = strings.TrimRight(utils.HexDump(address, buf, n), "\n")
		return data, nil
	}
}

func (this *Server) disassemblyData(addressArg string, count int) (map[string]interface{}, error) {
	address := this.client.Process.Context.PC
	var err error
	if strings.TrimSpace(addressArg) != "" {
		address, err = this.parseRuntimeAddress(addressArg)
		if err != nil {
			return nil, err
		}
	}
	if count <= 0 {
		count = 10
	}

	codeBuf := make([]byte, count*4)
	n, err := utils.ReadProcessMemory(this.client.Process.WorkPid, uintptr(address), codeBuf)
	if err != nil {
		return nil, err
	}

	instructions := make([]map[string]interface{}, 0, n/4)
	for i := 0; i+4 <= n; i += 4 {
		currentAddress := address + uint64(i)
		entry := map[string]interface{}{
			"address":    hexUint64(currentAddress),
			"is_current": i == 0,
		}

		if addInfo, parseErr := this.client.Process.ParseAddress(currentAddress); parseErr == nil && addInfo != nil && addInfo.LibInfo != nil {
			entry["library"] = addInfo.LibInfo.LibName
			entry["file_offset"] = hexUint64(addInfo.Offset)
			virtualOffset := addInfo.Offset
			if addInfo.LibInfo.LibPath != "" {
				if converted, convErr := utils.ConvertFileOffsetToVirtualOffset(addInfo.LibInfo.LibPath, addInfo.Offset); convErr == nil {
					virtualOffset = converted
				}
			}
			entry["virtual_offset"] = hexUint64(virtualOffset)
		}

		disasm, disErr := utils.DisASM(codeBuf[i:i+4], currentAddress, this.client.Process)
		if disErr != nil {
			entry["text"] = "(disassemble failed)"
		} else {
			entry["text"] = disasm
			if space := strings.Index(disasm, " "); space >= 0 {
				entry["mnemonic"] = disasm[:space]
				entry["operands"] = strings.TrimSpace(disasm[space+1:])
			} else {
				entry["mnemonic"] = disasm
				entry["operands"] = ""
			}
		}
		instructions = append(instructions, entry)
	}

	text := cleanText(this.client.CaptureOutput(func() {
		this.client.PrintDisassembleInfo(address, count)
	}))
	return map[string]interface{}{
		"requested_address": addressArg,
		"address":           hexUint64(address),
		"count":             count,
		"instructions":      instructions,
		"text":              text,
	}, nil
}

func (this *Server) backtraceData(mode string, text string) map[string]interface{} {
	text = cleanText(text)
	lines := []string{}
	if text != "" {
		lines = strings.Split(text, "\n")
	}
	return map[string]interface{}{
		"mode":  mode,
		"text":  text,
		"lines": lines,
	}
}

func (this *Server) writeMemoryData(addressArg string, hexValue string) (map[string]interface{}, error) {
	address, err := this.parseRuntimeAddress(addressArg)
	if err != nil {
		return nil, err
	}
	data, err := utils.HexStringToBytes(hexValue)
	if err != nil {
		return nil, err
	}
	n, err := utils.WriteProcessMemory(this.client.Process.WorkPid, uintptr(address), data)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"address":       hexUint64(address),
		"requested":     addressArg,
		"bytes_written": n,
		"bytes":         hex.EncodeToString(data[:n]),
		"text":          strings.TrimRight(utils.HexDump(address, data, n), "\n"),
	}, nil
}

func (this *Server) dumpData(addressArg string, lengthArg string, fileName string) (map[string]interface{}, error) {
	address, err := this.parseRuntimeAddress(addressArg)
	if err != nil {
		return nil, err
	}
	length, err := utils.GetExprValue(lengthArg, this.client.Process.Context)
	if err != nil {
		return nil, err
	}
	data, err := utils.ReadProcessMemoryRobust(this.client.Process.WorkPid, uintptr(address), int(length))
	if err != nil {
		return nil, err
	}
	if err := utils.WriteBytesToFile(fileName, data); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"address":       hexUint64(address),
		"requested":     addressArg,
		"length":        hexUint64(length),
		"file":          fileName,
		"bytes_written": len(data),
	}, nil
}

func (this *Server) forceStopPackage() (string, error) {
	if this.client.Process == nil || this.client.Process.PackageName == "" {
		return "", fmt.Errorf("no package selected")
	}
	return utils.RunCommand("am", "force-stop", this.client.Process.PackageName)
}

func (this *Server) parseAmStartOutput(output string) map[string]interface{} {
	result := map[string]interface{}{
		"raw_text": cleanText(output),
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "Status:"):
			result["status"] = strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
		case strings.HasPrefix(line, "Activity:"):
			result["activity"] = strings.TrimSpace(strings.TrimPrefix(line, "Activity:"))
		case strings.HasPrefix(line, "ThisTime:"):
			result["this_time_ms"] = strings.TrimSpace(strings.TrimPrefix(line, "ThisTime:"))
		case strings.HasPrefix(line, "TotalTime:"):
			result["total_time_ms"] = strings.TrimSpace(strings.TrimPrefix(line, "TotalTime:"))
		case strings.HasPrefix(line, "WaitTime:"):
			result["wait_time_ms"] = strings.TrimSpace(strings.TrimPrefix(line, "WaitTime:"))
		case strings.HasPrefix(line, "Complete"):
			result["complete"] = true
		case strings.HasPrefix(line, "Warning:"):
			result["warning"] = strings.TrimSpace(strings.TrimPrefix(line, "Warning:"))
		}
	}
	return result
}

func (this *Server) resolveActivityName(activityArg string, amOutput string) string {
	parsed := this.parseAmStartOutput(amOutput)
	if activity, ok := parsed["activity"].(string); ok && activity != "" {
		return activity
	}
	if activityArg == "" {
		return ""
	}
	if strings.Contains(activityArg, "/") {
		return activityArg
	}
	return this.client.Process.PackageName + "/" + activityArg
}

func optionalIntArgValue(args map[string]interface{}, key string, fallback int) int {
	if value, ok := optionalIntArg(args, key); ok {
		return value
	}
	return fallback
}

func effectiveWaitTimeout(args map[string]interface{}) time.Duration {
	timeout := defaultContinueTimeout
	if timeoutMS, ok := optionalIntArg(args, "timeout_ms"); ok && timeoutMS > 0 {
		timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	if timeout > maxContinueTimeout {
		timeout = maxContinueTimeout
	}
	return timeout
}

func (this *Server) parseRuntimeAddress(arg string) (uint64, error) {
	address, err := this.client.ParseUserAddressToAbsolute(arg)
	if err == nil {
		return address, nil
	}
	value, exprErr := utils.GetExprValue(arg, this.client.Process.Context)
	if exprErr == nil {
		return value, nil
	}
	return 0, err
}

func (this *Server) combineOutput(output string, fallback string) string {
	output = strings.TrimSpace(output)
	fallback = strings.TrimSpace(fallback)
	switch {
	case output == "":
		return fallback
	case fallback == "":
		return output
	default:
		return output + "\n\n" + fallback
	}
}

func (this *Server) requireStopped(toolName string) error {
	if err := this.requireTarget(toolName); err != nil {
		return err
	}
	if this.client.IsStopped() {
		return nil
	}
	return fmt.Errorf("%s is only available after a breakpoint has been hit", toolName)
}

func (this *Server) requireTarget(toolName string) error {
	if this.client.HasTarget() {
		return nil
	}
	return fmt.Errorf("%s requires an active target. Call attach with package and library first", toolName)
}

func (this *Server) hasEnabledBreakpoints() bool {
	for _, brk := range this.client.BrkManager.BreakPoints {
		if !brk.Deleted && brk.Enable {
			return true
		}
	}
	return false
}

func (this *Server) enabledBreakpointCount() int {
	count := 0
	for _, brk := range this.client.BrkManager.BreakPoints {
		if !brk.Deleted && brk.Enable {
			count++
		}
	}
	return count
}

func (this *Server) preAttachHint(toolName string) string {
	return fmt.Sprintf(
		"%s is unavailable before attach.\n\nFirst call status to confirm connectivity if needed, then call attach with package and library.\nRead resource %s for the full workflow and tool restrictions.",
		toolName,
		guideResourceURI,
	)
}

func (this *Server) launchApp(activity string) (string, error) {
	packageName := this.client.Process.PackageName
	if activity != "" {
		component := activity
		if !strings.Contains(component, "/") {
			component = packageName + "/" + component
		}
		return utils.RunCommand("am", "start", "-n", component)
	}

	resolved, err := utils.RunCommand("cmd", "package", "resolve-activity", "--brief", packageName)
	if err == nil {
		for _, line := range strings.Split(resolved, "\n") {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "/") {
				return utils.RunCommand("am", "start", "-n", line)
			}
		}
	}

	return "", fmt.Errorf("failed to resolve launcher activity for %s; specify activity explicitly", packageName)
}

func (this *Server) ensureTarget(packageName string, libName string) error {
	if this.client.HasTarget() {
		samePackage := packageName == "" || packageName == this.client.Process.PackageName
		sameLibrary := libName == "" || libName == this.client.Library.LibName
		if samePackage && sameLibrary {
			return nil
		}
		if packageName == "" || libName == "" {
			return fmt.Errorf("switching targets requires both package and library")
		}
		if err := this.resetTarget(); err != nil {
			return err
		}
	}

	if packageName == "" || libName == "" {
		return fmt.Errorf("attach requires package and library when no active target is selected")
	}

	process, err := controller.CreateProcess(packageName)
	if err != nil {
		return fmt.Errorf("create process error: %w", err)
	}
	library, err := controller.CreateLibrary(process, libName)
	if err != nil {
		return fmt.Errorf("create library error: %w", err)
	}

	this.client.SetTarget(process, library)
	return nil
}

func (this *Server) resetTarget() error {
	if this.client.BrkManager != nil && this.client.BrkManager.Running {
		if err := this.client.BrkManager.Stop(); err != nil {
			return err
		}
	}
	if this.client.Process != nil {
		_ = this.client.Process.Continue()
	}
	this.client.ClearTarget()
	return nil
}

func (this *Server) toolDefinitions() []map[string]interface{} {
	return []map[string]interface{}{
		toolDefinition("status", "返回当前 MCP 连通性、attach 状态、运行状态，以及下一步建议。这个工具始终可用。", emptySchema()),
		toolDefinition("attach", "选中当前调试目标 package 和 library，不会启动 app。切换目标时也使用这个工具。", map[string]interface{}{
			"type":     "object",
			"required": []string{"package", "library"},
			"properties": map[string]interface{}{
				"package": map[string]interface{}{
					"type":        "string",
					"description": "包名。切换目标时必填。",
				},
				"library": map[string]interface{}{
					"type":        "string",
					"description": "库名或绝对路径。切换目标时必填。",
				},
				"package_name": map[string]interface{}{
					"type":        "string",
					"description": "package 的别名。",
				},
				"lib": map[string]interface{}{
					"type":        "string",
					"description": "library 的别名。",
				},
			},
		}),
		toolDefinition("break", "按虚拟偏移设置断点，等价于 eDBG 的 vbreak。需要先用 attach 选中 package 和 library。", map[string]interface{}{
			"type":     "object",
			"required": []string{"address"},
			"properties": map[string]interface{}{
				"address": map[string]interface{}{
					"type":        "string",
					"description": "虚拟偏移、绝对地址或 library.so+0xOFFSET。",
				},
			},
		}),
		toolDefinition("enable_breakpoint", "启用指定断点。调用前需要先 attach。", intIDSchema()),
		toolDefinition("disable_breakpoint", "禁用指定断点。调用前需要先 attach。", intIDSchema()),
		toolDefinition("delete_breakpoint", "删除指定断点。调用前需要先 attach。", intIDSchema()),
		toolDefinition("info_break", "查看当前断点列表。调用前需要先 attach。", emptySchema()),
		toolDefinition("info_file", "查看当前目标库文件信息；attach 后在待命态也可用。", emptySchema()),
		toolDefinition("info_register", "查看当前寄存器信息，仅在 attach 且断点命中后可用。", emptySchema()),
		toolDefinition("info_thread", "查看当前线程信息，仅在 attach 且断点命中后可用。", emptySchema()),
		toolDefinition("run", "先执行一次 continue 进入等待状态，再启动当前 attach 的目标 app，并等待首个断点命中。为了避免失控，至少需要先设置一个启用的断点。", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"activity": map[string]interface{}{
					"type":        "string",
					"description": "可选。显式指定 Activity；不传时自动解析 launcher activity 后用 am start 启动。",
				},
				"timeout_ms": map[string]interface{}{
					"type":        "integer",
					"description": "可选。run 内部等待首次断点命中的最长时间；服务端当前默认且最大都为 90000 毫秒，以确保在 MCP 客户端超时之前返回结果。",
				},
			},
		}),
		toolDefinition("continue", "继续运行并阻塞等待直到下一个断点命中。需要先 attach；在首次 run 之前不可用。", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"timeout_ms": map[string]interface{}{
					"type":        "integer",
					"description": "可选。最长等待时间；服务端当前默认且最大都为 90000 毫秒，以确保在 MCP 客户端超时之前返回结果。",
				},
			},
		}),
		toolDefinition("wait_stop", "当目标已经在运行中时，继续等待当前这次运行命中断点，不会再次发送 continue。适合在 continue/run 超时后继续接管当前会话。", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"timeout_ms": map[string]interface{}{
					"type":        "integer",
					"description": "可选。最长等待时间；服务端当前默认且最大都为 90000 毫秒，以确保在 MCP 客户端超时之前返回结果。",
				},
			},
		}),
		toolDefinition("cancel_run", "取消当前正在等待的运行，停止探针并回到 attach 后的待命状态，同时保留当前 target 和断点。", emptySchema()),
		toolDefinition("examine", "读取内存，仅在 attach 且断点命中后可用。", map[string]interface{}{
			"type":     "object",
			"required": []string{"address"},
			"properties": map[string]interface{}{
				"address": map[string]interface{}{"type": "string"},
				"length_or_type": map[string]interface{}{
					"type":        "string",
					"description": "可选。长度表达式，或 ptr/str/int。",
				},
			},
		}),
		toolDefinition("list", "反汇编当前或指定地址，仅在 attach 且断点命中后可用。", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"address": map[string]interface{}{"type": "string"},
				"count":   map[string]interface{}{"type": "integer"},
			},
		}),
		toolDefinition("backtrace", "查看调用栈，仅在 attach 且断点命中后可用。", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"mode": map[string]interface{}{
					"type":        "string",
					"description": "unwind 或 fp，默认 unwind。",
				},
			},
		}),
		toolDefinition("thread", "管理线程过滤器，仅在 attach 且断点命中后可用。", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"action": map[string]interface{}{
					"type":        "string",
					"description": "list/add/name/del/all。",
				},
				"value": map[string]interface{}{
					"type":        "string",
					"description": "动作需要的值，例如线程列表里的索引或线程名。",
				},
			},
		}),
		toolDefinition("set_symbol", "为地址设置符号名，仅在 attach 且断点命中后可用。", map[string]interface{}{
			"type":     "object",
			"required": []string{"address", "name"},
			"properties": map[string]interface{}{
				"address": map[string]interface{}{"type": "string"},
				"name":    map[string]interface{}{"type": "string"},
			},
		}),
		toolDefinition("write_memory", "向内存写入 hex 数据，仅在 attach 且断点命中后可用。", map[string]interface{}{
			"type":     "object",
			"required": []string{"address", "hex"},
			"properties": map[string]interface{}{
				"address": map[string]interface{}{"type": "string"},
				"hex":     map[string]interface{}{"type": "string"},
			},
		}),
		toolDefinition("dump", "导出内存到文件，仅在 attach 且断点命中后可用。", map[string]interface{}{
			"type":     "object",
			"required": []string{"address", "length", "file"},
			"properties": map[string]interface{}{
				"address": map[string]interface{}{"type": "string"},
				"length":  map[string]interface{}{"type": "string"},
				"file":    map[string]interface{}{"type": "string"},
			},
		}),
		toolDefinition("quit", "清空当前 MCP 调试上下文并回到初始待命状态，不会退出 MCP server。重置后需要重新 attach。", emptySchema()),
	}
}

func (this *Server) resourceDefinitions() []map[string]interface{} {
	return []map[string]interface{}{
		{
			"uri":         guideResourceURI,
			"name":        "MCP Usage Guide",
			"description": "eDBG MCP workflow, tool restrictions, and the required attach/run/continue sequence.",
			"mimeType":    "text/plain",
		},
		{
			"uri":         statusResourceURI,
			"name":        "Runtime Status",
			"description": "Current connection state, selected target, breakpoint summary, and next-step hints.",
			"mimeType":    "text/plain",
		},
	}
}

func (this *Server) renderGuideResource() string {
	return strings.TrimSpace(`eDBG MCP guide

This server exposes all tool capabilities up front so the agent can plan, but there are workflow restrictions:

1. First use status to confirm connectivity and inspect the current state.
2. Then use attach with package and library to select the debug target.
3. Before attach succeeds, every other debug tool will return a guidance message instead of executing.
4. After attach, use break to add virtual-offset breakpoints.
5. Use run to arm the probes, launch the app with am start, and wait for the first breakpoint hit. run does not accept package or library; use attach for target selection.
6. After the first stop, use continue to resume execution and wait for later breakpoint hits.
7. If run or continue times out, the target may still be running under eDBG. In that case, use wait_stop to keep waiting for the current run, or cancel_run to return to standby without losing attach state and breakpoints.
8. Stopped-only tools become available only after a breakpoint has been hit: info_register, info_thread, examine, list, backtrace, thread, set_symbol, write_memory, dump.
9. quit resets the current debug context but keeps the MCP server running.

Tool policy summary:
- Always safe: status, attach
- Requires attach: break, enable_breakpoint, disable_breakpoint, delete_breakpoint, info_break, info_file, run, continue, wait_stop, cancel_run, quit
- Requires attach and a breakpoint hit: info_register, info_thread, examine, list, backtrace, thread, set_symbol, write_memory, dump

The break tool uses virtual offsets, equivalent to eDBG vbreak.

Process status meanings:
- standby: target is attached, but no run is currently in progress and the target is not stopped on a breakpoint.
- running: target process is alive and eDBG is currently waiting for a breakpoint.
- stopped: target is currently stopped on a breakpoint and stopped-only tools are available.
- running_unmanaged: target process is alive, but eDBG is not currently waiting for a breakpoint.
- exited: target process is no longer running. If unexpected_exit is true, the app likely exited or crashed while eDBG was waiting for a breakpoint.`)
}

func (this *Server) setCommonHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Accept, Origin, "+sessionHeaderName)
}

func (this *Server) getSession(sessionID string) (*session, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("missing MCP session id")
	}

	this.mu.Lock()
	sess, ok := this.sessions[sessionID]
	if !ok {
		// Some MCP clients keep a session id cached across server restarts and
		// don't always perform a fresh initialize before the next tool call.
		// Recreate a compatible session so the client can recover automatically.
		sess = &session{
			id:              sessionID,
			protocolVersion: defaultProtocolVersion,
			initialized:     true,
		}
		this.sessions[sessionID] = sess
	}
	this.mu.Unlock()
	return sess, nil
}

func (this *Server) writeRPCResult(w http.ResponseWriter, id json.RawMessage, result interface{}) {
	response := rpcResponse{
		JSONRPC: "2.0",
		ID:      parseID(id),
		Result:  result,
	}

	_ = json.NewEncoder(w).Encode(response)
}

func (this *Server) writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	response := rpcResponse{
		JSONRPC: "2.0",
		ID:      parseID(id),
		Error: &rpcError{
			Code:    code,
			Message: message,
		},
	}

	_ = json.NewEncoder(w).Encode(response)
}

func toolDefinition(name string, description string, schema map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"name":        name,
		"description": description,
		"inputSchema": schema,
	}
}

func emptySchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func intIDSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":     "object",
		"required": []string{"id"},
		"properties": map[string]interface{}{
			"id": map[string]interface{}{
				"type": "integer",
			},
		},
	}
}

func parseID(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return nil
	}

	var value interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	return value
}

func requiredStringArg(args map[string]interface{}, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	str, ok := value.(string)
	if !ok || strings.TrimSpace(str) == "" {
		return "", fmt.Errorf("argument %q must be a non-empty string", key)
	}
	return strings.TrimSpace(str), nil
}

func optionalStringArg(args map[string]interface{}, key string, fallback string) string {
	value, ok := args[key]
	if !ok {
		return fallback
	}
	str, ok := value.(string)
	if !ok {
		return fallback
	}
	str = strings.TrimSpace(str)
	if str == "" {
		return fallback
	}
	return str
}

func requiredIntArg(args map[string]interface{}, key string) (int, error) {
	value, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("missing required argument %q", key)
	}
	switch typed := value.(type) {
	case float64:
		return int(typed), nil
	case int:
		return typed, nil
	default:
		return 0, fmt.Errorf("argument %q must be an integer", key)
	}
}

func optionalIntArg(args map[string]interface{}, key string) (int, bool) {
	value, ok := args[key]
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	default:
		return 0, false
	}
}

func chooseProtocolVersion(requested string) string {
	switch requested {
	case "", defaultProtocolVersion:
		return defaultProtocolVersion
	default:
		return defaultProtocolVersion
	}
}

func randomSessionID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func validateOrigin(r *http.Request) error {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" || origin == "null" {
		return nil
	}

	parsed, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("invalid Origin header")
	}

	host := parsed.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}

	return fmt.Errorf("Origin %q is not allowed", origin)
}
