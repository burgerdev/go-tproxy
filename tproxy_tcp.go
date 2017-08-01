// +build linux

package tproxy

import (
	"fmt"
	"net"
	"syscall"
	"strings"
	"os"
	"strconv"
)

// TProxyListener describes a TCP Listener
// with the Linux IP_TRANSPARENT option defined
// on the listening socket
type TProxyListener struct {
	base net.Listener
}

// Accept waits for and returns
// the next connection to the listener.
//
// This command wraps the AcceptTProxy
// method of the TProxyListener
func (listener *TProxyListener) Accept() (net.Conn, error) {
	return listener.AcceptTProxy()
}

// AcceptTProxy will accept a TCP connection
// and wrap it to a TProxy connection to provide
// TProxy functionality
func (listener *TProxyListener) AcceptTProxy() (*TProxyConn, error) {
	tcpConn, err := listener.base.(*net.TCPListener).AcceptTCP()
	if err != nil {
		return nil, err
	}

	return &TProxyConn{TCPConn: tcpConn}, nil
}

// Addr returns the network address
// the listener is accepting connections
// from
func (listener *TProxyListener) Addr() net.Addr {
	return listener.base.Addr()
}

// Close will close the listener from accepting
// any more connections. Any blocked connections
// will unblock and close
func (listener *TProxyListener) Close() error {
	return listener.base.Close()
}

// ListenTCP will construct a new TCP listener
// socket with the Linux IP_TRANSPARENT option
// set on the underlying socket
func ListenTCP(network string, laddr *net.TCPAddr) (net.Listener, error) {
	listener, err := net.ListenTCP(network, laddr)
	if err != nil {
		return nil, err
	}

	fileDescriptorSource, err := listener.File()
	if err != nil {
		return nil, &net.OpError{Op: "listen", Net: network, Source: nil, Addr: laddr, Err: fmt.Errorf("get file descriptor: %s", err)}
	} else {
		defer fileDescriptorSource.Close()
	}

	fileDescriptor := int(fileDescriptorSource.Fd())
	if err = syscall.SetsockoptInt(fileDescriptor, syscall.SOL_IP, syscall.IP_TRANSPARENT, 1); err != nil {
		syscall.Close(fileDescriptor)
		return nil, &net.OpError{Op: "listen", Net: network, Source: nil, Addr: laddr, Err: fmt.Errorf("set socket option: IP_TRANSPARENT: %s", err)}
	}

	return &TProxyListener{listener}, nil
}

// TProxyConn describes a connection
// accepted by the TProxy listener.
//
// It is simply a TCP connection with
// the ability to dial a connection to
// the original destination while assuming
// the IP address of the client
type TProxyConn struct {
	*net.TCPConn
}

// DialOriginalDestination will open a
// TCP connection to the original destination
// that the client was trying to connect to before
// being intercepted.
//
// When `dontAssumeRemote` is false, the connection will
// originate from the IP address and port that the client
// used when making the connection. Otherwise, when true,
// the connection will originate from an IP address and port
// assigned by the Linux kernel that is owned by the
// operating system
func (conn *TProxyConn) DialOriginalDestination(dontAssumeRemote bool) (*net.TCPConn, error) {
	remoteSocketAddress, err := tcpAddrToSocketAddr(conn.LocalAddr().(*net.TCPAddr))
	if err != nil {
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("build destination socket address: %s", err)}
	}

	localSocketAddress, err := tcpAddrToSocketAddr(conn.RemoteAddr().(*net.TCPAddr))
	if err != nil {
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("build local socket address: %s", err)}
	}

	fileDescriptor, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err != nil {
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("socket open: %s", err)}
	}

	if err = syscall.SetsockoptInt(fileDescriptor, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		syscall.Close(fileDescriptor)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("set socket option: SO_REUSEADDR: %s", err)}
	}

	if err = syscall.SetsockoptInt(fileDescriptor, syscall.SOL_IP, syscall.IP_TRANSPARENT, 1); err != nil {
		syscall.Close(fileDescriptor)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("set socket option: IP_TRANSPARENT: %s", err)}
	}

	if err = syscall.SetNonblock(fileDescriptor, true); err != nil {
		syscall.Close(fileDescriptor)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("set socket option: SO_NONBLOCK: %s", err)}
	}

	if !dontAssumeRemote {
		if err = syscall.Bind(fileDescriptor, localSocketAddress); err != nil {
			syscall.Close(fileDescriptor)
			return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("socket bind: %s", err)}
		}
	}

	if err = syscall.Connect(fileDescriptor, remoteSocketAddress); err != nil && !strings.Contains(err.Error(), "operation now in progress") {
		syscall.Close(fileDescriptor)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("socket connect: %s", err)}
	}

	remoteConn, err := net.FileConn(os.NewFile(uintptr(fileDescriptor), fmt.Sprintf("net-tcp-dial-%s", conn.LocalAddr().String())))
	if err != nil {
		syscall.Close(fileDescriptor)
		return nil, &net.OpError{Op: "dial", Err: fmt.Errorf("convert file descriptor to connection: %s", err)}
	}

	return remoteConn.(*net.TCPConn), nil
}

// tcpAddToSockerAddr will convert a TCPAddr
// into a Sockaddr that may be used when
// connecting and binding sockets
func tcpAddrToSocketAddr(addr *net.TCPAddr) (syscall.Sockaddr, error) {
	switch {
	case addr.IP.To4() != nil:
		ip := [4]byte{}
		copy(ip[:], addr.IP.To4())

		return &syscall.SockaddrInet4{Addr: ip, Port: addr.Port}, nil

	default:
		ip := [16]byte{}
		copy(ip[:], addr.IP.To16())

		zoneId, err := strconv.ParseUint(addr.Zone, 10, 32)
		if err != nil {
			return nil, err
		}

		return &syscall.SockaddrInet6{Addr: ip, Port: addr.Port, ZoneId: uint32(zoneId)}, nil
	}
}