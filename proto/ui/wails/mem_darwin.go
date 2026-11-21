package main

/*
#include <dlfcn.h>
typedef int (*resp_fn)(int);
static int responsible_pid(int pid) {
	static resp_fn f = 0;
	static int looked = 0;
	if (!looked) {
		f = (resp_fn)dlsym(RTLD_DEFAULT, "responsibility_get_pid_responsible_for_pid");
		looked = 1;
	}
	return f ? f(pid) : -1;
}
#include <libproc.h>
#include <sys/resource.h>
// phys_footprint (what Activity Monitor shows as Memory); -1 on error.
static long long footprint(int pid) {
	struct rusage_info_v2 ri;
	if (proc_pid_rusage(pid, RUSAGE_INFO_V2, (rusage_info_t *)&ri) != 0) return -1;
	return (long long)ri.ri_phys_footprint;
}
*/
import "C"

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func preciseUnixNano() int64 { return time.Now().UnixNano() }

// processTreeMemory sums the resident size of this process, its descendants by
// parent PID, and the WebKit XPC helpers whose responsible process is this
// one (they are children of launchd, not of the app).
func processTreeMemory() memReport {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,rss=,comm=").Output()
	if err != nil {
		return memReport{RSSMB: "not measured: ps: " + err.Error()}
	}
	type ent struct {
		ppid int
		rss  uint64
		name string
	}
	all := map[int]ent{}
	kids := map[int][]int{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		kb, _ := strconv.ParseUint(f[2], 10, 64)
		name := strings.Join(f[3:], " ")
		all[pid] = ent{ppid, kb * 1024, name}
		kids[ppid] = append(kids[ppid], pid)
	}
	me := os.Getpid()
	tree := []int{me}
	links := map[int]string{me: "self"}
	for i := 0; i < len(tree); i++ {
		for _, k := range kids[tree[i]] {
			if all[k].name == "ps" || strings.HasSuffix(all[k].name, "/ps") {
				continue // our own measurement command
			}
			if _, ok := links[k]; !ok {
				links[k] = "child"
				tree = append(tree, k)
			}
		}
	}
	var rep memReport
	if C.responsible_pid(C.int(me)) == -1 {
		rep.Note = "macOS: responsibility_get_pid_responsible_for_pid not found; WebKit helper processes are NOT included"
	} else {
		rep.Note = "macOS: WebKit helpers are found by responsible PID (they are launchd children); resident size double-counts shared pages"
		for pid := range all {
			if _, ok := links[pid]; !ok && int(C.responsible_pid(C.int(pid))) == me {
				links[pid] = "responsible"
				tree = append(tree, pid)
			}
		}
	}
	var rss, foot uint64
	footOK := true
	for _, pid := range tree {
		e := all[pid]
		rss += e.rss
		pi := procInfo{PID: pid, Name: e.name, Link: links[pid], RSSMB: mb(e.rss), PPID: e.ppid}
		if f := C.footprint(C.int(pid)); f >= 0 {
			foot += uint64(f)
			pi.Foot = mb(uint64(f))
		} else {
			footOK = false
		}
		rep.Processes = append(rep.Processes, pi)
	}
	if footOK {
		rep.FootprintMB = mb(foot)
	} else {
		rep.FootprintMB = "not measured: proc_pid_rusage failed for a process in the tree"
	}
	for pid, e := range all {
		if strings.Contains(e.name, "WebKit") {
			rep.WebKitSeen = append(rep.WebKitSeen, procInfo{PID: pid, Name: e.name, PPID: e.ppid,
				Resp: int(C.responsible_pid(C.int(pid))), RSSMB: mb(e.rss)})
		}
	}
	rep.RSSMB = mb(rss)
	rep.PrivateMB = "not measured: no private-bytes counter via ps on macOS"
	return rep
}
