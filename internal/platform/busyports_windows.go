//go:build windows

package platform

import "encoding/binary"

// allListeningPorts lists every TCP port in LISTEN state, IPv4 and IPv6, from
// the kernel's listener table, whatever process owns it.
func allListeningPorts() ([]int, error) {
	var ports []int
	for _, t := range []struct {
		af         uint32
		size, port int
	}{
		{2, row4Size, row4Port}, // AF_INET
		{afInet6, row6Size, row6Port},
	} {
		table, err := tcpListenTable(t.af)
		if err != nil {
			return nil, err
		}
		ports = append(ports, tableAllPorts(table, t.size, t.port)...)
	}
	return ports, nil
}

// tableAllPorts returns the local port of every row of a listener table.
func tableAllPorts(table []byte, rowSize, portOff int) []int {
	if len(table) < 4 {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(table))
	var ports []int
	for i := range n {
		row := table[4+i*rowSize:]
		if len(row) < rowSize {
			break
		}
		ports = append(ports, int(binary.BigEndian.Uint16(row[portOff:])))
	}
	return ports
}
