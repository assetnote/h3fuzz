//go:build plan9 || js || wasip1

package webserver

func availableDiskBytes(string) (int64, error) { return -1, nil }
