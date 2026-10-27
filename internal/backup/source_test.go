package backup

import (
	"context"
	"net"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
)

// This is a local gRPC protocol fixture, NOT an actual etcd integration test.
// It exercises the direct 3.7 client and same-binary local inspector together.
type maintenanceFixture struct {
	pb.UnimplementedMaintenanceServer
	data []byte
}

func (s *maintenanceFixture) Status(context.Context, *pb.StatusRequest) (*pb.StatusResponse, error) {
	return &pb.StatusResponse{Header: &pb.ResponseHeader{ClusterId: 11, MemberId: 21}, Version: "3.7.3", StorageVersion: "3.7.0", DbSize: int64(len(s.data) - 32)}, nil
}
func (s *maintenanceFixture) Snapshot(_ *pb.SnapshotRequest, server grpc.ServerStreamingServer[pb.SnapshotResponse]) error {
	// Match the real etcd 3.7.3 server: snapshot responses omit Header.
	return server.Send(&pb.SnapshotResponse{Version: "3.7.0", Blob: s.data})
}
func TestDirectClientBackupAgainstLocalProtocolFixture(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	server := grpc.NewServer()
	pb.RegisterMaintenanceServer(server, &maintenanceFixture{data: fixture(t)})
	done := make(chan struct{})
	go func() { defer close(done); server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); listener.Close(); <-done })
	cfg := testConfig(t)
	cfg.Endpoints = []string{"http://" + listener.Addr().String()}
	result, err := Run(context.Background(), cfg, "protocol-test")
	must(t, err)
	if result.Metadata.Keys != 1 || result.Metadata.Revision != 2 {
		t.Fatalf("wrong direct-client result: %+v", result)
	}
}
