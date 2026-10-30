package webserver

import (
	"math"
	"testing"
)

func TestAvailableByteProduct(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name              string
		blocks, blockSize uint64
		want              int64
	}{
		{name: "ordinary", blocks: 1_000, blockSize: 4_096, want: 4_096_000},
		{name: "zero block size", blocks: math.MaxUint64, blockSize: 0, want: 0},
		{name: "saturates overflow", blocks: math.MaxUint64, blockSize: 4_096, want: math.MaxInt64},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := availableByteProduct(test.blocks, test.blockSize); got != test.want {
				t.Fatalf("availableByteProduct(%d, %d) = %d, want %d", test.blocks, test.blockSize, got, test.want)
			}
		})
	}
}

func TestAvailableSignedByteProduct(t *testing.T) {
	t.Parallel()
	if got := availableSignedByteProduct(-1, 4_096); got != 0 {
		t.Fatalf("availableSignedByteProduct(-1, 4096) = %d, want 0", got)
	}
	if got := availableSignedByteProduct(1_000, 4_096); got != 4_096_000 {
		t.Fatalf("availableSignedByteProduct(1000, 4096) = %d, want 4096000", got)
	}
}
