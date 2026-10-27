package check

import (
	"context"
	"testing"
	"time"

	"etcd-backup-keeper/internal/config"
)

// These cases must reject before client.New: no local or external server and
// no certificate fixtures are needed to check the mandatory dial budget.
func TestNewConnectionRejectsMissingOrEndedBudget(t *testing.T) {
	cfg := testConfig(t)
	ended, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer cancel()
	for _, dial := range []context.Context{context.Background(), ended} {
		client, err := newConnection(context.Background(), dial, cfg, 0)
		if err == nil || client != nil {
			t.Fatalf("unbounded connection accepted: %v %v", client, err)
		}
	}
}

func TestProbeRetainsEarlierRequestDeadline(t *testing.T) {
	cfg := testConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	cfg.RequestTimeout = 2 * time.Hour
	cfg.DialTimeout = 3 * time.Hour
	parentDeadline, _ := ctx.Deadline()
	f := &fakeConnection{info: validStatus()}
	_, err := run(ctx, cfg, true, func(lifetime, dial context.Context, _ config.Config, _ int) (connection, error) {
		for _, bounded := range []context.Context{lifetime, dial} {
			deadline, ok := bounded.Deadline()
			if !ok || !deadline.Equal(parentDeadline) {
				t.Fatal("request or dial context extended the earlier parent deadline")
			}
		}
		return f, nil
	})
	if err != nil || !f.closed || ctx.Err() != nil {
		t.Fatalf("incorrect lifetime/cleanup: %v closed=%v parent=%v", err, f.closed, ctx.Err())
	}
}
