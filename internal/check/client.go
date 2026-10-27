package check

import (
	"context"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"etcd-backup-keeper/internal/apperr"
	"etcd-backup-keeper/internal/config"
)

type etcdConnection struct {
	client   *clientv3.Client
	endpoint string
}

func newConnection(ctx, dialCtx context.Context, cfg config.Config, index int) (connection, error) {
	clientConfig, err := cfg.ClientConfig(ctx, index)
	if err != nil {
		return nil, err
	}
	deadline, ok := dialCtx.Deadline()
	if !ok || dialCtx.Err() != nil {
		return nil, apperr.New("remote_unavailable", "candidate Status is unavailable")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return nil, apperr.New("remote_unavailable", "candidate Status is unavailable")
	}
	// New can authenticate synchronously. Bound that work with the remaining
	// dial budget, while retaining the request context as client lifetime.
	clientConfig.DialTimeout = remaining
	client, err := clientv3.New(clientConfig)
	if err != nil {
		return nil, apperr.New("remote_unavailable", "candidate Status is unavailable")
	}
	return &etcdConnection{client: client, endpoint: cfg.Endpoints[index]}, nil
}

func (c *etcdConnection) Status(ctx context.Context) (status, error) {
	response, err := c.client.Status(ctx, c.endpoint)
	if err != nil {
		return status{}, apperr.New("remote_unavailable", "candidate Status is unavailable")
	}
	if response == nil || response.Header == nil {
		return status{}, nil
	}
	return status{header: true, clusterID: response.Header.ClusterId, memberID: response.Header.MemberId, version: response.Version}, nil
}

func (c *etcdConnection) Close() { c.client.Close() }
