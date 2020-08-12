//
// Use and distribution licensed under the Apache license version 2.
//
// See the COPYING file in the root project directory for full text.
//

package ghw

import (
	"os"
	"strings"
	"testing"
)

// nolint: gocyclo
func TestBlock(t *testing.T) {
	if _, ok := os.LookupEnv("GHW_TESTING_SKIP_BLOCK"); ok {
		t.Skip("Skipping block tests.")
	}

	ctx := contextFromEnv()

	info := &BlockInfo{}

	if err := ctx.blockFillInfo(info); err != nil {
		t.Fatalf("Expected no error creating BlockInfo, but got %v", err)
	}
	tpb := info.TotalPhysicalBytes

	if tpb < 1 {
		t.Fatalf("Expected >0 total physical bytes, got %d", tpb)
	}

	disks := info.Disks
	if len(disks) == 0 {
		t.Fatalf("Expected >0 disks. Got %d", len(disks))
	}

	d0 := disks[0]
	if d0.Name == "" {
		t.Fatalf("Expected disk name, but got \"\"")
	}
	if d0.SerialNumber == "unknown" {
		t.Fatalf("Got unknown serial number.")
	}
	if d0.SizeBytes <= 0 {
		t.Fatalf("Expected >0 disk size, but got %d", d0.SizeBytes)
	}
	if d0.Partitions == nil {
		t.Fatalf("Expected non-nil partitions, but got nil.")
	}
	if d0.PhysicalBlockSizeBytes <= 0 {
		t.Fatalf("Expected >0 sector size, but got %d", d0.PhysicalBlockSizeBytes)
	}

	if len(d0.Partitions) > 0 {
		p0 := d0.Partitions[0]
		if p0 == nil {
			t.Fatalf("Expected non-nil partition, but got nil.")
		}
		if !strings.HasPrefix(p0.Name, d0.Name) {
			t.Fatalf("Expected partition name to begin with disk name but "+
				"got %s does not begin with %s", p0.Name, d0.Name)
		}
	}

	for _, p := range d0.Partitions {
		if p.SizeBytes <= 0 {
			t.Fatalf("Expected >0 partition size, but got %d", p.SizeBytes)
		}
		if p.Disk != d0 {
			t.Fatalf("Expected disk to be the same as d0 but got %v", p.Disk)
		}
	}
}
