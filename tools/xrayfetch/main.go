// Command xrayfetch downloads and verifies the pinned Xray-core release.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	platform := flag.String("platform", "", "os-arch, e.g. linux-amd64")
	dest := flag.String("dest", "", "destination directory, e.g. third_party/xray/bin/linux-amd64")
	base := flag.String("base-url", "", "override release download base URL (default: pinned GitHub release)")
	pinFile := flag.String("pin", PinFile, "path to pin.json")
	flag.Parse()
	if *platform == "" || *dest == "" {
		flag.Usage()
		os.Exit(2)
	}
	PinFile = *pinFile
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	p, err := Fetch(ctx, http.DefaultClient, *base, *platform, *dest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "xrayfetch:", err)
		os.Exit(1)
	}
	fmt.Println(p)
}
