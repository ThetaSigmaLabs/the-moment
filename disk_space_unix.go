// SPDX-License-Identifier: GPL-3.0-or-later
// The Moment — derived from FilaBridge (https://github.com/needo37/filabridge)
// Copyright (C) 2025 needo37 / Copyright (C) 2026 maudy2u

//go:build !windows

package main

import "syscall"

// availableDiskSpace returns the number of free bytes available to the current user at path.
func availableDiskSpace(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return -1, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}

// totalDiskSpace returns the total capacity in bytes of the filesystem containing path.
func totalDiskSpace(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return -1, err
	}
	return int64(stat.Blocks) * int64(stat.Bsize), nil
}
