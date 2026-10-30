//go:build windows

package webserver

import "golang.org/x/sys/windows"

func availableDiskBytes(path string) (int64, error) {
	directory, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(directory, &available, nil, nil); err != nil {
		return 0, err
	}
	return availableByteProduct(available, 1), nil
}
