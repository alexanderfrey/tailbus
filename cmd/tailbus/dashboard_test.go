package main

import (
	"context"
	"io"
	"testing"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type fakeDashboardClient struct {
	getStatusResp *agentpb.GetNodeStatusResponse
	getStatusErr  error
	watchStreams  []grpc.ServerStreamingClient[agentpb.ActivityEvent]
	watchErrs     []error
	watchCalls    int
}

func (f *fakeDashboardClient) GetNodeStatus(context.Context, *agentpb.GetNodeStatusRequest, ...grpc.CallOption) (*agentpb.GetNodeStatusResponse, error) {
	return f.getStatusResp, f.getStatusErr
}

func (f *fakeDashboardClient) WatchActivity(context.Context, *agentpb.WatchActivityRequest, ...grpc.CallOption) (grpc.ServerStreamingClient[agentpb.ActivityEvent], error) {
	idx := f.watchCalls
	f.watchCalls++
	if idx < len(f.watchErrs) && f.watchErrs[idx] != nil {
		return nil, f.watchErrs[idx]
	}
	if idx < len(f.watchStreams) {
		return f.watchStreams[idx], nil
	}
	return &fakeActivityStream{recvErr: io.EOF}, nil
}

type fakeActivityStream struct {
	ctx     context.Context
	events  []*agentpb.ActivityEvent
	recvErr error
	index   int
}

func (s *fakeActivityStream) Header() (metadata.MD, error) { return metadata.MD{}, nil }
func (s *fakeActivityStream) Trailer() metadata.MD         { return metadata.MD{} }
func (s *fakeActivityStream) CloseSend() error             { return nil }
func (s *fakeActivityStream) Context() context.Context {
	if s.ctx != nil {
		return s.ctx
	}
	return context.Background()
}
func (s *fakeActivityStream) SendMsg(any) error { return nil }
func (s *fakeActivityStream) RecvMsg(any) error { return nil }
func (s *fakeActivityStream) Recv() (*agentpb.ActivityEvent, error) {
	if s.index < len(s.events) {
		event := s.events[s.index]
		s.index++
		return event, nil
	}
	if s.recvErr != nil {
		err := s.recvErr
		s.recvErr = nil
		return nil, err
	}
	return nil, io.EOF
}

func TestDashboardClearsStatusOnDisconnect(t *testing.T) {
	model := newDashboardModel(&fakeDashboardClient{})
	model.status = &agentpb.GetNodeStatusResponse{NodeId: "node-1"}

	next, _ := model.Update(statusErrMsg{status.Error(codes.Unavailable, "daemon down")})
	updated := next.(dashboardModel)

	if updated.status != nil {
		t.Fatal("expected status to be cleared after disconnect")
	}
	if updated.err == nil {
		t.Fatal("expected dashboard error to be recorded")
	}
}

func TestDashboardResubscribesAfterActivityDisconnect(t *testing.T) {
	client := &fakeDashboardClient{
		watchStreams: []grpc.ServerStreamingClient[agentpb.ActivityEvent]{
			&fakeActivityStream{
				events: []*agentpb.ActivityEvent{
					{
						Event: &agentpb.ActivityEvent_HandleRegistered{
							HandleRegistered: &agentpb.HandleRegisteredEvent{Handle: "solver"},
						},
					},
				},
			},
		},
	}

	model := newDashboardModel(client)

	next, retryCmd := model.Update(activityErrMsg{status.Error(codes.Unavailable, "stream closed")})
	if retryCmd == nil {
		t.Fatal("expected reconnect retry command after activity stream error")
	}

	updated := next.(dashboardModel)
	_, watchCmd := updated.Update(watchRetryMsg{})
	if watchCmd == nil {
		t.Fatal("expected watch command after retry message")
	}

	msg := watchCmd()
	if _, ok := msg.(activityMsg); !ok {
		t.Fatalf("expected activityMsg after resubscribe, got %T", msg)
	}
	if client.watchCalls != 1 {
		t.Fatalf("expected one watch attempt, got %d", client.watchCalls)
	}
}
