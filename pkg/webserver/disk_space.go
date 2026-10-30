package webserver

func availableByteProduct(blocks, blockSize uint64) int64 {
	const maxInt64 = int64(^uint64(0) >> 1)
	if blockSize != 0 && blocks > uint64(maxInt64)/blockSize {
		return maxInt64
	}
	return int64(blocks * blockSize)
}

func availableSignedByteProduct(blocks int64, blockSize uint64) int64 {
	if blocks <= 0 {
		return 0
	}
	return availableByteProduct(uint64(blocks), blockSize)
}
