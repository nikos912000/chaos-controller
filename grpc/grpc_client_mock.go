// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2022 Datadog, Inc.

package grpc

import (
	"context"

	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/DataDog/chaos-controller/grpc/disruptionlistener"
)

// DisruptionListenerClientMock is a mock implementation of the DisruptionListenerClient interface
// used in unit tests
type DisruptionListenerClientMock struct {
	mock.Mock
}

//nolint:golint
func (d *DisruptionListenerClientMock) Disrupt(ctx context.Context, spec *pb.DisruptionSpec, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	mockArgs := d.Called(ctx, spec)

	return mockArgs.Get(0).(*emptypb.Empty), mockArgs.Error(1)
}

//nolint:golint
func (d *DisruptionListenerClientMock) ResetDisruptions(ctx context.Context, empty *emptypb.Empty, opts ...grpc.CallOption) (*emptypb.Empty, error) {
	mockArgs := d.Called(ctx, empty)

	return mockArgs.Get(0).(*emptypb.Empty), mockArgs.Error(1)
}
