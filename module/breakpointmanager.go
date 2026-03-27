package module

import (
	"eDBG/config"
	"eDBG/controller"
	"eDBG/utils"
	"fmt"
	"github.com/cilium/ebpf/perf"
	manager "github.com/gojue/ebpfmanager"
)

type IEventListener interface {
	SendRecord(rec perf.Record)
	OnEvent(int, []byte, *manager.PerfMap, *manager.Manager)
}

type BreakPoint struct {
	Addr     *controller.Address
	Enable   bool
	Deleted  bool
	Hardware bool
	Pid      uint32
	Type     int
}

type BreakPointManager struct {
	process             *controller.Process
	BreakPoints         []*BreakPoint
	temporaryBreakPoint []*BreakPoint
	ProbeHandler        *ProbeHandler
	TempBreakTid        uint32
	Running             bool
}

func CreateBreakPointManager(listener IEventListener, BTF_File string, process *controller.Process) *BreakPointManager {
	return &BreakPointManager{
		process:      process,
		ProbeHandler: CreateProbeHandler(listener, BTF_File),
		Running:      false,
	}
}

func (this *BreakPointManager) SetProcess(process *controller.Process) {
	this.process = process
}

func (this *BreakPointManager) Reset() {
	this.BreakPoints = nil
	this.temporaryBreakPoint = nil
	this.TempBreakTid = 0
	this.Running = false
}

func checkOffset(offset uint64) bool {
	return offset%4 == 0
}

func (this *BreakPointManager) SetTempBreak(address *controller.Address, tid uint32) error {
	if checkOffset(address.Offset) == false {
		return fmt.Errorf("Invalid address: %x", address.Offset)
	}
	for _, brk := range this.BreakPoints {
		if controller.Equals(brk.Addr, address) && brk.Enable == true {
			return nil
		}
	}

	brk := &BreakPoint{
		Addr:    address,
		Enable:  true,
		Deleted: false,
		Pid:     this.process.WorkPid,
		Type:    config.HW_BREAKPOINT_X,
	}

	switch config.Preference {
	case config.ALL_UPROBE, config.PREFER_UPROBE:
		brk.Hardware = false
		if address.IsAnouymous() {
			return fmt.Errorf("Failed: Anouymous address using uprobe.")
		}
	case config.ALL_PERF:
		brk.Hardware = true
	case config.PREFER_PERF:
		safe, err := utils.SafeAddress(this.process.WorkPid, address.Absolute)
		if err != nil {
			fmt.Printf("Failed parse current addr: %v\n", address.Absolute, err)
			brk.Hardware = false
			break
		}
		if !safe {
			brk.Hardware = true
		} else {
			brk.Hardware = false
		}
		if address.IsAnouymous() {
			brk.Hardware = true
		}
	}

	this.TempBreakTid = tid
	this.temporaryBreakPoint = append(this.temporaryBreakPoint, brk)
	return nil
}

func (this *BreakPointManager) CreateBreakPoint(address *controller.Address, enable bool) error {
	offset := address.Offset
	if checkOffset(offset) == false {
		return fmt.Errorf("Invalid address: %x", offset)
	}
	if address.IsAnouymous() {
		return fmt.Errorf("Anouymous address: %x, use hbreak.", offset)
	}
	for _, brk := range this.BreakPoints {
		if !brk.Deleted && controller.Equals(address, brk.Addr) {
			// fmt.Println("What?")
			if brk.Enable != enable {
				brk.Enable = enable
			} else {
				// return fmt.Errorf("BreakPoint %x exsists")
			}
			return nil
		}
	}
	brk := &BreakPoint{
		Addr:     address,
		Hardware: false,
		Enable:   enable,
		Deleted:  false,
		Pid:      this.process.WorkPid,
	}
	this.BreakPoints = append(this.BreakPoints, brk)
	return nil
}

func (this *BreakPointManager) CreateHWBreakPoint(address *controller.Address, enable bool, Type int) error {
	Count := 0
	for _, brk := range this.BreakPoints {
		if !brk.Deleted && controller.Equals(address, brk.Addr) {
			if brk.Enable != enable {
				brk.Enable = enable
			} else {
				// return fmt.Errorf("BreakPoint %x exsists")
			}
			return nil
		}
		if !brk.Deleted && brk.Hardware == true {
			Count++
		}
	}
	if Count >= config.Available_HW-2 {
		return fmt.Errorf("Hardware Breakpoint count limit exceed. Delete some hardware breakpoints or use uprobe.")
	}
	brk := &BreakPoint{
		Addr:     address,
		Hardware: true,
		Enable:   enable,
		Deleted:  false,
		Pid:      this.process.WorkPid,
		Type:     Type,
	}
	this.BreakPoints = append(this.BreakPoints, brk)
	return nil
}

func (this *BreakPointManager) ClearTempBreak() {
	this.temporaryBreakPoint = []*BreakPoint{}
}

func (this *BreakPointManager) SetupProbe() error {
	if this.Running {
		return fmt.Errorf("Probes are running now.")
	}
	if len(this.temporaryBreakPoint) == 0 {
		this.TempBreakTid = 0
	}
	err := this.ProbeHandler.SetupManager(append(this.temporaryBreakPoint, this.BreakPoints...))
	if err != nil {
		return err
	}
	this.ClearTempBreak()
	err = this.ProbeHandler.Run()
	// fmt.Println("probe is running..")
	if err != nil {
		return err
	}
	this.Running = true
	return nil
}
func (this *BreakPointManager) Init() error {
	return this.ProbeHandler.SetupManagerOptions()
}

func (this *BreakPointManager) Start(addresss []*controller.Address) error {
	for _, addr := range addresss {
		err := this.CreateBreakPoint(addr, true)
		if err != nil {
			fmt.Printf("Create Breakpoints Failed: %v, skipped.\n", err)
			continue
		}
	}
	return this.SetupProbe()
}

func (this *BreakPointManager) Stop() error {
	err := this.ProbeHandler.Stop()
	if err == nil {
		this.Running = false
	}
	return err
}

func (this *BreakPointManager) PrintBreakPoints() {
	for id, brk := range this.BreakPoints {
		if brk.Deleted {
			continue
		}
		if !brk.Enable {
			fmt.Printf("[-] ")
		} else {
			fmt.Printf("[+] ")
		}
		if brk.Hardware {
			fmt.Printf("%d: %x Hardware\n", id, brk.Addr.Absolute)
		} else {
			fmt.Printf("%d: %s+%x\n", id, brk.Addr.LibInfo.LibName, brk.Addr.Offset)
		}
	}
}

func (this *BreakPointManager) ChangeBreakPoint(id int, status bool) {
	if id >= len(this.BreakPoints) {
		fmt.Println("Breakpoint doesn't exist.")
		return
	}
	if this.BreakPoints[id].Deleted {
		fmt.Println("Breakpoint doesn't exist.")
		return
	}
	this.BreakPoints[id].Enable = status
}

func (this *BreakPointManager) DeleteBreakPoint(id int) {
	if id >= len(this.BreakPoints) {
		fmt.Println("Breakpoint doesn't exist.")
		return
	}
	if this.BreakPoints[id].Deleted {
		fmt.Println("Breakpoint doesn't exist.")
		return
	}
	this.BreakPoints[id].Enable = false
	this.BreakPoints[id].Deleted = true
}
