package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// processMemoryCountersEx is PROCESS_MEMORY_COUNTERS_EX.
type processMemoryCountersEx struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

var procGetProcessMemoryInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("K32GetProcessMemoryInfo")

func memOf(pid uint32) (ws, private uint64, ok bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_VM_READ, false, pid)
	if err != nil {
		return 0, 0, false
	}
	defer windows.CloseHandle(h)
	var c processMemoryCountersEx
	c.CB = uint32(unsafe.Sizeof(c))
	r, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&c)), uintptr(c.CB))
	if r == 0 {
		return 0, 0, false
	}
	return uint64(c.WorkingSetSize), uint64(c.PrivateUsage), true
}

// processTreeMemory sums the working set of this process and its descendants
// found by parent PID (WebView2 runs msedgewebview2.exe children).
func processTreeMemory() memReport {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return memReport{RSSMB: "not measured: " + err.Error(), PrivateMB: "not measured"}
	}
	defer windows.CloseHandle(snap)
	type ent struct {
		ppid uint32
		name string
	}
	all := map[uint32]ent{}
	kids := map[uint32][]uint32{}
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		all[e.ProcessID] = ent{e.ParentProcessID, windows.UTF16ToString(e.ExeFile[:])}
		kids[e.ParentProcessID] = append(kids[e.ParentProcessID], e.ProcessID)
	}
	me := windows.GetCurrentProcessId()
	tree := []uint32{me}
	seen := map[uint32]bool{me: true}
	for i := 0; i < len(tree); i++ {
		for _, k := range kids[tree[i]] {
			// PIDs are reused; a "child" must not be the parent of its parent.
			if !seen[k] && k != 0 {
				seen[k] = true
				tree = append(tree, k)
			}
		}
	}
	var rep memReport
	var ws, priv uint64
	for i, pid := range tree {
		w, p, ok := memOf(pid)
		if !ok {
			continue
		}
		ws += w
		priv += p
		link := "child"
		if i == 0 {
			link = "self"
		}
		rep.Processes = append(rep.Processes, procInfo{PID: int(pid), Name: all[pid].name, Link: link,
			RSSMB: mb(w), PPID: int(all[pid].ppid)})
	}
	rep.RSSMB, rep.PrivateMB = mb(ws), mb(priv)
	return rep
}
