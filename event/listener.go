package event

import (
	"eDBG/controller"
	"encoding/binary"
	"github.com/cilium/ebpf/perf"
	manager "github.com/gojue/ebpfmanager"
	"unsafe"
	// "eDBG/utils"
	"eDBG/cli"
	"fmt"
	"strings"
	"syscall"
	// "time"
)

type EventListener struct {
	pid           uint32
	client        *cli.Client
	process       *controller.Process
	ByteOrder     binary.ByteOrder
	Incomingdata  chan []byte
	EventData     chan []byte
	Record        chan perf.Record
	WaitingEvents int
}

func CreateEventListener(process *controller.Process) *EventListener {
	return &EventListener{
		process:      process,
		ByteOrder:    getHostByteOrder(),
		Incomingdata: make(chan []byte, 512),
		EventData:    make(chan []byte, 512),
		Record:       make(chan perf.Record, 1),
	}
}

func (this *EventListener) SendRecord(rec perf.Record) {
	this.Record <- rec
}

func (this *EventListener) SetupClient(client *cli.Client) {
	this.client = client
}

func getHostByteOrder() binary.ByteOrder {
	var i int32 = 0x01020304
	u := unsafe.Pointer(&i)
	pb := (*byte)(u)
	b := *pb
	if b == 0x04 {
		return binary.LittleEndian
	}
	return binary.BigEndian
}

func (this *EventListener) Workdata(data []byte) {
	if this.client == nil || this.client.Process == nil {
		return
	}
	<-this.client.Done
	bo := this.ByteOrder
	context := &controller.ProcessContext{}
	for i := 12; i < 12+8*30; i += 8 {
		context.Regs = append(context.Regs, bo.Uint64(data[i:i+8]))
	}
	context.LR = bo.Uint64(data[12+8*30 : 12+8*31])
	context.SP = bo.Uint64(data[12+8*31 : 12+8*32])
	context.PC = bo.Uint64(data[12+8*32 : 12+8*33])
	if len(data) >= 284 {
		// 硬件断点无法采样 pstate
		context.Pstate = bo.Uint64(data[12+8*33 : 12+8*34])
	} else {
		context.Pstate = 0xFFFFFFFF
	}
	this.client.Process.Context = context
	this.client.RecordBreakpointHit()
	this.client.Incoming <- true
}

func (this *EventListener) Run() {
	go func() {
		for {
			data := <-this.Incomingdata
			this.Workdata(data)
		}
	}()
	go func() {
		for {
			data := <-this.EventData
			this.WorkEvent(data)
			<-this.client.NotifyContinue
			// this.client.Time = time.Now()
			if this.WaitingEvents > 0 {
				this.WaitingEvents -= 1
			}
			// fmt.Println("Event End.")
			// fmt.Println(this.WaitingEvents)
			if this.WaitingEvents == 0 {
				// fmt.Println("OK, Continue.")
				if this.client != nil && this.client.Process != nil {
					this.client.Process.Continue()
				}
			}
		}
	}()
}

func (this *EventListener) OnEvent(cpu int, data []byte, perfmap *manager.PerfMap, manager *manager.Manager) {
	this.WaitingEvents += 1 // 还是有触发竞争的可能，但是每次加锁效率有点低，感觉高并发场景需求没那么高
	// fmt.Println("OnEvent: ", this.ByteOrder.Uint32(data[4:8]))
	this.EventData <- data
}
func (this *EventListener) PassEvent(IsHardware bool) {
	// fmt.Println("PASSED EVENT")
	if IsHardware {
		<-this.Record // 舍弃这个 Sample
	}
	this.client.Working = false
	this.client.NotifyContinue <- true
}
func (this *EventListener) WorkEvent(data []byte) {
	if this.client == nil || this.client.Process == nil {
		return
	}
	process := this.client.Process
	// fmt.Println("Event: ", this.ByteOrder.Uint32(data[4:8]))
	for {
		if !this.client.Working {
			break
		}
	}
	// fmt.Println("Event Start:", this.ByteOrder.Uint32(data[4:8]))
	this.client.Working = true
	process.UpdatePidList()
	bo := this.ByteOrder
	this.pid = bo.Uint32(data[4:8])
	nowTid := bo.Uint32(data[12+8*34 : 16+8*34])
	PC := bo.Uint64(data[12+8*32 : 12+8*33])

	for _, ablepid := range process.PidList {
		if this.pid == ablepid {
			process.WorkPid = this.pid
			process.StoppedPID(this.pid)
			if this.client.BrkManager.TempBreakTid != 0 {
				// 临时断点判断线程 ID
				if PC == 0xFFFFFFFF {
					if nowTid == this.client.BrkManager.TempBreakTid {
						process.WorkTid = nowTid
						if PC == 0xFFFFFFFF {
							dataRaw := <-this.Record
							this.Incomingdata <- dataRaw.RawSample[12:]
						} else {
							this.Incomingdata <- data
						}

						this.client.DoClean <- true
						return
					}
					// 单步调试断点被其他线程命中
					// 这里默认了如果存在单步调试断点那么下一个触发的一定是单步调试断点
					// 如果硬件断点失效会出错
					// fmt.Println("PASSED: SingleStep")
					this.PassEvent(PC == 0xFFFFFFFF)
					return
				}
			}

			valid := false
			for _, t := range this.client.Config.ThreadFilters {
				if !t.Enable {
					continue
				}
				if t.Thread.Tid != 0 {
					valid = true
					if nowTid == t.Thread.Tid {
						process.WorkTid = nowTid
						if PC == 0xFFFFFFFF {
							dataRaw := <-this.Record
							this.Incomingdata <- dataRaw.RawSample[12:]
						} else {
							this.Incomingdata <- data
						}
						this.client.DoClean <- true
						return
					}
					continue
				}
				if t.Thread.Name != "" {
					tList, err := process.GetCurrentThreads()
					if err != nil {
						fmt.Printf("WARNING: Failed to get threads: %v. Filters on thread name not working.\n", err)
						continue
					}
					found := false
					for _, tInfo := range tList {
						if strings.Contains(t.Thread.Name, tInfo.Name) {
							// 线程名称有长度限制会被截断，尽量支持用户指定完整的线程名称
							valid = true
							found = true
							if tInfo.Tid == nowTid {
								process.WorkTid = nowTid
								process.StoppedPID(this.pid)
								if PC == 0xFFFFFFFF {
									dataRaw := <-this.Record
									this.Incomingdata <- dataRaw.RawSample[12:]
								} else {
									this.Incomingdata <- data
								}
								this.client.DoClean <- true
								return
							}
						}
					}
					if !found {
						fmt.Printf("WARNING: No thread Named %s\n", t.Thread.Name)
						continue
					}
				}
			}
			if !valid {
				// 没有可用的线程过滤器，按照 pid 工作
				process.WorkTid = nowTid
				process.StoppedPID(this.pid)
				if PC == 0xFFFFFFFF {
					dataRaw := <-this.Record
					this.Incomingdata <- dataRaw.RawSample[12:]
				} else {
					this.Incomingdata <- data
				}
				this.client.DoClean <- true
				return
			}
			// fmt.Println("PASSED: PID ABLE BUT FILTERED BY THREAD")
			this.PassEvent(PC == 0xFFFFFFFF)
			// 目标 PID，在确认 Event 清空后 Continue
			return
		}
	}
	// 无关 PID 直接 Continue
	// fmt.Println("PASSED: Unrelated PID")
	this.PassEvent(PC == 0xFFFFFFFF)
	syscall.Kill(int(this.pid), syscall.SIGCONT)
}
