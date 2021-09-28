// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2021 Datadog, Inc.

package grpc_test

import (
	chaosv1beta1 "github.com/DataDog/chaos-controller/api/v1beta1"
	. "github.com/DataDog/chaos-controller/grpc"
	pb "github.com/DataDog/chaos-controller/grpc/disruption_listener"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("construct DisruptionListener query for configuring disruptions from api spec", func() {
	var (
		endpointAlterations []chaosv1beta1.EndpointAlteration
		endpointSpec        []*pb.EndpointSpec
	)

	Context("with five alterations which add up to less than 100", func() {
		BeforeEach(func() {
			endpointAlterations = []chaosv1beta1.EndpointAlteration{
				{
					TargetEndpoint:   "service/api_1",
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
					QueryPercent:     25,
				},
				{
					TargetEndpoint:   "service/api_2",
					ErrorToReturn:    "PERMISSION_DENIED",
					OverrideToReturn: "",
					QueryPercent:     50,
				},
				{
					TargetEndpoint:   "service/api_1",
					ErrorToReturn:    "ALREADY_EXISTS",
					OverrideToReturn: "",
					QueryPercent:     20,
				},
				{
					TargetEndpoint:   "service/api_2",
					ErrorToReturn:    "NOT_FOUND",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					TargetEndpoint:   "service/api_1",
					ErrorToReturn:    "",
					OverrideToReturn: "{}",
					QueryPercent:     0,
				},
			}

			var err error
			endpointSpec = GenerateEndpointSpecs(endpointAlterations)
			Expect(err).To(BeNil())
		})

		It("should create a list of endpointSpecs with 2 elements", func() {
			Expect(len(endpointSpec)).To(Equal(2))
		})

		It("should create and endpointSpec for api_1 with 3 elements", func() {
			// handling that the results of `endpointSpec` is indeterminate
			var endpointSpec_1 *pb.EndpointSpec
			if endpointSpec[0].TargetEndpoint == "service/api_1" {
				endpointSpec_1 = endpointSpec[0]
			} else {
				endpointSpec_1 = endpointSpec[1]
			}

			Expect(endpointSpec_1).ToNot(BeNil())
			Expect(len(endpointSpec_1.Alterations)).To(Equal(3))

			canceled_found := false
			already_exists_found := false
			empty_response_found := false

			for _, alteration := range endpointSpec_1.Alterations {
				if alteration.ErrorToReturn == "CANCELED" {
					canceled_found = true
					Expect(alteration.OverrideToReturn).To(Equal(""))
					Expect(alteration.QueryPercent).To(Equal(int32(25)))
				} else if alteration.ErrorToReturn == "ALREADY_EXISTS" {
					already_exists_found = true
					Expect(alteration.OverrideToReturn).To(Equal(""))
					Expect(alteration.QueryPercent).To(Equal(int32(20)))
				} else {
					Expect(alteration.ErrorToReturn).To(Equal(""))
					Expect(alteration.OverrideToReturn).To(Equal("{}"))
					Expect(alteration.QueryPercent).To(Equal(int32(0)))
					empty_response_found = true
				}
			}

			Expect(canceled_found).To(BeTrue())
			Expect(already_exists_found).To(BeTrue())
			Expect(empty_response_found).To(BeTrue())
		})

		It("should create and endpointSpec for api_2 with 2 elements", func() {
			// handling that the results of `endpointSpec` is indeterminate
			var endpointSpec_2 *pb.EndpointSpec
			if endpointSpec[0].TargetEndpoint == "service/api_2" {
				endpointSpec_2 = endpointSpec[0]
			} else {
				endpointSpec_2 = endpointSpec[1]
			}

			Expect(endpointSpec_2).ToNot(BeNil())
			Expect(len(endpointSpec_2.Alterations)).To(Equal(2))

			permission_denied_found := false
			not_found_found := false

			for _, alteration := range endpointSpec_2.Alterations {
				if alteration.ErrorToReturn == "PERMISSION_DENIED" {
					permission_denied_found = true
					Expect(alteration.OverrideToReturn).To(Equal(""))
					Expect(alteration.QueryPercent).To(Equal(int32(50)))
				} else {
					not_found_found = true
					Expect(alteration.ErrorToReturn).To(Equal("NOT_FOUND"))
					Expect(alteration.OverrideToReturn).To(Equal(""))
					Expect(alteration.QueryPercent).To(Equal(int32(0)))
				}
			}
			Expect(permission_denied_found).To(BeTrue())
			Expect(not_found_found).To(BeTrue())
		})
	})
})
