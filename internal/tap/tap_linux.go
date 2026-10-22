package tap

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type ifreqFlags struct {
	Name  [unix.IFNAMSIZ]byte
	Flags uint16
	_     [22]byte
}

type ifreqHW struct {
	Name   [unix.IFNAMSIZ]byte
	Family uint16
	Data   [14]byte
	_      [8]byte
}

type ifreqAddr struct {
	Name   [unix.IFNAMSIZ]byte
	Family uint16
	Port   uint16
	Addr   [4]byte
	_      [16]byte
}

func ioctl(fd int, req uint, arg unsafe.Pointer) error {
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(req), uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// open creates the TAP interface and configures it (MAC, IPv4, up).
func open(p Port) (*os.File, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("/dev/net/tun: %w (o container precisa de devices: [/dev/net/tun] e cap_add: [NET_ADMIN])", err)
	}
	var fr ifreqFlags
	copy(fr.Name[:], p.Name)
	fr.Flags = unix.IFF_TAP | unix.IFF_NO_PI
	if err := ioctl(fd, unix.TUNSETIFF, unsafe.Pointer(&fr)); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("criar %s: %w", p.Name, err)
	}
	s, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	defer unix.Close(s)
	if len(p.MAC) == 6 {
		var hw ifreqHW
		copy(hw.Name[:], p.Name)
		hw.Family = unix.ARPHRD_ETHER
		copy(hw.Data[:], p.MAC)
		if err := ioctl(s, unix.SIOCSIFHWADDR, unsafe.Pointer(&hw)); err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("MAC de %s: %w", p.Name, err)
		}
	}
	if p.Addr != nil {
		// The first address on the port, the others on its aliases
		// (eth1:1, eth1:2…).
		for k, nw := range append([]*net.IPNet{p.Addr}, p.More...) {
			name := p.Name
			if k > 0 {
				name = fmt.Sprintf("%s:%d", p.Name, k)
			}
			set := func(req uint, ip net.IP) error {
				var a ifreqAddr
				copy(a.Name[:], name)
				a.Family = unix.AF_INET
				copy(a.Addr[:], ip.To4())
				return ioctl(s, req, unsafe.Pointer(&a))
			}
			if err := set(unix.SIOCSIFADDR, nw.IP); err != nil {
				unix.Close(fd)
				return nil, fmt.Errorf("endereço de %s: %w", name, err)
			}
			if err := set(unix.SIOCSIFNETMASK, net.IP(nw.Mask)); err != nil {
				unix.Close(fd)
				return nil, fmt.Errorf("máscara de %s: %w", name, err)
			}
		}
	}
	var up ifreqFlags
	copy(up.Name[:], p.Name)
	if err := ioctl(s, unix.SIOCGIFFLAGS, unsafe.Pointer(&up)); err == nil {
		up.Flags |= unix.IFF_UP | unix.IFF_RUNNING
		err = ioctl(s, unix.SIOCSIFFLAGS, unsafe.Pointer(&up))
		if err != nil {
			unix.Close(fd)
			return nil, fmt.Errorf("ligar %s: %w", p.Name, err)
		}
	}
	return os.NewFile(uintptr(fd), "/dev/net/tun:"+p.Name), nil
}

// Run creates the ports and moves frames until ctx ends.
func Run(ctx context.Context, relay string, ports []Port, logf func(string, ...any)) error {
	host, err := RelayHost(relay)
	if err != nil {
		return err
	}
	errc := make(chan error, len(ports))
	for _, p := range ports {
		f, err := open(p)
		if err != nil {
			return err
		}
		raddr := &net.UDPAddr{IP: net.ParseIP(host), Port: p.Relay}
		if raddr.IP == nil {
			ips, err := net.LookupIP(host)
			if err != nil || len(ips) == 0 {
				return fmt.Errorf("relay %s: %v", host, err)
			}
			raddr.IP = ips[0]
		}
		c, err := net.DialUDP("udp4", nil, raddr)
		if err != nil {
			return fmt.Errorf("relay %s: %w", raddr, err)
		}
		addr := "sem IP"
		if p.Addr != nil {
			ones, _ := p.Addr.Mask.Size()
			addr = fmt.Sprintf("%s/%d", p.Addr.IP, ones)
			for _, m := range p.More {
				o, _ := m.Mask.Size()
				addr += fmt.Sprintf(" %s/%d", m.IP, o)
			}
		}
		logf("%s: %s, relay %s", p.Name, addr, raddr)
		go func() { // the TAP to the relay
			buf := make([]byte, 65536)
			for {
				n, err := f.Read(buf)
				if err != nil {
					errc <- fmt.Errorf("%s: %w", p.Name, err)
					return
				}
				c.Write(buf[:n])
			}
		}()
		go func() { // the relay to the TAP
			buf := make([]byte, 65536)
			for {
				n, err := c.Read(buf)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					time.Sleep(200 * time.Millisecond) // relay not up yet (ICMP refused)
					continue
				}
				if n > 0 {
					f.Write(buf[:n])
				}
			}
		}()
		go func() { // keepalive: the relay learns where this port is
			t := time.NewTicker(5 * time.Second)
			defer t.Stop()
			c.Write(nil)
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					c.Write(nil)
				}
			}
		}()
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errc:
		return err
	}
}
