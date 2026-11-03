// Command swapimport converts a raw on-chain Uniswap v3 swap dump into the
// format internal/history expects, writing it where the backtest store looks
// for it.
//
// A raw dump (the kind a subgraph or log-scraper emits) carries amount0 and
// amount1 as unscaled integers and often names the time column
// blockTimestamp. The store wants human token units and its own header. Only
// the amounts change; liquidity, sqrtPriceX96, tick and price already match
// and pass through verbatim.
//
//	go run ./cmd/swapimport -in pool_swaps_eth_usdc_40982309_44727310.csv
//	go run ./cmd/swapimport -in dump.csv -pool v3-eth-usdc-005-arb -out ../data
//
// The input streams through, so dump size is not bound by memory.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"amazing/internal/history"
)

func main() {
	in := flag.String("in", "", "raw swap CSV to convert (required)")
	pool := flag.String("pool", "v3-eth-usdc-005", "amazing pool id the dump belongs to")
	outDir := flag.String("out", "data", "directory the history store reads")
	force := flag.Bool("force", false, "overwrite an existing CSV instead of stopping")
	flag.Parse()

	if err := run(*in, *pool, *outDir, *force); err != nil {
		fmt.Fprintln(os.Stderr, "swapimport:", err)
		os.Exit(1)
	}
}

func run(in, pool, outDir string, force bool) error {
	if in == "" {
		flag.Usage()
		return fmt.Errorf("-in is required")
	}
	spec, ok := history.SpecFor(pool)
	if !ok {
		return fmt.Errorf("unknown pool id %q (known: %s)", pool, strings.Join(knownPools(), ", "))
	}

	src, err := os.Open(in)
	if err != nil {
		return err
	}
	defer src.Close()

	dst := filepath.Join(outDir, "swaps_"+spec.FileKey()+".csv")
	if _, err := os.Stat(dst); err == nil && !force {
		return fmt.Errorf("%s already exists; move it aside or pass -force", dst)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	fmt.Printf("pool   %s  %s/%s  chain %d  decimals %d/%d\n",
		pool, spec.Token0Sym, spec.Token1Sym, spec.ChainID, spec.Decimals0, spec.Decimals1)
	fmt.Printf("in     %s\n", in)
	fmt.Printf("out    %s\n", dst)

	// Write to a temp file and rename, so a failure part way through cannot
	// leave the store pointing at a half-converted CSV.
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	t0 := time.Now()
	st, convErr := history.ConvertRawCSV(bufio.NewReaderSize(src, 1<<20), bw, spec)
	if convErr == nil {
		if err := bw.Flush(); err != nil {
			convErr = err
		}
	}
	if cerr := f.Close(); convErr == nil {
		convErr = cerr
	}
	if convErr != nil {
		os.Remove(tmp)
		return convErr
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}

	fi, _ := os.Stat(dst)
	fmt.Printf("\nrows   %d converted, %d skipped, %d out of order\n", st.Rows, st.Skipped, st.Unsorted)
	fmt.Printf("window %s .. %s UTC\n",
		time.Unix(st.From, 0).UTC().Format("2006-01-02 15:04:05"),
		time.Unix(st.To, 0).UTC().Format("2006-01-02 15:04:05"))
	if fi != nil {
		fmt.Printf("size   %.1f MiB in %s\n", float64(fi.Size())/(1<<20), time.Since(t0).Round(time.Millisecond))
	}
	if st.Unsorted > 0 {
		fmt.Fprintf(os.Stderr, "\nwarning: %d rows are out of timestamp order; the store assumes\n"+
			"sorted rows for gap detection and daily closes\n", st.Unsorted)
	}
	if st.Rows == 0 {
		return fmt.Errorf("no rows converted")
	}
	return nil
}

func knownPools() []string {
	var ids []string
	for _, b := range history.Bindings() {
		ids = append(ids, b.ID)
	}
	return ids
}
