// Command xrayfetch downloads and verifies the pinned Xray-core release.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/ArtikGenius/zatvor/internal/xraybin"
)

func main() {
	platform := flag.String("platform", "", "os-arch, e.g. linux-amd64")
	dest := flag.String("dest", "", "destination directory, e.g. third_party/xray/bin/linux-amd64")
	pinFile := flag.String("pin", "", "path to pin.json (default: repo third_party/xray/pin.json)")
	flag.Parse()
	if *platform == "" || *dest == "" {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*platform, *dest, *pinFile); err != nil {
		fmt.Fprintln(os.Stderr, "xrayfetch:", err)
		os.Exit(1)
	}
}

func run(platform, dest, pinFile string) error {
	if pinFile == "" {
		p, err := xraybin.RepoPinPath()
		if err != nil {
			return err
		}
		pinFile = p
	}
	pin, err := xraybin.LoadPin(pinFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	p, err := xraybin.Fetch(ctx, http.DefaultClient, pin, "", platform, dest)
	if err != nil {
		return err
	}
	fmt.Println(p)
	return nil
}
