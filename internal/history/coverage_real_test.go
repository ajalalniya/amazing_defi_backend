package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRealCoverageIsCheap(t *testing.T) {
	path := filepath.Join("../../data", "swaps_"+BaseETHUSDC.FileKey()+".csv")
	fi, err := os.Stat(path)
	if err != nil || fi.Size() < 100<<20 {
		t.Skip("no full swap dump in ../../data; run cmd/swapimport first")
	}
	t0 := time.Now()
	cov := New("../../data", nil, nil).Coverage()
	el := time.Since(t0)
	b, _ := json.MarshalIndent(cov, "", "  ")
	t.Logf("%.0f MiB file, Coverage() took %s\n%s", float64(fi.Size())/(1<<20), el.Round(time.Microsecond), b)
	if el > 500*time.Millisecond {
		t.Errorf("Coverage() took %s; it should not be parsing the body", el)
	}
}
