//go:build netbsd || solaris

package webserver

import "golang.org/x/sys/unix"

func availableDiskBytes(path string) (int64, error) {
	var stats unix.Statvfs_t
	if err := unix.Statvfs(path, &stats); err != nil {
		return 0, err
	}
	blockSize := uint64(stats.Frsize)
	if blockSize == 0 {
		blockSize = uint64(stats.Bsize)
	}
	return availableByteProduct(uint64(stats.Bavail), blockSize), nil
}
