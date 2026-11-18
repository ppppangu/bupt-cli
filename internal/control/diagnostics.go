package control

import (
	"context"
	"time"

	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
)

func (c *Controller) checkRoute(ctx context.Context, r domain.Route) map[string]any {
	probe, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	result := map[string]any{"check": "ssh", "instance_id": r.InstanceID, "node_id": r.NodeID, "route": r.Kind, "host_key_pinned": r.HostKey != "", "state": "unreachable"}
	cl, e := connect.DialSSH(probe, r)
	if e != nil {
		result["error"] = rpcError(e)
		return result
	}
	cl.Close()
	result["state"] = "reachable"
	return result
}
