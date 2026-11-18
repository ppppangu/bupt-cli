package client

import (
	"context"
	"encoding/json"

	"bupt.local/cli/internal/domain"
)

// Each output frame is a complete account snapshot, including on reconnect.
// Local Unix sockets and authorized remote SSH gateways carry identical frames.
func (c *Client) WatchAccount(ctx context.Context, live bool, handle func(domain.AccountStatus) error) error {
	_, err := c.Stream(ctx, "account.watch", map[string]bool{"cached": !live}, nil, accountFrame(handle))
	return err
}

type accountFrame func(domain.AccountStatus) error

func (handle accountFrame) Write(frame []byte) (int, error) {
	var status domain.AccountStatus
	if err := json.Unmarshal(frame, &status); err != nil {
		return 0, err
	}
	if err := handle(status); err != nil {
		return 0, err
	}
	return len(frame), nil
}
