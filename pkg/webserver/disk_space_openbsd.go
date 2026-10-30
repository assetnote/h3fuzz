//go:build openbsd

package webserver

import "golang.org/x/sys/unix"

func availableDiskBytes(path string) (int64, error) {
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return 0, err
	}
	return availableSignedByteProduct(stats.F_bavail, uint64(stats.F_bsize)), nil
}
