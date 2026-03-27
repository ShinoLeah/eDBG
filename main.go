package main

import (
	"context"
	"eDBG/cli"
	"eDBG/config"
	"eDBG/controller"
	"eDBG/event"
	mcpserver "eDBG/mcp"
	"eDBG/module"
	"eDBG/utils"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/inconshreveable/go-update"
	_ "github.com/shuLhan/go-bindata" // add for bindata in Makefile
)

type UserBreakPoints struct {
	LibName string `json:"brklib"`
	Offset  uint64 `json:"offset"`
	Enable  bool   `json:"enable"`
}

var EdbgConfigVersion int

type AppConfig struct {
	ConfigVersion int               `json:"ConfigVersion"`
	PackageName   string            `json:"packagename"`
	LibName       string            `json:"libname"`
	Hidereg       bool              `json:"hidereg"`
	Hidedis       bool              `json:"hidedis"`
	BreakPoints   []UserBreakPoints `json:"brk"`
	TNames        []string          `json:"tname"`
}

func doUpdate(url string) error {
	// 在 adb shell 里似乎无法联网（悲
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	err = update.Apply(resp.Body, update.Options{})
	if err != nil {
		return err
	}
	return nil
}
func Update(proxy bool) {
	fmt.Println("Updating...")
	var err error
	if proxy {
		err = doUpdate("https://gh-proxy.com/github.com/ShinoLeah/eDBG/releases/latest/download/eDBG_arm64/")
	} else {
		err = doUpdate("https://github.com/ShinoLeah/eDBG/releases/latest/download/eDBG_arm64/")
	}
	if err != nil {
		fmt.Printf("Update Failed: %v\n", err)
	} else {
		fmt.Println("eDBG is up-to-date.")
	}
	os.Exit(0)
}
func SaveConfig(path string, cfg AppConfig) error {
	jsonData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, jsonData, 0644)
}

func LoadConfig(path string) (AppConfig, error) {
	var cfg AppConfig
	jsonData, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	err = json.Unmarshal(jsonData, &cfg)
	return cfg, err
}

func main() {
	EdbgConfigVersion = 1
	stopper := make(chan os.Signal, 1)
	signal.Notify(stopper, os.Interrupt, syscall.SIGTERM)
	workedlib := make(map[string]*controller.LibraryInfo)
	var (
		inputfile       string
		outputfile      string
		packageName     string
		libName         string
		hiddis          bool
		hidreg          bool
		save            bool
		prefer          string
		threadFilters   string
		disableColor    bool
		brkAddressInfos []*controller.Address
		doupdate        bool
		proxy           bool
		disablePkgChk   bool
		vbkFlag         string
		mcpMode         bool
		mcpPort         int
		// vertual			bool
	)
	var brkFlag string
	var err error
	doupdate = false
	proxy = false
	flag.StringVar(&brkFlag, "b", "", "Breakpoint addresses in hex format, e.g., [0x1234,0x5678]")
	flag.StringVar(&vbkFlag, "vb", "", "Breakpoint virtual addresses (from IDA, etc.) in hex format, e.g., [0x1234,0x5678]")
	flag.StringVar(&packageName, "p", "", "Target package name")
	flag.StringVar(&libName, "l", "", "Target library name")
	// 无法运行的功能，先放着
	// flag.BoolVar(&doupdate, "update", false, "Update eDBG")
	// flag.BoolVar(&proxy, "update-proxy", false, "Update eDBG With Proxy")
	flag.BoolVar(&hidreg, "hide-register", false, "Hide Register Window")
	flag.BoolVar(&disablePkgChk, "disable-package-check", false, "Don't check package name")
	flag.BoolVar(&hiddis, "hide-disassemble", false, "Hide Disassemble Window")
	flag.BoolVar(&config.SHOW_VERTUAL, "show-vertual", false, "Show vertual address by default")
	flag.StringVar(&threadFilters, "t", "", "Thread name filters, e.g., [name1,name2]")
	flag.StringVar(&inputfile, "i", "", "Input file saved from edbg. e.g. sample.edbg")
	flag.BoolVar(&save, "s", false, "Save your progress to input file")
	flag.BoolVar(&disableColor, "disable-color", false, "Disable color display")
	flag.StringVar(&prefer, "prefer", "", "Preference. 'uprobe' for Uprobes and 'hardware' for Hardware breakpoints")
	flag.StringVar(&outputfile, "o", "&&NotSetNotSetNotSetO=O", "Save your progress to specified file")
	flag.BoolVar(&mcpMode, "mcp", false, "Start eDBG in MCP server mode")
	flag.IntVar(&mcpPort, "mcp-port", 19810, "Port used by the MCP server")
	flag.Parse()
	config.DisablePackageCheck = disablePkgChk

	if mcpMode {
		prefer = "uprobe"
		config.SHOW_VERTUAL = true
	}

	if brkFlag != "" && vbkFlag != "" {
		fmt.Println("Error: Cannot use both -b (file offset) and -vb (virtual offset) flags simultaneously.")
		os.Exit(1)
	}

	if doupdate {
		Update(false)
	}
	if proxy {
		Update(true)
	}

	if inputfile != "" {
		cfg, err := LoadConfig(inputfile)
		if err != nil {
			fmt.Printf("Failed to load input file: %v\n", err)
			os.Exit(1)
		}

		if cfg.ConfigVersion != EdbgConfigVersion {
			fmt.Printf("Config version %d is not supported now.\n", cfg.ConfigVersion)
			os.Exit(1)
		}

		packageName = cfg.PackageName
		libName = cfg.LibName
		hidreg = cfg.Hidereg
		hiddis = cfg.Hidedis

		fmt.Printf("Using Config from: %s\n", inputfile)
	}
	hasInitialTarget := packageName != "" || libName != ""
	if !mcpMode || hasInitialTarget {
		if packageName == "" {
			fmt.Println("No Package Specified. Use -p com.package.name")
			os.Exit(1)
		}
		if libName == "" {
			fmt.Println("No Library Specified. Use -l libraryname.so")
			os.Exit(1)
		}
	}

	btfFile := ""
	if !utils.CheckConfig("CONFIG_DEBUG_INFO_BTF=y") {
		btfFile = utils.FindBTFAssets()
	}
	switch prefer {
	case "":
		config.Preference = config.PREFER_PERF
	case "uprobe":
		config.Preference = config.ALL_UPROBE
	case "hardware":
		config.Preference = config.ALL_PERF
	default:
		fmt.Println("Unsupported preference. Usage: -prefer uprobe/hardware")
		os.Exit(1)
	}
	if disableColor {
		config.GREEN = ""
		config.YELLOW = ""
		config.RED = ""
		config.BLUE = ""
		config.CYAN = ""
		config.NC = ""
	}
	if !utils.CheckConfig("CONFIG_HAVE_HW_BREAKPOINT=y") {
		fmt.Println("Hardware breakpoints not enabled. Using uprobes.")
		config.Preference = config.ALL_UPROBE
		config.Available_HW = 0
	}

	var process *controller.Process
	var library *controller.LibraryInfo
	if !mcpMode || hasInitialTarget {
		process, err = controller.CreateProcess(packageName)
		if err != nil {
			fmt.Println("Create process error: ", err)
			os.Exit(1)
		}
		library, err = controller.CreateLibrary(process, libName)
		if err != nil {
			fmt.Println("Create Library error: ", err)
			os.Exit(1)
		}
		workedlib[libName] = library
	}

	eventListener := event.CreateEventListener(process)
	brkManager := module.CreateBreakPointManager(eventListener, btfFile, process)
	client := cli.CreateClient(process, library, brkManager, &cli.UserConfig{
		Registers: !hidreg,
		Disasm:    !hiddis,
	})
	if mcpMode {
		client.EnableMCPMode()
	}
	if (inputfile == "" || mcpMode) && library != nil {
		var brkAddrs []uint64
		var err error

		if !mcpMode {
			// 修改：根据 -b 或 -vb 处理断点
			if vbkFlag != "" {
				// 解析虚拟地址
				virtualAddrs, err := ParseBreakPoints(vbkFlag)
				if err != nil {
					fmt.Println("Parsing virtual addresses failed: ", err)
					os.Exit(1)
				}
				// 将虚拟地址转换为文件偏移
				for _, vaddr := range virtualAddrs {
					fmt.Printf("Converting virtual address 0x%x ", vaddr)
					fileOffset, err := utils.ConvertVirtualOffsetToFileOffset(library.LibPath, vaddr)
					if err != nil {
						fmt.Printf("Failed to convert virtual address 0x%x: %v\n", vaddr, err)
						os.Exit(1)
					}
					fmt.Printf("to file offset: 0x%x\n", fileOffset)
					brkAddrs = append(brkAddrs, fileOffset)
				}
			} else { // 包含 brkFlag != "" 和两者都为空的情况
				brkAddrs, err = ParseBreakPoints(brkFlag)
				if err != nil {
					fmt.Println("Create Breakpoints Failed: ", err)
					os.Exit(1)
				}
			}
		}

		tNames, err := ParseThreadNames(threadFilters)
		if err != nil {
			fmt.Println("Create Thread names Failed: ", err)
			os.Exit(1)
		}

		for _, name := range tNames {
			client.AddThreadFilterName(name)
		}

		for _, offset := range brkAddrs {
			brkAddressInfos = append(brkAddressInfos, controller.NewAddress(library, offset))
		}
	} else if inputfile != "" && library != nil {
		Config, _ := LoadConfig(inputfile)
		for _, name := range Config.TNames {
			client.AddThreadFilterName(name)
		}
		if !mcpMode {
			for _, brk := range Config.BreakPoints {
				val, ok := workedlib[brk.LibName]
				var libInfo *controller.LibraryInfo
				if !ok {
					_libinfo, err := controller.CreateLibrary(process, brk.LibName)
					if err != nil {
						fmt.Printf("Cannot locate library %s: %v. Skipped.\n", brk.LibName, err)
						continue
					}
					libInfo = _libinfo
					workedlib[brk.LibName] = libInfo
				} else {
					libInfo = val
				}

				if brk.Enable {
					brkAddressInfos = append(brkAddressInfos, controller.NewAddress(libInfo, brk.Offset))
				} else {
					brkManager.CreateBreakPoint(controller.NewAddress(libInfo, brk.Offset), false)
				}
			}
		}
	}

	eventListener.SetupClient(client)
	err = brkManager.Init()
	if err != nil {
		fmt.Println("Module init Failed: ", err)
		os.Exit(1)
	}

	var server *mcpserver.Server
	serverErr := make(chan error, 1)

	if mcpMode {
		client.Run()
		eventListener.Run()

		server = mcpserver.NewServer(client)
		addr := fmt.Sprintf("0.0.0.0:%d", mcpPort)
		go func() {
			serverErr <- server.Serve(addr)
		}()
		fmt.Printf("MCP server listening on %s. Use adb forward tcp:%d tcp:%d and connect from the host with http://127.0.0.1:%d/mcp\n", addr, mcpPort, mcpPort, mcpPort)
		select {
		case <-stopper:
		case err := <-serverErr:
			if err != nil {
				fmt.Println("MCP server failed: ", err)
			}
		}
	} else {
		if len(brkAddressInfos) > 0 {
			err = brkManager.Start(brkAddressInfos)
			if err != nil {
				fmt.Println("Module start Failed: ", err)
				fmt.Println("Possible reasons:\n\n1. Some instructions do not support uprobe. Try setting breakpoints on other instructions or use until to skip the current instruction.\n2. Breakpoints with invalid addresses exist. Check the breakpoint list.\n")
				os.Exit(1)
			}
			fmt.Printf("Working on %s in %s. Press Ctrl+C to quit\n", libName, packageName)
		} else {
			fmt.Println("eDBG is not running. Use continue/run to start eDBG when breakpoints are ready.")
		}

		client.Run()
		eventListener.Run()
		<-stopper
	}

	fmt.Println("Quiting eDBG...")
	if server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}
	if client.Process != nil {
		_ = client.Process.Continue()
	}
	// _ = brkManager.Stop()
	if save {
		if inputfile == "" {
			fmt.Println("No input file!")
		} else {
			outputfile = inputfile
		}
	}
	if outputfile != "&&NotSetNotSetNotSetO=O" {
		cfg := AppConfig{
			ConfigVersion: EdbgConfigVersion,
			PackageName:   packageName,
			LibName:       libName,
			Hidereg:       hidreg,
			Hidedis:       hiddis,
		}

		for _, brk := range brkManager.BreakPoints {
			if brk.Deleted || brk.Hardware {
				continue
			}
			cfg.BreakPoints = append(cfg.BreakPoints, UserBreakPoints{
				LibName: brk.Addr.LibInfo.LibName,
				Offset:  brk.Addr.Offset,
				Enable:  brk.Enable,
			})
		}

		for _, t := range client.Config.ThreadFilters {
			if !t.Enable {
				continue
			}
			tName := t.Thread.Name
			if tName != "" {
				cfg.TNames = append(cfg.TNames, tName)
			}
		}
		SaveConfig(outputfile, cfg)
		fmt.Println("Progress saved to file: ", outputfile)
	}
}

func ParseBreakPoints(brkFlag string) ([]uint64, error) {
	trimmed := strings.Trim(brkFlag, "[]")
	if trimmed == "" {
		return []uint64{}, nil
	}

	addresses := strings.Split(trimmed, ",")
	var brkAddrs []uint64
	for _, addrStr := range addresses {
		addrStr = strings.TrimSpace(addrStr)
		addr, err := strconv.ParseUint(addrStr, 0, 64)
		if err != nil {
			return nil, fmt.Errorf("ParseBreakPoints: Invalid address %q: %v", addrStr, err)
		}
		brkAddrs = append(brkAddrs, addr)
	}
	return brkAddrs, nil
}

func ParseThreadNames(brkFlag string) ([]string, error) {
	trimmed := strings.Trim(brkFlag, "[]")
	if trimmed == "" {
		return []string{}, nil
	}
	addresses := strings.Split(trimmed, ",")
	var brkAddrs []string
	for _, addrStr := range addresses {
		addrStr = strings.TrimSpace(addrStr)
		brkAddrs = append(brkAddrs, addrStr)
	}
	return brkAddrs, nil
}
