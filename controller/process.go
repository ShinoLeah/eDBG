package controller

import (
	"path"
	"syscall"
	"fmt"
	"os"
	"strings"
	"eDBG/utils"
	"golang.org/x/exp/slices"
	"strconv"
)



type Process struct {
	PidList []uint32
	PackageName string
	ExecPath string
	ProcMaps map[uint32]*ProcMaps
	StoppedPid []uint32
	Context *ProcessContext
	WorkPid uint32
}

func CreateProcess(packageName string) (*Process, error) {
	process := &Process{}
	process.ProcMaps = make(map[uint32]*ProcMaps)
	process.PackageName = packageName
	err := process.GetExecPath()
	if err != nil {
		return &Process{}, err
	}
	err = process.checkPackageName()
	if err != nil {
		return &Process{}, err
	}
	return process, nil
}

func (this *Process) UpToDate() {
	// this.UpdatePidList()
	this.UpdateMapsPid(this.WorkPid)
}

func (this *Process) GetExecPath() error {
	exec_path, err := os.Executable()
    if err != nil {
        return fmt.Errorf("please build as executable binary, %v", err)
    }
	this.ExecPath = path.Dir(exec_path)
	return nil
}

func (this *Process) UpdatePidList() {
	this.PidList = []uint32{}
    content, err := utils.RunCommand("sh", "-c", "ps -ef -o name,pid,ppid | grep ^"+this.PackageName)
    if err != nil {
        return
    }
    lines := strings.Split(content, "\n")
    for _, line := range lines {
        parts := strings.Fields(line)
        value, err := strconv.ParseUint(parts[1], 10, 32)
        if err != nil {
            panic(err)
        }
        this.PidList = append(this.PidList, uint32(value))
    }
    return
}

func (this *Process) UpdateMapsPid(pid uint32) {
	procMaps, err := GetProcMaps(pid)
	if err == nil {
		this.ProcMaps[pid] = procMaps
	}
}

func (this *Process) UpdateMaps() {
	for _, pid := range this.PidList {
		procMaps, err := GetProcMaps(pid)
		if err == nil {
			this.ProcMaps[pid] = procMaps
		}
	}
}


func (this *Process) checkPackageName() error {
	packageinfos := utils.GetPackageInfos()
	_, err := packageinfos.FindPackageByName(this.PackageName)
	return err
}

func (this *Process) GetLibSearchPaths() []string {
	SearchPath := []string{
        "/system/lib64",
        "/apex/com.android.art/lib64",
        "/apex/com.android.conscrypt/lib64",
        "/apex/com.android.runtime/bin",
        "/apex/com.android.runtime/lib64/bionic",
    }
	if this.PackageName == "" {
		return SearchPath
	}
	this.UpdatePidList()
	this.UpdateMaps()
	for _, mapsInfo := range this.ProcMaps {
		mapsPaths := mapsInfo.GetLibSearchPaths()
		for _, paths := range mapsPaths {
			if !slices.Contains(SearchPath, paths) {
				SearchPath = append(SearchPath, paths)
			}
		}
	}

	pkgPaths := FindLibPathFromPackage(this.PackageName)
	for _, path := range pkgPaths {
		if !slices.Contains(SearchPath, path) {
			SearchPath = append(SearchPath, path)
		}
	}
	return SearchPath
}

func FindLibPathFromPackage(name string) []string {
	SearchPath := []string{}
    content, err := utils.RunCommand("pm", "path", name)
    if err != nil {
        panic(err)
    }
    for _, line := range strings.Split(content, "\n") {
        parts := strings.Split(line, ":")
        if len(parts) == 2 {
            // 将 apk 文件也作为搜索路径
            apk_path := parts[1]
            _, err := os.Stat(apk_path)
            if err == nil {
                SearchPath = append(SearchPath, apk_path)
            }
            // 将 apk + /lib/arm64 作为搜索路径
            items := strings.Split(parts[1], "/")
            lib_search_path := strings.Join(items[:len(items)-1], "/") + "/lib/arm64"
            _, err = os.Stat(lib_search_path)
            if err == nil {
                SearchPath = append(SearchPath, lib_search_path)
            }
        }
    }
	return SearchPath
}

func (this *Process) Continue() error {
	this.UpdatePidList()
	for _, pid := range this.PidList {
		// fmt.Printf("Continued pid: %d\n", int(pid))
        err := syscall.Kill(int(pid), syscall.SIGCONT)
        if err != nil {
            if err == syscall.ESRCH {
                return fmt.Errorf("No such process -> %d\n", pid)
            } else {
                return fmt.Errorf("Process continue error:%v\n", pid)
            }
        }
    }
	this.StoppedPid = []uint32{}
	return nil
}

func (this *Process) StoppedPID(pid uint32) {
	this.StoppedPid = append(this.StoppedPid, pid)
}

func (this *Process) Stop() error {
	this.UpdatePidList()
	for _, pid := range this.PidList {
        err := syscall.Kill(int(pid), syscall.SIGSTOP)
		this.StoppedPid = append(this.StoppedPid, pid)
        if err != nil {
            if err == syscall.ESRCH {
                return fmt.Errorf("No such process -> %d\n", pid)
            } else {
                return fmt.Errorf("Process Stop error:%v\n", pid)
            }
        }
    }
	return nil
}

