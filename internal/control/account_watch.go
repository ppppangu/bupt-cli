package control

import (
	"bufio"
	"context"
	"io"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/wire"
	"github.com/fsnotify/fsnotify"
)

// Interface addresses and wake-up gaps can invalidate WebVPN's binding.
// Proxy/NAT changes invisible to the OS are caught by the shared minute probe.
func accountNetwork() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	names := make([]string, 0, len(addrs))
	for _, a := range addrs {
		names = append(names, a.String())
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

func (f *accountFeed) networkChanged() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidateNetworkLocked()
}

func (f *accountFeed) invalidateNetworkLocked() {
	if f.closed || f.signedOut {
		return
	}
	f.generation++
	f.scope++
	vpn := "unknown"
	if f.value.VPN == "not_configured" {
		vpn = "not_configured"
	}
	f.setLocked(reduceAccount(f.value, domain.AccountObservation{VPN: vpn, Platform: "unknown"}, time.Now()))
	// Clear stale facts immediately. The next eligible sample observes the new network.
}

// refreshAccount shares the in-flight request and next eligible probe across
// explicit refresh, background checks, and all transports for this profile.
func (c *Controller) refreshAccount(ctx context.Context, f *accountFeed) (domain.AccountStatus, error) {
	f.mu.Lock()
	if ch := f.inFlight; ch != nil {
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return domain.AccountStatus{}, ctx.Err()
		case <-ch:
			return f.current()
		}
	}
	now := time.Now()
	if f.closed || f.signedOut || now.Before(f.nextProbe) || now.Before(f.retryAfter) {
		f.mu.Unlock()
		return f.current()
	}
	ticket := f.ticketLocked()
	ch := make(chan struct{})
	f.inFlight = ch
	f.nextProbe = now.Add(accountProbeInterval)
	f.work.Add(1)
	profile := f.value.Profile
	f.mu.Unlock()
	defer f.work.Done()
	defer func() {
		f.mu.Lock()
		f.inFlight = nil
		close(ch)
		f.mu.Unlock()
	}()

	before := accountNetwork()
	start := time.Now().UTC()
	o := domain.AccountObservation{VPN: "unknown", Platform: "unknown"}
	if c.Configuration().Profiles[profile].GatewayURL == "" {
		o.VPN = "not_configured"
	}
	api, err := c.api(profile)
	if err == nil {
		if probe, ok := api.(interface {
			ProbeAccountStatus(context.Context) (domain.AccountObservation, error)
		}); ok {
			sampleCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			stop := context.AfterFunc(c.ctx, cancel)
			sample, e := probe.ProbeAccountStatus(sampleCtx)
			o.RetryAfter = sample.RetryAfter
			stop()
			cancel()
			if e == nil {
				o = sample
			}
			err = e
		}
	}
	if o.RetryAfter > 0 {
		f.mu.Lock()
		f.retryAfter = time.Now().Add(o.RetryAfter)
		f.mu.Unlock()
	}
	if ctx.Err() != nil {
		return domain.AccountStatus{}, ctx.Err()
	}
	if before != accountNetwork() || time.Since(start) > 20*time.Second {
		f.networkChanged()
	} else {
		ticket.observe(o, false)
	}
	v, publicationErr := f.current()
	if publicationErr != nil {
		return v, publicationErr
	}
	return v, err
}

func (f *accountFeed) checkNetwork() {
	now, current := time.Now().UTC(), accountNetwork()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.networkChecked.IsZero() && (current != f.network || (f.live > 0 && now.Sub(f.networkChecked) > 25*time.Second)) {
		f.invalidateNetworkLocked()
	}
	f.network, f.networkChecked = current, now
}

func (c *Controller) followAccount(f *accountFeed) func() {
	f.checkNetwork()
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return func() {}
	}
	f.live++
	if f.live == 1 {
		ctx, cancel := context.WithCancel(c.ctx)
		f.stop = cancel
		f.work.Add(1)
		go func() {
			defer f.work.Done()
			tick := time.NewTicker(3 * time.Second)
			defer tick.Stop()
			for ctx.Err() == nil {
				f.checkNetwork()
				_, _ = c.refreshAccount(ctx, f)
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
				}
			}
		}()
	}
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.live--
		if f.live == 0 && f.stop != nil {
			f.stop()
			f.stop = nil
		}
	}
}

func (c *Controller) watchAccount(ctx context.Context, req domain.RPCRequest, p Params, reader *bufio.Reader, conn io.ReadWriteCloser) {
	f, err := c.account(req.Profile)
	if err != nil {
		_ = streamAck(conn, req.ID, nil, err)
		return
	}
	// Watch the directory before the initial snapshot: atomic rename replaces
	// the inode. The reconciliation tick also recovers lost/overflowed events.
	var events <-chan fsnotify.Event
	var watcherErrors <-chan error
	if watcher, err := fsnotify.NewWatcher(); err == nil {
		defer watcher.Close()
		if watcher.Add(filepath.Dir(f.path)) == nil {
			events, watcherErrors = watcher.Events, watcher.Errors
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	go watchOutputStream(reader, cancel)
	if streamAck(conn, req.ID, nil, nil) != nil {
		return
	}
	fw := &wire.Writer{W: conn}
	if !p.Bool("cached") {
		leave := c.followAccount(f)
		defer leave()
	}
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var sent uint64
	for {
		if err := c.Authorize(req.ClientID, req.Method, req.Profile); err != nil {
			streamFinish(fw, 1, err)
			return
		}
		v, err := f.current()
		if err != nil {
			streamFinish(fw, 1, err)
			return
		}
		if v.Revision != sent {
			if err = fw.Frame(wire.Output, append(domain.Result(v), '\n')); err != nil {
				return
			}
			sent = v.Revision
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case e, ok := <-events:
			if !ok {
				events = nil
			} else if filepath.Clean(e.Name) != f.path {
				continue
			}
		case _, ok := <-watcherErrors:
			if !ok {
				watcherErrors = nil
			}
		}
	}
}
