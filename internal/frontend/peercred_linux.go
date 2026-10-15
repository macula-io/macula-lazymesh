//go:build linux

package frontend

import (
	"fmt"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// peerCredsSupported: Linux reports peer credentials on unix sockets.
const peerCredsSupported = 1

// peerUID returns the uid of the process on the other end of a unix
// socket connection, read from the kernel with SO_PEERCRED. The kernel
// records it at connect time, so the peer cannot forge it.
func peerUID(conn net.Conn) (int, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("frontend: peer credentials need a unix connection, got %T", conn)
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("frontend: raw conn: %w", err)
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, fmt.Errorf("frontend: raw conn control: %w", err)
	}
	if credErr != nil {
		return 0, fmt.Errorf("frontend: SO_PEERCRED: %w", credErr)
	}
	return int(cred.Uid), nil
}

// ownerUID returns the uid that owns the file fi describes.
func ownerUID(fi os.FileInfo) int {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(st.Uid)
}
