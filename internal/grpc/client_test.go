package grpc

import (
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func newLazyConn(t *testing.T) *grpc.ClientConn {
	t.Helper()
	// grpc.NewClient is lazy: it builds the ClientConn without dialing, so no
	// real server is needed to exercise the swap/close bookkeeping.
	conn, err := grpc.NewClient("passthrough:///unused",
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return conn
}

// replaceConn must return the superseded connection so the caller can close it.
// This is the core of the leak fix: a reconnect that does not get the old
// connection back has no way to close it.
func TestReplaceConn_ReturnsSupersededConnection(t *testing.T) {
	c := &Client{}

	conn1 := newLazyConn(t)
	t.Cleanup(func() { conn1.Close() })
	if old := c.replaceConn(conn1); old != nil {
		t.Fatalf("first replace returned %v, want nil", old)
	}
	if c.GetConnection() != conn1 {
		t.Fatal("active connection should be conn1")
	}

	conn2 := newLazyConn(t)
	t.Cleanup(func() { conn2.Close() })
	old := c.replaceConn(conn2)
	if old != conn1 {
		t.Fatal("second replace should return conn1 so the caller can close it")
	}
	if c.GetConnection() != conn2 {
		t.Fatal("active connection should now be conn2")
	}
}

// Re-installing the same connection must not report it as superseded (which
// would make the caller close the still-active connection).
func TestReplaceConn_SameConnectionReturnsNil(t *testing.T) {
	c := &Client{}
	conn := newLazyConn(t)
	t.Cleanup(func() { conn.Close() })

	c.replaceConn(conn)
	if old := c.replaceConn(conn); old != nil {
		t.Fatalf("replacing with the same conn returned %v, want nil", old)
	}
}
