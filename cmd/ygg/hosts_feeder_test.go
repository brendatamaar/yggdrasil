package main

import (
	"testing"
	"time"

	"github.com/brendatamaar/yggdrasil/internal/hosts"
)

// mark never blocks and coalesces: any number of nudges while nobody reads
// leave exactly one pending read.
func TestHostsFeederMarkCoalesces(t *testing.T) {
	f := newHostsFeeder(nil, hosts.New(hosts.Deps{Config: hosts.ParseConfig(nil)}), nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10000; i++ {
			f.mark()
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("mark blocked with no reader")
	}
	if n := len(f.dirty); n != 1 {
		t.Fatalf("%d pending reads after 10000 marks, want 1", n)
	}
	<-f.dirty
	f.mark()
	if n := len(f.dirty); n != 1 {
		t.Fatalf("a mark after a read left %d pending", n)
	}
}

func TestHostsFeederMarkIsSafeWithoutHosts(t *testing.T) {
	var nilFeeder *hostsFeeder
	nilFeeder.mark() // must not panic
	f := newHostsFeeder(nil, nil, nil)
	f.mark()
	if len(f.dirty) != 0 {
		t.Fatal("a feeder without a hosts service queued a read")
	}
}
