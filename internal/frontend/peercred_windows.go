package frontend

import "net"

// peerCredsSupported: this platform reports no peer credentials on unix sockets.
const peerCredsSupported = 0

// peerUID has no kernel source of peer credentials here: Windows AF_UNIX
// sockets carry none, so Start refuses the control socket. A platform
// that is neither Linux, macOS nor Windows has no peerUID at all and does
// not build, rather than run the control plane unchecked.
func peerUID(net.Conn) (int, error) {
	return 0, errPeerCredUnsupported
}
