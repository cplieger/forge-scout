package main

import (
	"net"
	"strings"
	"testing"
)

func TestRunRefusesUnknownCommand(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"unknown":         {"trigger"},
		"health and more": {"health", "now"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p, _, stderr := newProc(nil, args...)
			if code := run(t.Context(), p); code != 2 {
				t.Errorf("run = %d, want 2", code)
			}
			if got := stderr.String(); !strings.Contains(got, "unknown command "+`"`+strings.Join(args, " ")+`"`) || !strings.Contains(got, "health") {
				t.Errorf("stderr = %q, want the command and the valid ones named", got)
			}
		})
	}
}

func TestHealthWithNoServer(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	p, _, stderr := newProc(map[string]string{"OPS_LISTEN_ADDR": addr}, "health")
	if code := run(t.Context(), p); code != 1 {
		t.Errorf("health = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), addr) {
		t.Errorf("stderr = %q, want the probed address", stderr)
	}
}

func TestHealthRefusesBadAddress(t *testing.T) {
	t.Parallel()
	p, _, stderr := newProc(map[string]string{"OPS_LISTEN_ADDR": "no-port"}, "health")
	if code := run(t.Context(), p); code != 1 {
		t.Errorf("health = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "OPS_LISTEN_ADDR") {
		t.Errorf("stderr = %q, want OPS_LISTEN_ADDR named", stderr)
	}
}
