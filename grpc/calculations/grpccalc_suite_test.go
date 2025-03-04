// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2022 Datadog, Inc.

package calculations_test

import (
	"testing"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

func TestGrpcCalc(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "GRPC Calculations Test Suite")
}
