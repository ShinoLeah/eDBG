package cli

import (
	"eDBG/config"
	"eDBG/controller"
	"eDBG/module"
	"eDBG/utils"
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"encoding/binary"
	"github.com/c-bata/go-prompt"
)

type DisplayInfo struct {
	Name string
	Address uint64
	Enable bool
	Len int
}

type ThreadFilter struct {
	Thread *controller.Thread
	Enable bool
} 

type UserConfig struct {
	Registers bool
	Disasm bool
	ThreadFilters []*ThreadFilter
	Display []*DisplayInfo
}

type Client struct {
	Library *controller.LibraryInfo
	Process *controller.Process
	BrkManager *module.BreakPointManager
	Config *UserConfig
	Incoming chan bool
	Done chan bool
	DoClean chan bool
	NotifyContinue chan bool
	PreviousCMD string
	Working bool
	promptInstance *prompt.Prompt
}

func CreateClient(process *controller.Process, library *controller.LibraryInfo, brkManager *module.BreakPointManager, config *UserConfig) *Client {
	return &Client{
		Library: library, 
		Process: process, 
		BrkManager: brkManager, 
		Config: config, 
		Incoming: make(chan bool, 1), 
		Done: make(chan bool, 1), 
		DoClean: make(chan bool, 1), 
		NotifyContinue: make(chan bool, 1), 
		PreviousCMD: "",
	}
}

func (this *Client) Run() {
	go func() {
		for {
			<- this.DoClean
			this.StopProbes()
		}
	}()
	go func() {
		for {
			<- this.Incoming
			// fmt.Println("Incoming!")
			this.OutputInfo()
			
		}
	}()
	go func() {
		this.REPL()
	}()
}

func (this *Client) OutputInfo() {
	// fmt.Println("?")
	if this.Config.Registers {
		fmt.Print(config.BLUE)
		fmt.Println("──────────────────────────────────────[ REGISTERS ]──────────────────────────────────────")
		fmt.Print(config.NC)
		this.Process.PrintContext()
	}
	if this.Config.Disasm {
		fmt.Print(config.BLUE)
		fmt.Println("──────────────────────────────────────[  DISASM  ]────────────────────────────────────────")
		fmt.Print(config.NC)
		this.PrintDisassembleInfo(this.Process.Context.PC, 10)
	}
	cntDisplay := 0
	for _, display := range this.Config.Display {
		if display.Enable {
			cntDisplay++
			break
		}
	}
	if cntDisplay > 0 {
		fmt.Print(config.BLUE)
		fmt.Println("──────────────────────────────────────[ DISPLAY ]────────────────────────────────────────")
		fmt.Print(config.NC)
		this.PrintDisplay()
	}
	if this.Config.Registers || this.Config.Disasm || cntDisplay > 0 {
		fmt.Print(config.BLUE)
		fmt.Println("─────────────────────────────────────────────────────────────────────────────────────────")
		fmt.Print(config.NC)
	}
}

func (this *Client) PrintDisplay() {
	for id, display := range this.Config.Display {
		if !display.Enable {
			continue
		}
		fmt.Printf("[%d] 0x%x:%s\n", id, display.Address, display.Name)
		data := make([]byte, display.Len)
		n, err := utils.ReadProcessMemory(this.Process.WorkPid, uintptr(display.Address), data)

		if err != nil {
			fmt.Printf("Reading Memory Error: %v\n", err)
		}

		fmt.Println(utils.HexDump(display.Address, data, n))
	}
}

func (this *Client) StopProbes() {
	err := this.BrkManager.Stop()
	if err != nil {
		fmt.Println("WARN: Failed to terminate. A Breakpoint maybe triggered due to a race condition.", err)
		this.CleanUp()
	} else {	
		this.Done <- true
	}
}

func (this *Client) REPL() {
	this.promptInstance = prompt.New(
		this.executeCommand,
		this.completer,
		prompt.OptionPrefix("(eDBG) "),
		// prompt.OptionTitle("eDBG REPL"),
		prompt.OptionPrefixTextColor(prompt.Blue),
		prompt.OptionLivePrefix(func() (string, bool) {
			return "(eDBG) ", true
		}),
		prompt.OptionAddKeyBind(
			prompt.KeyBind{
				Key: prompt.ControlC,
				Fn: func(b *prompt.Buffer) {
					this.CleanUp()
				},
			},
		),
	)
	this.promptInstance.Run()
}

func (this *Client) executeCommand(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		line = this.PreviousCMD
	} else {
		this.PreviousCMD = line
	}
	if line == "" {
		return
	}
	parts := strings.Fields(line)
	cmd := parts[0]
	args := parts[1:]
	switch cmd {
		case "break", "b":
			this.HandleBreak(args)
		case "hbreak", "hb":
			this.HandleHBreak(args, config.HW_BREAKPOINT_X)
		case "rwatch":
			this.HandleHBreak(args, config.HW_BREAKPOINT_R)
		case "watch":
			this.HandleHBreak(args, config.HW_BREAKPOINT_W)
		case "step", "s":
			if this.HandleStep() && this.HandleContinue() {
			}
		case "next", "n":
			if this.HandleNext() && this.HandleContinue() {
			}
		case "examine", "x":
			this.HandleMemory(args)
		case "dump":
			this.HandleDump(args)
		case "quit", "q":
			this.CleanUp()
			return
		case "continue", "c":
			if this.HandleContinue() {
			}
		case "display", "disp":
			this.HandleDisplay(args)
		case "undisplay", "undisp":
			this.HandleUndisplay(args)
		case "list", "l", "dis", "disassemble":
			this.HandleList(args)
		case "info", "i":
			this.HandleInfo(args)
		case "finish", "fi":
			if this.HandleFinish() && this.HandleContinue() {
			}
		case "return":
			fmt.Println("Command return is not supported because eDBG cannot perform modification. Use finish or fi instead.")
		case "backtrace", "bt":
			fmt.Println("Command backtrace is coming soon...")
		case "jump", "j":
			fmt.Println("Command jump is not supported because eDBG cannot perform modification.")
		case "thread", "t":
			this.HandleThread(args)
		case "disable":
			this.HandleChangeBrk(args, false)
		case "enable":
			this.HandleChangeBrk(args, true)
		case "delete":
			this.HandleDelete(args)
		case "until", "u":
			if this.HandleUntil(args) && this.HandleContinue() {
			}
		case "run", "r":
			fmt.Println("eDBG DO NOT execute programs. Please run it manually.")
		case "set":
			this.HandleSet(args)
		case "write", "w":
			this.HandleWrite(args)
		default:
			fmt.Println("Unknown command:", cmd)
	}
}

func (this *Client) completer(d prompt.Document) []prompt.Suggest {
	words := strings.Split(d.Text, " ")
    if len(words) != 1 || len(d.Text) == 0 {
        return nil
    }

	s := []prompt.Suggest{
		{Text: "break", Description: "Set software breakpoint [b]"},
		{Text: "enable", Description: "Enable breakpoint"},
		{Text: "disable", Description: "Disable breakpoint"},
		{Text: "delete", Description: "Delete breakpoint"},
		{Text: "hbreak", Description: "Set hardware breakpoint [hb]"},
		{Text: "watch", Description: "Set watchpoint (write)"},
		{Text: "rwatch", Description: "Set watchpoint (read)"},
		{Text: "step", Description: "Step into instruction [s]"},
		{Text: "next", Description: "Step over instruction [n]"},
		{Text: "continue", Description: "Continue execution [c]"},
		{Text: "disassemble", Description: "Disassemble instructions [dis]"},
		{Text: "list", Description: "Disassemble instructions [l]"},
		{Text: "set", Description: "Set a name for specified address"},
		{Text: "info", Description: "Show debug information [i]"},
		{Text: "display", Description: "Add memory display [disp]"},
		{Text: "until", Description: "Execute to specified address [u]"},
		{Text: "undisplay", Description: "Remove memory display [undisp]"},
		{Text: "x", Description: "Examine memory [x]"},
		{Text: "dump", Description: "Dump memory to file"},
		{Text: "thread", Description: "Manage threads [t]"},
		{Text: "quit", Description: "Exit debugger [q]"},
		{Text: "examine", Description: "Examine memory [x]"},
		{Text: "finish", Description: "Execute until function return [fi]"},
		{Text: "write", Description: "Write memory"},
	}
	return prompt.FilterHasPrefix(s, d.GetWordBeforeCursor(), true)
}

func (this *Client) HandleSet(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: set <address> <name>")
		return
	}
	address, err := this.ParseUserAddressToAbsolute(args[0])
	if err != nil {
		fmt.Printf("Failed to parse address: %v\n", err)
		return
	}
	this.Process.Symbols[address] = args[1]
}


func (this *Client) AddThreadFilter(args string) {
	id, err := strconv.ParseInt(args, 0, 32)
	if err != nil {
		fmt.Printf("Bad id: %v\n", err)
		return 
	}
	tlist, err := this.Process.GetCurrentThreads()
	if err != nil {
		fmt.Printf("Failed to get threads: %v\n", err)
		return 
	}
	if int(id) >= len(tlist) {
		fmt.Printf("Bad id.\n")
		return 
	}
	this.Config.ThreadFilters = append(this.Config.ThreadFilters, &ThreadFilter{
		Thread: tlist[id],
		Enable: true,
	})

}
func (this *Client) AddThreadFilterName(args string) {
	this.Config.ThreadFilters = append(this.Config.ThreadFilters, &ThreadFilter{
		Thread: &controller.Thread{
			Tid: 0,
			Name: args,
		},
		Enable: true,
	})
}

func (this *Client) DeleteThreadFilter(args string) {
	id, err := strconv.ParseInt(args, 0, 32)
	if err != nil {
		fmt.Printf("Bad id: %v\n", err)
		return 
	}
	if int(id) >= len(this.Config.ThreadFilters) {
		fmt.Printf("Bad id.\n")
		return 
	}
	if this.Config.ThreadFilters[id].Enable == false {
		fmt.Printf("Bad id.\n")
		return 
	}
	this.Config.ThreadFilters[id].Enable = false
}

func (this *Client) HandleThread(args []string) {
	if len(args) == 0 {
		this.Process.PrintThreads()
		return
	}
	if len(args) >= 2 {
		switch args[0] {
		case "add", "+":
			this.AddThreadFilter(args[1])
		case "name", "+n":
			this.AddThreadFilterName(args[1])
		case "del", "-", "delete":
			this.DeleteThreadFilter(args[1])
		case "all":
			this.Config.ThreadFilters = []*ThreadFilter{}
		default:
			fmt.Println("Usage: thread add id\n      thread del id\nUse info t to browse threads.")
		}
		return
	}
	fmt.Println("Usage: thread add id\n      thread del id\nUse info t to browse threads.")
}

func (this *Client) PrintThreadFilters() {
	for id, t := range this.Config.ThreadFilters {
		if !t.Enable {
			continue
		}
		if t.Thread.Tid != 0 {
			fmt.Printf("[%d] ThreadID: %d\n", id, t.Thread.Tid)
			continue
		}
		if t.Thread.Name != "" {
			fmt.Printf("[%d] ThreadName: %s\n", id, t.Thread.Name)
			continue
		}
	}
}

func (this *Client) HandleInfo(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: info break/b\n       info register/reg/r\n       info thread/t")
		return
	}
	switch args[0] {
		case "break", "b":
			this.BrkManager.PrintBreakPoints()
		case "reg", "register", "r":
			this.Process.PrintContext()
		case "thread", "t":
			fmt.Println("Available threads:")
			this.Process.PrintThreads()
			fmt.Println("Thread filters:")
			this.PrintThreadFilters()
		default:
			fmt.Println("Usage: info break/b\n       info register/reg/r\n       info threads/t")
	}
}


func (this *Client) HandleChangeBrk(args []string, status bool) {
	if len(args) == 0 {
		fmt.Println("Usage: enable/disable <breakpoint id>. Use <info b> to browse breakpoint info\n")
		return
	}
	id, err := strconv.ParseInt(args[0], 0, 32)
	if err != nil {
		fmt.Printf("Bad breakpoint id: %v\n", err)
		return 
	}
	this.BrkManager.ChangeBreakPoint(int(id), status)
}


func (this *Client) HandleDelete(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: delete <breakpoint id>. Use <info b> to browse breakpoint info")
		return
	}
	id, err := strconv.ParseInt(args[0], 0, 32)
	if err != nil {
		fmt.Printf("Bad breakpoint id: %v\n", err)
		return 
	}
	this.BrkManager.DeleteBreakPoint(int(id))
}

func (this *Client) HandleFinish() bool {
	address, err := this.Process.ParseAddress(this.Process.Context.LR)
	if err != nil {
		fmt.Printf("Failed to parse LR: %v\n", err)
		return false
	}
	// fmt.Printf("Next addr: %s+%x\n", address.LibInfo.LibName, address.Offset)
	this.BrkManager.SetTempBreak(address, this.Process.WorkTid)
	return true
	// this.HandleContinue()
}

func (this *Client) CleanUp() {
	this.Process.Continue()
	_ = this.BrkManager.Stop()
	syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
}

func (this *Client) HandleList(args []string) {
	if len(args) == 0 {
		this.PrintDisassembleInfo(this.Process.Context.PC, 10)
		return
	}
	if len(args) == 1 {
		address, err := utils.GetExprValue(args[0], this.Process.Context)
		if err != nil {
			fmt.Println(err)
			return
		}
		this.PrintDisassembleInfo(address, 10)
		return
	}
	if len(args) >= 2 {
		address, err := utils.GetExprValue(args[0], this.Process.Context)
		if err != nil {
			fmt.Println(err)
			return
		}
		len, err := utils.GetExprValue(args[1], this.Process.Context)
		if err != nil {
			fmt.Println(err)
			return
		}
		this.PrintDisassembleInfo(address, int(len))
		return
	}
}

func (this *Client) HandleDisplay(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: display <address>\n       display <address> <len>\n       display <address> <len> <name>\n")
		return
	}
	info := &DisplayInfo{"", 0, true, 16}
	address, err := utils.GetExprValue(args[0], this.Process.Context)
	if err != nil {
		fmt.Println(err)
		return
	}
	info.Address = address
	if len(args) > 1 {
		len, err := utils.GetExprValue(args[1], this.Process.Context)
		if err != nil {
			fmt.Println(err)
			return
		}
		info.Len = int(len)
	}
	if len(args) > 2 {
		info.Name = args[2]
	}
	this.Config.Display = append(this.Config.Display, info)
}

func (this *Client) HandleUndisplay(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: undisplay <id>\n")
		return
	}
	id, err := strconv.ParseUint(args[0], 0, 64)
	if err != nil {
		fmt.Printf("Bad id: %v\n", err)
		return
	}
	if int(id) >= len(this.Config.Display) {
		fmt.Printf("Invalid id.\n")
		return
	}
	if !this.Config.Display[id].Enable {
		fmt.Printf("Invalid id.\n")
		return
	}
	this.Config.Display[id].Enable = false
}

func (this *Client) ParseUserAddressToAbsolute(arg string) (uint64, error) {
	offset, err := utils.GetExprValue(arg, this.Process.Context)
	if err != nil {
		if strings.Contains(arg, "+") {
			lastIndex := strings.LastIndex(arg, "+")
			libName := arg[:lastIndex]
			offset_str := arg[lastIndex+1:]
			offset, err := strconv.ParseUint(offset_str, 0, 64)
			if err != nil {
				return 0, fmt.Errorf("Bad address: %v", err)
			}
			if libName == "$" {
				return this.Process.Context.PC+offset*4, nil
			} else {
				libInfo, err := controller.CreateLibrary(this.Process, libName)
				if err != nil {
					return 0, err
				}
				address := controller.NewAddress(libInfo, offset)
				return this.Process.GetAbsoluteAddress(address)
			}
		} 
		return 0, err
	}
	if offset > 0x5000000000 {
		return offset, nil
	}
	address := controller.NewAddress(this.Library, offset)
	return this.Process.GetAbsoluteAddress(address)
	
}


func (this *Client) ParseUserAddress(arg string) (*controller.Address, error) {
	offset, err := utils.GetExprValue(arg, this.Process.Context)
	if err != nil {
		if strings.Contains(arg, "+") {
			lastIndex := strings.LastIndex(arg, "+")
			libName := arg[:lastIndex]
			offset_str := arg[lastIndex+1:]
			offset, err := strconv.ParseUint(offset_str, 0, 64)
			if err != nil {
				return &controller.Address{}, fmt.Errorf("Bad address: %v", err)
			}
			if libName == "$" {
				address, err := this.Process.ParseAddress(this.Process.Context.PC+offset*4)
				if err != nil {
					return &controller.Address{}, fmt.Errorf("Bad address: %v", err)
				}
				return address, nil
			} else {
				libInfo, err := controller.CreateLibrary(this.Process, libName)
				if err != nil {
					return &controller.Address{}, err
				}
				return controller.NewAddress(libInfo, offset), nil
			}
		} 
		return &controller.Address{}, fmt.Errorf("Bad address: %v", err)
	}
	address := controller.NewAddress(this.Library, offset)
	if offset > 0x5000000000 {
		address, err = this.Process.ParseAddress(offset)
		if err != nil {
			return &controller.Address{}, fmt.Errorf("Bad address: %v", err)
		}
	}
	return address, nil
}

func (this *Client) HandleUntil(args []string) bool {
	if len(args) == 0 {
		fmt.Println("Usage: until <address>")
		return false
	}
	address, err := this.ParseUserAddress(args[0])
	if err != nil {
		fmt.Printf("Failed to parse address: %v\n", err)
		return false
	}
	if address.Absolute == 0 {
		fmt.Printf("Relative address is not support for until\n")
		return false
	}
	
	if err = this.BrkManager.SetTempBreak(address, this.Process.WorkTid); err != nil {
		fmt.Printf("Failed to set Temporary breakpoint: %v\n", err)
	} else {
		// this.HandleContinue()
	}
	return true
}

func (this *Client) HandleHBreak(args []string, Type int) {
	if len(args) == 0 {
		fmt.Println("Usage: hbreak <address>")
		return
	}
	address, err := this.ParseUserAddress(args[0])
	if err != nil {
		fmt.Printf("Failed to parse address: %v\n", err)
		return
	}
	if address.Absolute == 0 {
		absolute, err := this.Process.GetAbsoluteAddress(address)
		if err != nil {
			fmt.Printf("Failed to get absolute address.")
			return
		}
		address.Absolute = absolute
	}
	if err = this.BrkManager.CreateHWBreakPoint(address, true, Type); err != nil {
		fmt.Printf("Failed to set breakpoint: %v\n", err)
	} else {
		fmt.Printf("Breakpoint at %x\n", address.Absolute)
	}
}

func (this *Client) HandleBreak(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: break <address>")
		return
	}
	address, err := this.ParseUserAddress(args[0])
	if err != nil {
		fmt.Printf("Failed to parse address: %v\n", err)
		return
	}
	
	if err = this.BrkManager.CreateBreakPoint(address, true); err != nil {
		fmt.Printf("Failed to set breakpoint: %v\n", err)
	} else {
		fmt.Printf("Breakpoint at %s+%x\n", address.LibInfo.LibName, address.Offset)
	}
}

func (this *Client) HandleContinue() bool {
	err := this.BrkManager.SetupProbe()
	if err != nil {
		fmt.Printf("Failed to Continue: %v\n", err)
		this.BrkManager.ClearTempBreak()
		// this.CleanUp()
		fmt.Println("Possible reasons:\n\n1. Some instructions do not support uprobe. Try setting breakpoints on other instructions or use until to skip the current instruction.\n2. Breakpoints with invalid addresses exist. Check the breakpoint list.\n")
		return false
	}
	this.NotifyContinue <- true
	this.Working = false
	return true
}

func (this *Client) HandleStep() bool {
	NextPC, err := utils.PredictNextPC(this.Process.WorkPid, this.Process.Context, true)
	if NextPC == 0xDEADBEEF {
		target, err := utils.GetTarget(this.Process.WorkPid, this.Process.Context)
		if err != nil {
			fmt.Printf("Failed to get branch target: %v\n", err)
			return false
		}
		address, err := this.Process.ParseAddress(uint64(this.Process.Context.GetPC()+4))
		if err != nil {
			fmt.Printf("Failed to parse nextPC: %v\n", err)
			return false
		}
		this.BrkManager.SetTempBreak(address, this.Process.WorkTid)
		address2, err := this.Process.ParseAddress(uint64(target))
		if err != nil {
			fmt.Printf("Failed to parse nextPC: %v\n", err)
			return false
		}
		this.BrkManager.SetTempBreak(address2, this.Process.WorkTid)
		// this.HandleContinue()
		return true
	}
	if err != nil {
		fmt.Printf("Failed to predict next addr: %v\n", err)
		return false
	}
	address, err := this.Process.ParseAddress(uint64(NextPC))
	if err != nil {
		fmt.Printf("Failed to parse nextPC: %v\n", err)
		return false
	}
	this.BrkManager.SetTempBreak(address, this.Process.WorkTid)
	// this.HandleContinue()
	return true
}

func (this *Client) HandleNext() bool {
	NextPC, err := utils.PredictNextPC(this.Process.WorkPid, this.Process.Context, false)
	if NextPC == 0xDEADBEEF {
		target, err := utils.GetTarget(this.Process.WorkPid, this.Process.Context)
		if err != nil {
			fmt.Printf("Failed to get branch target: %v\n", err)
			return false
		}
		address, err := this.Process.ParseAddress(uint64(this.Process.Context.GetPC()+4))
		if err != nil {
			fmt.Printf("Failed to parse nextPC: %v\n", err)
			return false
		}
		this.BrkManager.SetTempBreak(address, this.Process.WorkTid)
		address2, err := this.Process.ParseAddress(uint64(target))
		if err != nil {
			fmt.Printf("Failed to parse nextPC: %v\n", err)
			return false
		}
		this.BrkManager.SetTempBreak(address2, this.Process.WorkTid)
		// this.HandleContinue()
		return true
	}
	if err != nil {
		fmt.Printf("Failed to predict next addr: %v\n", err)
		return false
	}
	address, err := this.Process.ParseAddress(uint64(NextPC))
	if err != nil {
		fmt.Printf("Failed to parse nextPC: %v\n", err)
		return false
	}
	this.BrkManager.SetTempBreak(address, this.Process.WorkTid)
	// this.HandleContinue()
	return true
}

func (this *Client) HandleWrite(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: write <address> <hexstring>")
		return
	}
	address, err := utils.GetExprValue(args[0], this.Process.Context)
	if err != nil {
		fmt.Println(err)
		return
	}
	
	data, err := utils.HexStringToBytes(args[1])
	if err != nil {
		fmt.Printf("Failed to parse hexstring %s: %v\n", args[1], err)
		return
	}

	n, err := utils.WriteProcessMemory(this.Process.WorkPid, uintptr(address), data)

	if err != nil {
		fmt.Printf("Writing Memory Error: %v\n", err)
		return
	}
	fmt.Printf("%d bytes written.\n", n)
	fmt.Println(utils.HexDump(address, data, n))
}

func (this *Client) HandleDump(args []string) {
	if len(args) < 3 {
		fmt.Println("Usage: dump <address> <length> <filename>")
		return
	}
	address, err := utils.GetExprValue(args[0], this.Process.Context)
	if err != nil {
		fmt.Println(err)
		return
	}
	
	length := 16
	if len(args) > 1 {
		len, err := strconv.Atoi(args[1])
		if err != nil || len <= 0 {
			fmt.Println("Bad length")
			return
		}
		length = len
	}

	data := make([]byte, length)
	n, err := utils.ReadProcessMemory(this.Process.WorkPid, uintptr(address), data)

	if err != nil {
		fmt.Printf("Reading Memory Error: %v\n", err)
		return
	}

	// fmt.Println(utils.HexDump(address, data, n))
	err = utils.WriteBytesToFile(args[2], data)
	if err != nil {
		fmt.Printf("Failed to write to file: %v\n", err)
		return
	}
	fmt.Printf("Saved %d bytes to %s\n", n, args[2])
}


func (this *Client) HandleMemory(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: x <address> <length/type>")
		return
	}
	address, err := utils.GetExprValue(args[0], this.Process.Context)
	if err != nil {
		fmt.Println(err)
		return
	}
	
	length := 16
	format := "hexdump"
	if len(args) > 1 {
		len, err := utils.GetExprValue(args[1], this.Process.Context)
		if err != nil {
			switch args[1] {
			case "ptr":
				format = "ptr"
				length = 8
			case "str":
				format = "str"
				outbuf := &strings.Builder{}
				remoteAddr := uintptr(address)
				for {
					stringbuf := make([]byte, 1)
					n, err := utils.ReadProcessMemory(this.Process.WorkPid, remoteAddr, stringbuf)
					if n < 1 || err != nil || !strconv.IsPrint(rune(stringbuf[0])) {
						break
					}
					outbuf.WriteByte(stringbuf[0])
					remoteAddr += 1
				}
				fmt.Println(outbuf)
				return
			case "int":
				format = "int"
				length = 4
			default:
				fmt.Println("Invalid type or length: ", err)
				return
			}
		} else {
			if len < 0 || len > 0x100000{
				fmt.Println("Invalid length")
				return
			}
			length = int(len)
		}
	}

	data := make([]byte, length)
	n, err := utils.ReadProcessMemory(this.Process.WorkPid, uintptr(address), data)

	if err != nil {
		fmt.Printf("Reading Memory Error: %v\n", err)
		return
	}

	switch format {
	case "hexdump":
		fmt.Println(utils.HexDump(address, data, n))
	case "ptr":
		fmt.Printf("0x%x\n", binary.LittleEndian.Uint64(data))
	case "int":
		fmt.Printf("%d\n", binary.LittleEndian.Uint32(data))
	}
	

}

func (this *Client) PrintDisassembleInfo(address uint64, len int) {
	// len为指令条数
	codeBuf := make([]byte, len*4)
	n, err := utils.ReadProcessMemory(this.Process.WorkPid, uintptr(address), codeBuf)
	if err != nil {
		fmt.Println("Failed to read code...")
	} else {
		addInfo, err := this.Process.ParseAddress(address)
		if err == nil {
			fmt.Printf("%s>>  0x%x<%s+%x>%s\t", config.GREEN, address, addInfo.LibInfo.LibName, addInfo.Offset, config.GREEN)
		} else {
			fmt.Printf("%s>>  0x%x\t%s", config.GREEN, address, config.GREEN)
		}

		code, err := utils.DisASM(codeBuf[0:4], address, this.Process)
		if err == nil {
			index := strings.Index(code, " ")
			fmt.Printf("%s%s%s ", config.YELLOW, code[:index], config.NC)
			fmt.Printf("%s%s%s\n", config.CYAN, code[index+1:], config.NC)
		} else {
			fmt.Println("(disassemble failed)")
		}
		for i := 4; i < n; i += 4{
			addInfo, err = this.Process.ParseAddress(address+uint64(i))
			if err == nil {
				fmt.Printf("    0x%x<%s+%x>\t", address+uint64(i), addInfo.LibInfo.LibName, addInfo.Offset)
			} else {
				fmt.Printf("    0x%x\t", address+uint64(i))
			}

			code, err = utils.DisASM(codeBuf[i:i+4], address+uint64(i), this.Process)
			if err == nil {
				index := strings.Index(code, " ")
				fmt.Printf("%s%s%s ", config.YELLOW, code[:index], config.NC)
				fmt.Printf("%s%s%s\n", config.CYAN, code[index+1:], config.NC)
			} else {
				fmt.Println("(disassemble failed)\n")
			}
		}
	}
}


