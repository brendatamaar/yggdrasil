//go:build windows

package platform

import (
	"encoding/binary"
	"slices"
	"testing"
)

func TestTableAllPortsIgnoresOwner(t *testing.T) {
	table := make([]byte, 4+2*row4Size)
	binary.LittleEndian.PutUint32(table, 2)
	binary.BigEndian.PutUint16(table[4+row4Port:], 20001)
	binary.LittleEndian.PutUint32(table[4+row4PID:], 99)
	binary.BigEndian.PutUint16(table[4+row4Size+row4Port:], 20002)
	got := tableAllPorts(table, row4Size, row4Port)
	if !slices.Equal(got, []int{20001, 20002}) {
		t.Fatalf("got %v", got)
	}
	if tableAllPorts(table[:3], row4Size, row4Port) != nil || len(tableAllPorts(table[:30], row4Size, row4Port)) != 1 {
		t.Fatal("short tables must be read safely")
	}
}
