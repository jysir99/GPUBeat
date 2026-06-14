package main

import "testing"

func TestParseGPUDataAllowsGenericHostWithoutGPU(t *testing.T) {
	raw := `===SYSINFO===
CPU:12.5
MEM:1024/4096/25
LOAD:0.10 0.20 0.30
===DISKS===
/,20480,10240,50%
===NET===
RX:123456
TX:654321
===GPU===
===PROCESSES===
===USERS===
root 1
`
	data := ParseGPUData(raw, "cloud-1", "203.0.113.10")
	if data.Status != "online" {
		t.Fatalf("status = %q, want online", data.Status)
	}
	if len(data.GPUs) != 0 {
		t.Fatalf("GPUs = %d, want 0", len(data.GPUs))
	}
	if data.Sys.CPUUsage != 12.5 || data.Sys.MemPercent != 25 {
		t.Fatalf("sys = %#v", data.Sys)
	}
	if len(data.Disks) != 1 || data.Disks[0].Mount != "/" || data.Disks[0].UsePercent != 50 {
		t.Fatalf("disks = %#v", data.Disks)
	}
	if data.Net.RXBytes != 123456 || data.Net.TXBytes != 654321 {
		t.Fatalf("net = %#v", data.Net)
	}
}
