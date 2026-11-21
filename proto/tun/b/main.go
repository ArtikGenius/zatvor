//go:build windows

// Command b is TUN variant B: a sing-tun stack (Go library, system stack,
// auto route, strict route) that hands every TCP and UDP flow to an Xray
// SOCKS inbound, the v2rayN default architecture. TCP from the process given
// by -direct-process is dialled directly instead, bound to the default
// physical interface (the per-app rule check).
//
// Prototype only (stage 0c, task 8). It changes routes, DNS and WFP filters,
// so it refuses to run unless CI=true and ZATVOR_ALLOW_LIVE=1, like
// internal/systest; Check.ps1 runs it on a disposable GitHub runner.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"time"

	tun "github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common/bufio"
	"github.com/sagernet/sing/common/control"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/protocol/socks"
)

// Same addresses as variant A (proto/tun/a): the TUN is .1, its DNS is .2
// (sing-tun's default: the next address in the first prefix).
var (
	inet4 = netip.MustParsePrefix("172.19.0.1/30")
	inet6 = netip.MustParsePrefix("fdfe:dcba:9876::1/126")
)

type stdLogger struct{ l *log.Logger }

func (s stdLogger) Trace(args ...any) {}
func (s stdLogger) Debug(args ...any) {}
func (s stdLogger) Info(args ...any)  { s.l.Print(append([]any{"INFO "}, args...)...) }
func (s stdLogger) Warn(args ...any)  { s.l.Print(append([]any{"WARN "}, args...)...) }
func (s stdLogger) Error(args ...any) { s.l.Print(append([]any{"ERROR "}, args...)...) }
func (s stdLogger) Fatal(args ...any) { s.l.Fatal(append([]any{"FATAL "}, args...)...) }
func (s stdLogger) Panic(args ...any) { s.l.Panic(append([]any{"PANIC "}, args...)...) }

type handler struct {
	ctx     context.Context
	logger  stdLogger
	socks   *socks.Client
	direct  string // lower-case absolute path, or empty
	monitor tun.DefaultInterfaceMonitor
	finder  control.InterfaceFinder
}

func (h *handler) JudgeFlow(network uint8, source, destination netip.AddrPort, firstPacket []byte) tun.FlowVerdict {
	return tun.FlowVerdict{Action: tun.ActionAccept}
}

func (h *handler) NewDNSPacket(payload []byte, source, destination M.Socksaddr, writer N.PacketWriter) {
	// Not used: JudgeFlow never asks for DNS hijack, DNS goes through SOCKS
	// to the upstream like any other UDP flow.
}

func (h *handler) NewConnectionEx(ctx context.Context, conn net.Conn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var remote net.Conn
	var err error
	route := "proxy"
	if h.direct != "" {
		path, perr := tcpOwnerPath(source.AddrPort())
		if perr != nil {
			h.logger.Warn("process lookup ", source, ": ", perr)
		} else {
			h.logger.Info("owner of ", source, ": ", path)
			if strings.EqualFold(path, h.direct) {
				route = "direct"
			}
		}
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	if route == "direct" {
		remote, err = h.dialDirect(dctx, destination)
	} else {
		remote, err = h.socks.DialContext(dctx, N.NetworkTCP, destination)
	}
	cancel()
	h.logger.Info("tcp ", source, " -> ", destination, " via ", route, errSuffix(err))
	if err != nil {
		conn.Close()
		if onClose != nil {
			onClose(err)
		}
		return
	}
	err = bufio.CopyConn(ctx, conn, remote)
	if onClose != nil {
		onClose(err)
	}
}

func (h *handler) NewPacketConnectionEx(ctx context.Context, conn N.PacketConn, source, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	pc, err := h.socks.ListenPacket(dctx, destination)
	cancel()
	h.logger.Info("udp ", source, " -> ", destination, " via proxy", errSuffix(err))
	if err != nil {
		conn.Close()
		if onClose != nil {
			onClose(err)
		}
		return
	}
	err = bufio.CopyPacketConn(ctx, conn, bufio.NewPacketConn(pc))
	if onClose != nil {
		onClose(err)
	}
}

// dialDirect dials destination bound to the current default interface, which
// keeps the socket out of our own TUN routes.
func (h *handler) dialDirect(ctx context.Context, destination M.Socksaddr) (net.Conn, error) {
	iface := h.monitor.DefaultInterface()
	if iface == nil {
		return nil, fmt.Errorf("no default interface")
	}
	d := net.Dialer{Control: control.BindToInterface(h.finder, iface.Name, iface.Index)}
	return d.DialContext(ctx, "tcp", destination.String())
}

func errSuffix(err error) string {
	if err != nil {
		return " error: " + err.Error()
	}
	return ""
}

func main() {
	name := flag.String("name", "zatvor-b", "TUN adapter name")
	socksAddr := flag.String("socks", "127.0.0.1:10808", "Xray SOCKS inbound")
	direct := flag.String("direct-process", "", "absolute path of a process whose TCP goes direct")
	strict := flag.Bool("strict-route", true, "sing-tun strict route (WFP DNS block)")
	flag.Parse()

	if os.Getenv("CI") != "true" || os.Getenv("ZATVOR_ALLOW_LIVE") != "1" {
		fmt.Fprintln(os.Stderr, "proto/tun/b runs only on CI runners (needs CI=true and ZATVOR_ALLOW_LIVE=1)")
		os.Exit(2)
	}
	logger := stdLogger{log.New(os.Stderr, "", log.LstdFlags|log.Lmicroseconds)}
	if err := run(logger, *name, *socksAddr, *direct, *strict); err != nil {
		logger.l.Fatal("b: ", err)
	}
}

func run(logger stdLogger, name, socksAddr, direct string, strict bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	finder := control.NewDefaultInterfaceFinder()
	netMon, err := tun.NewNetworkUpdateMonitor(logger)
	if err != nil {
		return fmt.Errorf("network monitor: %w", err)
	}
	if err := netMon.Start(); err != nil {
		return fmt.Errorf("network monitor start: %w", err)
	}
	defer netMon.Close()
	ifMon, err := tun.NewDefaultInterfaceMonitor(netMon, logger, tun.DefaultInterfaceMonitorOptions{InterfaceFinder: finder})
	if err != nil {
		return fmt.Errorf("interface monitor: %w", err)
	}
	if err := ifMon.Start(); err != nil {
		return fmt.Errorf("interface monitor start: %w", err)
	}
	defer ifMon.Close()
	if d := ifMon.DefaultInterface(); d != nil {
		logger.Info("default interface: ", d.Name, " index ", d.Index)
	}

	opts := tun.Options{
		Name:             name,
		Inet4Address:     []netip.Prefix{inet4},
		Inet6Address:     []netip.Prefix{inet6},
		MTU:              1500,
		AutoRoute:        true,
		StrictRoute:      strict,
		InterfaceFinder:  finder,
		InterfaceMonitor: ifMon,
		Logger:           logger,
	}
	dev, err := tun.New(opts)
	if err != nil {
		return fmt.Errorf("tun.New: %w", err)
	}
	defer dev.Close()

	server := M.ParseSocksaddr(socksAddr)
	h := &handler{
		ctx:     ctx,
		logger:  logger,
		socks:   socks.NewClient(N.SystemDialer, server, socks.Version5, "", ""),
		direct:  strings.ToLower(direct),
		monitor: ifMon,
		finder:  finder,
	}
	stack, err := tun.NewStack("system", tun.StackOptions{
		Context:         ctx,
		Tun:             dev,
		TunOptions:      opts,
		UDPTimeout:      30 * time.Second,
		Handler:         h,
		Logger:          logger,
		InterfaceFinder: finder,
	})
	if err != nil {
		return fmt.Errorf("stack: %w", err)
	}
	if err := stack.Start(); err != nil {
		return fmt.Errorf("stack start: %w", err)
	}
	defer stack.Close()
	if err := dev.Start(); err != nil {
		return fmt.Errorf("tun start: %w", err)
	}
	logger.Info("TUN up: ", name, " strict_route=", strict, " socks=", server, " direct=", direct)
	fmt.Println("READY") // Check.ps1 waits for the adapter, this is for humans reading the log
	<-ctx.Done()
	logger.Info("stopping")
	return nil
}
