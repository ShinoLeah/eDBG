package cli

import (
	"bytes"
	"context"
	"eDBG/controller"
	"eDBG/utils"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

type StopInfo struct {
	Sequence      uint64
	Pid           uint32
	Tid           uint32
	BreakpointID  int
	Library       string
	FileOffset    uint64
	VirtualOffset uint64
	PC            uint64
	LR            uint64
	SP            uint64
}

type MCPState struct {
	mu             sync.Mutex
	mode           bool
	suppressOutput bool
	runIssued      bool
	stopped        bool
	stopSeq        uint64
	lastStop       *StopInfo
	notifyCh       chan struct{}
}

var captureStdoutMu sync.Mutex

func NewMCPState() *MCPState {
	return &MCPState{
		notifyCh: make(chan struct{}),
	}
}

func (this *Client) EnableMCPMode() {
	this.MCP.mu.Lock()
	defer this.MCP.mu.Unlock()
	this.MCP.mode = true
	this.MCP.suppressOutput = true
}

func (this *Client) IsMCPMode() bool {
	this.MCP.mu.Lock()
	defer this.MCP.mu.Unlock()
	return this.MCP.mode
}

func (this *Client) HasTarget() bool {
	return this.Process != nil && this.Library != nil && this.BrkManager != nil
}

func (this *Client) SetTarget(process *controller.Process, library *controller.LibraryInfo) {
	this.Process = process
	this.Library = library
	if this.BrkManager != nil {
		this.BrkManager.SetProcess(process)
	}
	this.ResetMCPState()
}

func (this *Client) ClearTarget() {
	this.Process = nil
	this.Library = nil
	this.PreviousCMD = ""
	if this.Config != nil {
		this.Config.ThreadFilters = nil
		this.Config.Display = nil
	}
	if this.BrkManager != nil {
		this.BrkManager.SetProcess(nil)
		this.BrkManager.Reset()
	}
	this.ResetMCPState()
}

func (this *Client) ShouldSuppressOutput() bool {
	this.MCP.mu.Lock()
	defer this.MCP.mu.Unlock()
	return this.MCP.suppressOutput
}

func (this *Client) SetRunIssued(runIssued bool) {
	this.MCP.mu.Lock()
	defer this.MCP.mu.Unlock()
	this.MCP.runIssued = runIssued
}

func (this *Client) HasRunIssued() bool {
	this.MCP.mu.Lock()
	defer this.MCP.mu.Unlock()
	return this.MCP.runIssued
}

func (this *Client) IsStopped() bool {
	this.MCP.mu.Lock()
	defer this.MCP.mu.Unlock()
	return this.MCP.stopped
}

func (this *Client) MarkRunning() {
	this.MCP.mu.Lock()
	defer this.MCP.mu.Unlock()
	this.MCP.stopped = false
}

func (this *Client) MarkStandby() {
	this.MCP.mu.Lock()
	defer this.MCP.mu.Unlock()
	this.MCP.runIssued = false
	this.MCP.stopped = false
}

func (this *Client) ResetMCPState() {
	this.MCP.mu.Lock()
	defer this.MCP.mu.Unlock()
	this.MCP.runIssued = false
	this.MCP.stopped = false
	this.MCP.stopSeq = 0
	this.MCP.lastStop = nil
	close(this.MCP.notifyCh)
	this.MCP.notifyCh = make(chan struct{})
}

func (this *Client) CurrentStopSequence() uint64 {
	this.MCP.mu.Lock()
	defer this.MCP.mu.Unlock()
	return this.MCP.stopSeq
}

func (this *Client) LastStopInfo() *StopInfo {
	this.MCP.mu.Lock()
	defer this.MCP.mu.Unlock()
	if this.MCP.lastStop == nil {
		return nil
	}
	info := *this.MCP.lastStop
	return &info
}

func (this *Client) WaitForStopAfter(seq uint64, timeout time.Duration) (*StopInfo, error) {
	return this.WaitForStopAfterContext(context.Background(), seq, timeout)
}

func (this *Client) WaitForStopAfterContext(ctx context.Context, seq uint64, timeout time.Duration) (*StopInfo, error) {
	deadline := time.Now().Add(timeout)
	for {
		this.MCP.mu.Lock()
		if this.MCP.stopSeq > seq && this.MCP.lastStop != nil {
			info := *this.MCP.lastStop
			this.MCP.mu.Unlock()
			return &info, nil
		}
		waitCh := this.MCP.notifyCh
		this.MCP.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("timed out waiting for a breakpoint after %s", timeout)
		}

		select {
		case <-waitCh:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(remaining):
			return nil, fmt.Errorf("timed out waiting for a breakpoint after %s", timeout)
		}
	}
}

func (this *Client) RecordBreakpointHit() *StopInfo {
	info := this.buildStopInfo()

	this.MCP.mu.Lock()
	defer this.MCP.mu.Unlock()

	this.MCP.stopSeq++
	info.Sequence = this.MCP.stopSeq
	this.MCP.lastStop = info
	this.MCP.stopped = true

	close(this.MCP.notifyCh)
	this.MCP.notifyCh = make(chan struct{})

	copied := *info
	return &copied
}

func (this *Client) buildStopInfo() *StopInfo {
	info := &StopInfo{
		Pid:          this.Process.WorkPid,
		Tid:          this.Process.WorkTid,
		BreakpointID: -1,
		PC:           this.Process.Context.PC,
		LR:           this.Process.Context.LR,
		SP:           this.Process.Context.SP,
	}

	address, err := this.Process.ParseAddress(this.Process.Context.PC)
	if err != nil {
		return info
	}

	info.Library = address.LibInfo.LibName
	info.FileOffset = address.Offset
	info.VirtualOffset = address.Offset

	if converted, convErr := utils.ConvertFileOffsetToVirtualOffset(address.LibInfo.LibPath, address.Offset); convErr == nil {
		info.VirtualOffset = converted
	}

	for id, brk := range this.BrkManager.BreakPoints {
		if brk.Deleted || !brk.Enable {
			continue
		}
		if brk.Hardware {
			if brk.Addr.Absolute == info.PC {
				info.BreakpointID = id
				break
			}
			continue
		}
		if controller.Equals(brk.Addr, address) {
			info.BreakpointID = id
			break
		}
	}

	return info
}

func (this *Client) CaptureOutput(fn func()) string {
	captureStdoutMu.Lock()
	defer captureStdoutMu.Unlock()

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		fn()
		return ""
	}

	os.Stdout = writer

	outputCh := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, reader)
		outputCh <- buf.String()
	}()

	fn()

	_ = writer.Close()
	os.Stdout = oldStdout
	output := <-outputCh
	_ = reader.Close()

	return strings.TrimSpace(output)
}
