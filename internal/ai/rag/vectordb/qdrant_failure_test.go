package vectordb

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Swhshd/kestreldesk/internal/pkg/config"
)

func TestQdrantSearchReturnsBoundedErrorWhenLoopbackPortRefusesConnections(t *testing.T) {
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	address := reservation.Addr().(*net.TCPAddr)
	if err := reservation.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}

	provider, err := NewQdrantProvider(&config.QdrantVectorDBConfig{
		Host:     "127.0.0.1",
		GrpcPort: address.Port,
	})
	if err != nil {
		t.Fatalf("create local Qdrant client: %v", err)
	}
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("close Qdrant client: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	results, err := provider.Search(ctx, syntheticSearchRequest())
	if err == nil {
		t.Fatal("expected connection-refused search error")
	}
	if results != nil {
		t.Fatalf("failed search returned results: %#v", results)
	}
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("search error code = %s, want %s: %v", got, codes.Unavailable, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("search exceeded bounded deadline: %s", elapsed)
	}
}

func TestQdrantSearchHonorsDeadlineWhenLocalServerDoesNotRespond(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen on loopback: %v", err)
	}
	grpcServer := grpc.NewServer()
	fake := &blockingQdrantPointsServer{started: make(chan struct{}, 1)}
	qdrant.RegisterPointsServer(grpcServer, fake)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		if err := <-serveDone; err != nil && err != grpc.ErrServerStopped {
			t.Errorf("serve local Qdrant fake: %v", err)
		}
	})

	address := listener.Addr().(*net.TCPAddr)
	provider, err := NewQdrantProvider(&config.QdrantVectorDBConfig{
		Host:     "127.0.0.1",
		GrpcPort: address.Port,
	})
	if err != nil {
		t.Fatalf("create local Qdrant client: %v", err)
	}
	t.Cleanup(func() {
		if err := provider.Close(); err != nil {
			t.Errorf("close Qdrant client: %v", err)
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	type searchCall struct {
		results []SearchResult
		err     error
	}
	done := make(chan searchCall, 1)
	go func() {
		results, err := provider.Search(ctx, syntheticSearchRequest())
		done <- searchCall{results: results, err: err}
	}()
	select {
	case <-fake.started:
	case call := <-done:
		t.Fatalf("Qdrant search finished before the blocking server received it: %v", call.err)
	case <-time.After(2 * time.Second):
		t.Fatal("blocking Qdrant server did not receive the search")
	}
	var call searchCall
	select {
	case call = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Qdrant search did not finish within the context deadline")
	}
	results, err := call.results, call.err
	if err == nil {
		t.Fatal("expected deadline error")
	}
	if results != nil {
		t.Fatalf("failed search returned results: %#v", results)
	}
	if got := status.Code(err); got != codes.DeadlineExceeded {
		t.Fatalf("search error code = %s, want %s: %v", got, codes.DeadlineExceeded, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("search exceeded bounded deadline: %s", elapsed)
	}
}

func syntheticSearchRequest() *SearchRequest {
	return &SearchRequest{
		CollectionName: "synthetic-e3",
		Vector:         []float32{0.1, 0.2, 0.3},
		TopK:           3,
	}
}

type blockingQdrantPointsServer struct {
	qdrant.UnimplementedPointsServer
	started chan struct{}
}

func (s *blockingQdrantPointsServer) Query(ctx context.Context, _ *qdrant.QueryPoints) (*qdrant.QueryResponse, error) {
	s.started <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}
