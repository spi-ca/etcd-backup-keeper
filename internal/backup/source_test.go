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
	data                          []byte
	serverVersion, storageVersion string
}

func (s *maintenanceFixture) Status(context.Context, *pb.StatusRequest) (*pb.StatusResponse, error) {
	return &pb.StatusResponse{Header: &pb.ResponseHeader{ClusterId: 11, MemberId: 21}, Version: s.serverVersion, StorageVersion: s.storageVersion, DbSize: int64(len(s.data) - 32)}, nil
}
func (s *maintenanceFixture) Snapshot(_ *pb.SnapshotRequest, server grpc.ServerStreamingServer[pb.SnapshotResponse]) error {
	// Exercise headerless responses; pre/post Status must still establish identity.
	return server.Send(&pb.SnapshotResponse{Version: s.storageVersion, Blob: s.data})
}
func TestDirectClientBackupAgainstLocalProtocolFixture(t *testing.T) {
	for _, serverVersion := range []string{"3.6.8", "3.7.3"} {
		for _, storageVersion := range []string{"3.6.0", "3.7.0"} {
			t.Run(serverVersion+"/"+storageVersion, func(t *testing.T) {
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				must(t, err)
				server := grpc.NewServer()
				pb.RegisterMaintenanceServer(server, &maintenanceFixture{data: fixtureWithStorage(t, storageVersion), serverVersion: serverVersion, storageVersion: storageVersion})
				done := make(chan struct{})
				go func() { defer close(done); server.Serve(listener) }()
				t.Cleanup(func() { server.Stop(); listener.Close(); <-done })
				cfg := testConfig(t)
				cfg.Endpoints = []string{"http://" + listener.Addr().String()}
				result, err := Run(context.Background(), cfg, "protocol-test")
				must(t, err)
				if result.Metadata.Keys != 1 || result.Metadata.Revision != 2 || result.Metadata.ServerVersion != serverVersion || result.Metadata.StorageVersion != storageVersion {
					t.Fatalf("wrong direct-client result: %+v", result)
				}
			})
		}
	}
}
