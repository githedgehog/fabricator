package vlab

import (
	"context"
	"net"

	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

type grpcService struct {
	UnimplementedServiceServer
}

func (s *grpcService) Status(context.Context, *StatusRequest) (*StatusResponse, error) {
	if false {
		return nil, status.Errorf(codes.Unimplemented, "method Status not implemented")
	}

	return &StatusResponse{
		Ok:      true,
		Message: "All Good!",
	}, nil
}

func StartGRPCServer(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Wrapf(err, "error starting tcp listener on %s", addr)
	}

	grpcServer := grpc.NewServer()
	serviceServer := &grpcService{}
	RegisterServiceServer(grpcServer, serviceServer)
	reflection.Register(grpcServer)

	return errors.Wrap(grpcServer.Serve(lis), "error serving grpc")
}
