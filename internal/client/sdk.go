package client

import (
	"bupt.local/cli/internal/domain"
	"context"
)

func (c *Client) Status(ctx context.Context, target string, cached bool) (domain.StatusReport, error) {
	var result domain.StatusReport
	e := c.Call(ctx, "status.get", map[string]any{"target": target, "cached": cached}, &result)
	return result, e
}

// These typed operations are shared by command, script and terminal UI clients.
// Interactive rendering and credential input deliberately stay outside the SDK.
type LoginOptions struct {
	ForceGateway bool   `json:"force_gateway,omitempty"`
	SSOMethod    string `json:"sso_method,omitempty"`
}

func (c *Client) StartLogin(ctx context.Context, o LoginOptions) (domain.AuthChallenge, error) {
	var v domain.AuthChallenge
	e := c.Call(ctx, "auth.start", o, &v)
	return v, e
}
func (c *Client) PollLogin(ctx context.Context) (domain.AuthChallenge, error) {
	var v domain.AuthChallenge
	e := c.Call(ctx, "auth.poll", nil, &v)
	return v, e
}
func (c *Client) SubmitLoginPassword(ctx context.Context, challengeID, username, password, captcha string) (domain.AuthChallenge, error) {
	var v domain.AuthChallenge
	err := c.Call(ctx, "auth.sso.submit", map[string]string{"challenge_id": challengeID, "username": username, "password": password, "captcha_code": captcha}, &v)
	return v, err
}
func (c *Client) AuthStatus(ctx context.Context) (domain.AuthStatus, error) {
	var v domain.AuthStatus
	e := c.Call(ctx, "auth.status", nil, &v)
	return v, e
}
func (c *Client) ListInstances(ctx context.Context) ([]domain.Instance, error) {
	var v []domain.Instance
	e := c.Call(ctx, "instance.list", nil, &v)
	return v, e
}
func (c *Client) ListRuns(ctx context.Context) ([]domain.Run, error) {
	var v []domain.Run
	e := c.Call(ctx, "job.list", nil, &v)
	return v, e
}
func (c *Client) GetRun(ctx context.Context, target string) (domain.Run, error) {
	var v domain.Run
	e := c.Call(ctx, "run.get", map[string]string{"target": target}, &v)
	return v, e
}
func (c *Client) GetTask(ctx context.Context, id string) (domain.TaskRecord, error) {
	var v domain.TaskRecord
	e := c.Call(ctx, "task.get", map[string]string{"target": id}, &v)
	return v, e
}
func (c *Client) GetRoute(ctx context.Context, target, node string) (domain.Route, error) {
	var v domain.Route
	e := c.Call(ctx, "route.get", map[string]any{"target": target, "node": node, "repair": false}, &v)
	return v, e
}

// WaitRun observes a durable run; cancellation ends only this wait.
func (c *Client) WaitRun(ctx context.Context, target string, finished bool) (domain.Run, error) {
	condition := "ready"
	if finished {
		condition = "finished"
	}
	var run domain.Run
	err := c.Call(ctx, "run.wait", map[string]any{"target": target, "condition": condition}, &run)
	return run, err
}
