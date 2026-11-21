// Package xrayint holds integration tests that run the pinned Xray-core
// binary against the repository fixtures (build tag xrayint). This file is
// the UDP sink those tests point outbounds at.
package xrayint

import (
	"net"
	"sync"
	"time"
)

// Datagram is one UDP payload the sink received.
type Datagram struct {
	At   time.Time
	From net.Addr
	Data []byte
}

// UDPSink listens on a local UDP port and records every datagram it gets,
// without answering.
type UDPSink struct {
	conn *net.UDPConn
	mu   sync.Mutex
	got  []Datagram
	more chan struct{}
	done chan struct{}
}

// NewUDPSink listens on addr (e.g. "127.0.0.1:0") and starts recording.
func NewUDPSink(addr string) (*UDPSink, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	c, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, err
	}
	s := &UDPSink{conn: c, more: make(chan struct{}, 1), done: make(chan struct{})}
	go s.loop()
	return s, nil
}

// Addr is the address the sink listens on.
func (s *UDPSink) Addr() *net.UDPAddr { return s.conn.LocalAddr().(*net.UDPAddr) }

func (s *UDPSink) loop() {
	defer close(s.done)
	buf := make([]byte, 64<<10)
	for {
		n, from, err := s.conn.ReadFrom(buf)
		if err != nil {
			return
		}
		d := Datagram{At: time.Now(), From: from, Data: append([]byte(nil), buf[:n]...)}
		s.mu.Lock()
		s.got = append(s.got, d)
		s.mu.Unlock()
		select {
		case s.more <- struct{}{}:
		default:
		}
	}
}

// Wait returns once n datagrams have arrived or timeout passes, with what
// has arrived so far (possibly fewer than n).
func (s *UDPSink) Wait(n int, timeout time.Duration) []Datagram {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		if len(s.got) >= n {
			out := append([]Datagram(nil), s.got...)
			s.mu.Unlock()
			return out
		}
		s.mu.Unlock()
		select {
		case <-s.more:
		case <-deadline.C:
			s.mu.Lock()
			defer s.mu.Unlock()
			return append([]Datagram(nil), s.got...)
		}
	}
}

// Close stops the sink.
func (s *UDPSink) Close() error {
	err := s.conn.Close()
	<-s.done
	return err
}
