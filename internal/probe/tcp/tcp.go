// Package tcp is the bare TCP connect engine (FR-AC-4): a plain connect()
// to each configured port of each known host, closed at once with a reset
// without sending anything. Never SYN scanning (AGENTS.md rule 7). Every
// result becomes a tcp_connect observation: OPEN, REFUSED, TIMEOUT or
// UNREACHABLE, or LOCAL_ERROR when the kernel has no direct route and source
// address for the target on the interface (then nothing is dialled).
//
// A connect is one start of the budget (ADR 0011), whatever the kernel
// sends for it: OPEN sends SYN, ACK and RST (3 packets); REFUSED one SYN
// (the target's RST ends it); TIMEOUT the SYN and at most 2 kernel
// retransmissions (platform.SYNRetries), never data.
package tcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/otfabric/lan-tracker/internal/config"
	"github.com/otfabric/lan-tracker/internal/observation"
	"github.com/otfabric/lan-tracker/internal/platform"
	"github.com/otfabric/lan-tracker/internal/probe"
)

// Engine dials through the platform transmitter.
type Engine struct {
	TX platform.Transmitter
}

// Protocol implements probe.Engine.
func (Engine) Protocol() probe.Protocol { return probe.TCP }

// Classify maps a connect error to a service state (FR-AC-5). LOCAL_ERROR
// is a failure here (no permission, interface gone, out of descriptors,
// no usable source address), not an answer from the target.
func Classify(err error) observation.ServiceState {
	var ne net.Error
	switch {
	case err == nil:
		return observation.ServiceOpen
	case errors.Is(err, syscall.ECONNREFUSED), errors.Is(err, syscall.ECONNRESET):
		return observation.ServiceRefused
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return observation.ServiceTimeout
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTDOWN):
		return observation.ServiceUnreachable
	}
	return observation.ServiceLocalError
}

// LocalDetail names a local connect failure for the result's detail.
func LocalDetail(err error) string {
	switch {
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES):
		return "permission denied"
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return "no usable source address"
	case errors.Is(err, syscall.ENODEV), errors.Is(err, syscall.ENXIO):
		return "interface missing"
	case errors.Is(err, syscall.EMFILE), errors.Is(err, syscall.ENFILE), errors.Is(err, syscall.ENOBUFS), errors.Is(err, syscall.ENOMEM):
		return "out of resources"
	}
	return "local error"
}

// Run implements probe.Engine. Probes go port by port across the hosts, so
// consecutive connects reach different hosts. A connect that fails locally
// (LOCAL_ERROR) is recorded with its detail, so the endpoint shows why it
// was not tested, and fails the pass; it says nothing about the target.
func (e Engine) Run(ctx context.Context, p probe.Pass) ([]probe.Result, error) {
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		results  []probe.Result
		runErr   error
		localErr error
	)
	add := func(r probe.Result) {
		mu.Lock()
		results = append(results, r)
		mu.Unlock()
	}
	// A check needs a direct route on the interface and a source address:
	// SO_BINDTODEVICE picks the interface, it does not make a destination
	// directly connected. Without one nothing is sent and no start is
	// spent; the endpoint records why (LOCAL_ERROR), once per pass.
	unroutable := map[netip.Addr]string{}
	for _, target := range p.Targets {
		rt, err := e.TX.Route(p.Link.Name, target)
		switch {
		case err != nil:
			unroutable[target] = "no route via " + p.Link.Name
		case !rt.Direct(p.Link.Name):
			unroutable[target] = rt.Problem(p.Link.Name)
		}
	}
loop:
	for _, tg := range p.TCP {
		for _, target := range p.Targets {
			if why, ok := unroutable[target]; ok {
				add(probe.Result{Target: target, Port: tg.Port, State: string(observation.ServiceLocalError)})
				p.Emit(observation.Observation{
					Time: p.Now(), Source: observation.TCPConnect, Interface: p.Link.Name, IP: target,
					Service: &observation.ServiceResult{Proto: "tcp", Port: tg.Port, State: observation.ServiceLocalError},
					Meta:    map[string]string{observation.MetaDetail: why},
				})
				continue
			}
			release, err := p.Budget.Acquire(ctx, probe.TCP, p.Link.Name, target)
			if errors.Is(err, probe.ErrRefused) {
				add(probe.Result{Target: target, Port: tg.Port, State: probe.Blocked})
				continue
			}
			if err != nil {
				runErr = err
				break loop
			}
			wg.Add(1)
			go func(target netip.Addr, tg config.TCPTarget) {
				defer wg.Done()
				defer release()
				addr := netip.AddrPortFrom(target, uint16(tg.Port)) //nolint:gosec // ports are validated 1–65535
				state, ok, err := e.connect(ctx, p.Link.Name, addr, timeout(tg))
				if !ok {
					return
				}
				add(probe.Result{Target: target, Port: tg.Port, State: string(state)})
				o := observation.Observation{
					Time: p.Now(), Source: observation.TCPConnect, Interface: p.Link.Name, IP: target,
					Service: &observation.ServiceResult{Proto: "tcp", Port: tg.Port, State: state},
				}
				if state == observation.ServiceLocalError {
					o.Meta = map[string]string{observation.MetaDetail: LocalDetail(err)}
					mu.Lock()
					if localErr == nil {
						localErr = fmt.Errorf("tcp on %s: connect %s: %w", p.Link.Name, addr, err)
					}
					mu.Unlock()
				}
				p.Emit(o)
			}(target, tg)
		}
	}
	wg.Wait()
	if runErr == nil {
		runErr = localErr
	}
	return results, runErr
}

func timeout(tg config.TCPTarget) time.Duration {
	if d := tg.Timeout.D(); d > 0 {
		return d
	}
	return probe.DefaultTCPTimeout
}

// connect dials and closes at once. ok is false when the pass was cancelled
// mid-connect: that is no result at all.
func (e Engine) connect(ctx context.Context, iface string, addr netip.AddrPort, d time.Duration) (observation.ServiceState, bool, error) {
	conn, err := e.TX.DialTCP(ctx, iface, addr, d)
	if conn != nil {
		abort(conn)
	}
	if ctx.Err() != nil {
		return "", false, nil
	}
	return Classify(err), true, err
}

// abort closes a bare connect with SO_LINGER 0: the close is a RST, so the
// connect sends exactly SYN, ACK and RST, and neither side keeps the
// connection in TIME_WAIT or CLOSE_WAIT. Only for connects that carried no
// data.
func abort(c net.Conn) {
	if l, ok := c.(interface{ SetLinger(sec int) error }); ok {
		_ = l.SetLinger(0)
	}
	_ = c.Close()
}
