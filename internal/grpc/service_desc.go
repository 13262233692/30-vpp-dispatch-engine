package grpc

import (
	"context"

	proto "github.com/vpp/dispatch-engine/api/proto"
	"google.golang.org/grpc"
)

type dispatchServiceDesc struct {
	handler DispatchServiceHandler
}

func RegisterDispatchService(s *grpc.Server, handler DispatchServiceHandler) {
	desc := &dispatchServiceDesc{handler: handler}
	s.RegisterService(&_DispatchService_serviceDesc, desc)
}

func (d *dispatchServiceDesc) LoadShedding(ctx context.Context, req *proto.LoadSheddingRequest) (*proto.LoadSheddingResponse, error) {
	return d.handler.LoadShedding(ctx, req)
}

func (d *dispatchServiceDesc) GetFleetStatus(ctx context.Context, req *proto.FleetStatusRequest) (*proto.FleetStatusResponse, error) {
	return d.handler.GetFleetStatus(ctx, req)
}

func (d *dispatchServiceDesc) EmergencyShutdown(ctx context.Context, req *proto.EmergencyShutdownRequest) (*proto.EmergencyShutdownResponse, error) {
	return d.handler.EmergencyShutdown(ctx, req)
}

var _DispatchService_serviceDesc = grpc.ServiceDesc{
	ServiceName: "vpp.dispatch.DispatchService",
	HandlerType: (*DispatchServiceHandler)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "LoadShedding",
		},
		{
			MethodName: "GetFleetStatus",
		},
		{
			MethodName: "EmergencyShutdown",
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "api/proto/dispatch.proto",
}
