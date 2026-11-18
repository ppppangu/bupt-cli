package client

import (
	"bufio"
	"encoding/json"
	"errors"
	"testing"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/wire"
)

func TestWatchAccountDeliversFullFramesOnUnixSocket(t *testing.T) {
	c, listener := localFixture(t)
	served := make(chan error, 1)
	go func() {
		n, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer n.Close()
		var req domain.RPCRequest
		if err = wire.ReadJSON(bufio.NewReader(n), &req); err != nil {
			served <- err
			return
		}
		var p struct{ Cached bool }
		err = json.Unmarshal(req.Params, &p)
		if err != nil || req.Method != "account.watch" || req.Profile != "bupt" || !p.Cached {
			served <- errors.New("invalid account request")
			return
		}
		if err = wire.WriteJSON(n, domain.RPCResponse{ID: req.ID}); err != nil {
			served <- err
			return
		}
		w := &wire.Writer{W: n}
		for rev := uint64(1); rev <= 2; rev++ {
			v := domain.AccountStatus{Epoch: "fixture", Revision: rev, Profile: "bupt", VPN: "expired", Platform: "expired"}
			if err = w.Frame(wire.Output, append(domain.Result(v), '\n')); err != nil {
				served <- err
				return
			}
		}
		served <- nil
	}()
	done := errors.New("done observing")
	var last uint64
	err := c.WatchAccount(t.Context(), false, func(v domain.AccountStatus) error {
		if v.Epoch != "fixture" || v.Profile != "bupt" || v.Revision != last+1 {
			t.Fatalf("invalid wire projection: %+v", v)
		}
		last = v.Revision
		if last == 2 {
			return done
		}
		return nil
	})
	if !errors.Is(err, done) || last != 2 {
		t.Fatal(err, last)
	}
	if err = <-served; err != nil {
		t.Fatal(err)
	}
}
