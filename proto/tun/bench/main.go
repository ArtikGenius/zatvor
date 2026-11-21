// Command bench is the local test server and throughput client of the TUN
// prototype (stage 0c, task 8).
//
//	bench serve    -http 127.0.0.1:18080 -source 127.0.0.1:18081
//	bench download -addr 198.18.0.20:5201 -seconds 30
//
// serve answers HTTP with "zatvor-tunnel" (the upstream redirects the probe
// addresses here, so that body proves a request went through the tunnel) and
// streams zeros to every TCP client of -source. download reads from -addr for
// the given time and prints {"bytes":..,"seconds":..,"mbps":..}.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// TunnelBody is what the local HTTP server answers.
const TunnelBody = "zatvor-tunnel\n"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: bench serve|download [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "download":
		err = download(os.Args[2:])
	default:
		err = fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	httpAddr := fs.String("http", "127.0.0.1:18080", "HTTP listen address")
	source := fs.String("source", "127.0.0.1:18081", "zero-stream listen address")
	fs.Parse(args)
	ln, err := net.Listen("tcp", *source)
	if err != nil {
		return err
	}
	go func() {
		buf := make([]byte, 256<<10)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				for {
					if _, err := c.Write(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, TunnelBody)
	})
	fmt.Println("bench serving", *httpAddr, *source)
	return http.ListenAndServe(*httpAddr, mux)
}

// Result is what download prints.
type Result struct {
	Bytes   int64   `json:"bytes"`
	Seconds float64 `json:"seconds"`
	Mbps    float64 `json:"mbps"`
	Error   string  `json:"error,omitempty"`
}

func download(args []string) error {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	addr := fs.String("addr", "198.18.0.20:5201", "address to read from")
	seconds := fs.Float64("seconds", 30, "how long to read")
	fs.Parse(args)
	res := measure(*addr, time.Duration(*seconds*float64(time.Second)))
	return json.NewEncoder(os.Stdout).Encode(res)
}

func measure(addr string, d time.Duration) Result {
	c, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return Result{Error: err.Error()}
	}
	defer c.Close()
	start := time.Now()
	c.SetReadDeadline(start.Add(d))
	buf := make([]byte, 256<<10)
	var n int64
	for {
		k, err := c.Read(buf)
		n += int64(k)
		if err != nil {
			break
		}
	}
	s := time.Since(start).Seconds()
	r := Result{Bytes: n, Seconds: s}
	if s > 0 {
		r.Mbps = float64(n) * 8 / s / 1e6
	}
	return r
}
