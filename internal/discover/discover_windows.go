//go:build windows

package discover

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

var (
	ntdllWinCwds                     = syscall.NewLazyDLL("ntdll.dll")
	procNtQueryInformationProcessWin = ntdllWinCwds.NewProc("NtQueryInformationProcess")
	kernel32WinCwds                  = syscall.NewLazyDLL("kernel32.dll")
	procReadProcessMemoryWin         = kernel32WinCwds.NewProc("ReadProcessMemory")
)

type processBasicInfo struct {
	Reserved1       uintptr
	PebBaseAddress  uintptr
	Reserved2       [2]uintptr
	UniqueProcessId uintptr
	Reserved3       uintptr
}

type unicodeString struct {
	Length        uint16
	MaximumLength uint16
	Buffer        uintptr
}

func getProcessCwdWindows(pid int) (string, error) {
	const (
		processQueryInformation = 0x0400
		processVMRead           = 0x0010
	)
	h, err := syscall.OpenProcess(processQueryInformation|processVMRead, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer syscall.CloseHandle(h)

	var pbi processBasicInfo
	var returnLength uint32
	ret, _, _ := procNtQueryInformationProcessWin.Call(
		uintptr(h),
		0,
		uintptr(unsafe.Pointer(&pbi)),
		uintptr(unsafe.Sizeof(pbi)),
		uintptr(unsafe.Pointer(&returnLength)),
	)
	if ret != 0 || pbi.PebBaseAddress == 0 {
		return "", errors.New("NtQueryInformationProcess failed")
	}

	// 64-bit PEB layout: ProcessParameters pointer at PebBaseAddress + 0x20
	pebProcessParametersOffset := pbi.PebBaseAddress + 0x20
	var pParams uintptr
	var bytesRead uintptr
	ret, _, _ = procReadProcessMemoryWin.Call(
		uintptr(h),
		pebProcessParametersOffset,
		uintptr(unsafe.Pointer(&pParams)),
		unsafe.Sizeof(pParams),
		uintptr(unsafe.Pointer(&bytesRead)),
	)
	if ret == 0 || pParams == 0 {
		return "", errors.New("read ProcessParameters failed")
	}

	// CurrentDirectory struct (starts at offset 0x38 in RTL_USER_PROCESS_PARAMETERS on x64)
	dirStructOffset := pParams + 0x38
	var dosPathUnicode unicodeString
	ret, _, _ = procReadProcessMemoryWin.Call(
		uintptr(h),
		dirStructOffset,
		uintptr(unsafe.Pointer(&dosPathUnicode)),
		unsafe.Sizeof(dosPathUnicode),
		uintptr(unsafe.Pointer(&bytesRead)),
	)
	if ret == 0 || dosPathUnicode.Length == 0 || dosPathUnicode.Buffer == 0 {
		return "", errors.New("read DosPath failed")
	}

	buf := make([]uint16, dosPathUnicode.Length/2)
	ret, _, _ = procReadProcessMemoryWin.Call(
		uintptr(h),
		dosPathUnicode.Buffer,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(dosPathUnicode.Length),
		uintptr(unsafe.Pointer(&bytesRead)),
	)
	if ret == 0 {
		return "", errors.New("read buffer failed")
	}

	return syscall.UTF16ToString(buf), nil
}

func windowsCwdsMap(pids []int) map[int]cwdInfo {
	res := make(map[int]cwdInfo, len(pids))
	for _, pid := range pids {
		path, err := getProcessCwdWindows(pid)
		if err != nil || path == "" {
			continue
		}
		_, statErr := os.Stat(path)
		exists := true
		if statErr != nil && errors.Is(statErr, os.ErrNotExist) {
			exists = false
		}
		res[pid] = cwdInfo{Path: path, Exists: exists}
	}
	return res
}
