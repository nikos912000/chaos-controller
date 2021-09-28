// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2021 Datadog, Inc.

package calculations_test

import (
	. "github.com/DataDog/chaos-controller/grpc/calculations"
	pb "github.com/DataDog/chaos-controller/grpc/disruption_listener"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("get mapping from AlterationSpecs to QueryPercentByAltConfig based on GetMappingFromAlterationToQueryPercent", func() {
	var (
		alterationSpecs         []*pb.AlterationSpec
		QueryPercentByAltConfig map[AlterationConfiguration]QueryPercent
	)

	Context("with three alterations which add up to less than 100", func() {
		It("should create a QueryPercentByAltConfig with correct configs", func() {
			alterationSpecs = []*pb.AlterationSpec{
				{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
					QueryPercent:     int32(20),
				},
				{
					ErrorToReturn:    "ALREADY_EXISTS",
					OverrideToReturn: "",
					QueryPercent:     int32(30),
				},
				{
					ErrorToReturn:    "",
					OverrideToReturn: "{}",
					QueryPercent:     int32(40),
				},
			}

			By("returning no errors", func() {
				var err error
				QueryPercentByAltConfig, err = ConvertAltSpecToQueryPercentByAltConfig(alterationSpecs)
				Expect(err).To(BeNil())
			})

			By("returning 3 elements", func() {
				Expect(len(QueryPercentByAltConfig)).To(Equal(3))
			})

			By("by assigning a query percentage of 20 to CANCELLED error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
				}

				pct_canceled, ok_canceled := QueryPercentByAltConfig[altCfg]
				Expect(ok_canceled).To(BeTrue())
				Expect(pct_canceled).To(Equal(QueryPercent(20)))
			})

			By("by assigning a query percentage of 30 to ALREADY_EXISTS error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "ALREADY_EXISTS",
					OverrideToReturn: "",
				}
				pct_exists, ok_exists := QueryPercentByAltConfig[altCfg]

				Expect(ok_exists).To(BeTrue())
				Expect(pct_exists).To(Equal(QueryPercent(30)))
			})

			By("by assigning a query percentage of 40 to empty override", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "",
					OverrideToReturn: "{}",
				}
				pct_emptyret, ok_emptyret := QueryPercentByAltConfig[altCfg]

				Expect(ok_emptyret).To(BeTrue())
				Expect(pct_emptyret).To(Equal(QueryPercent(40)))
			})
		})
	})

	Context("with one alterations with too many fields specified", func() {
		It("should fail", func() {
			alterationSpecs = []*pb.AlterationSpec{
				{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "{}",
				},
			}

			By("returning an InvalidArgument error", func() {
				_, err := ConvertAltSpecToQueryPercentByAltConfig(alterationSpecs)
				Expect(err.Error()).To(Equal("rpc error: code = InvalidArgument desc = cannot map alteration to assigned percentage when ErrorToReturn and OverrideToReturn are both specified for a target endpoint"))
			})
		})
	})

	Context("with one alterations with too few fields specified", func() {
		It("should fail", func() {

			alterationSpecs = []*pb.AlterationSpec{
				{
					ErrorToReturn:    "",
					OverrideToReturn: "",
				},
			}

			By("returning an InvalidArgument error", func() {
				_, err := ConvertAltSpecToQueryPercentByAltConfig(alterationSpecs)
				Expect(err.Error()).To(Equal("rpc error: code = InvalidArgument desc = cannot map alteration to assigned percentage without specifying either ErrorToReturn or OverrideToReturn for a target endpoint"))
			})
		})
	})

	Context("with three alterations which are more than 100", func() {
		It("should fail", func() {
			alterationSpecs = []*pb.AlterationSpec{
				{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
					QueryPercent:     int32(50),
				},
				{
					ErrorToReturn:    "ALREADY_EXISTS",
					OverrideToReturn: "",
					QueryPercent:     int32(50),
				},
				{
					ErrorToReturn:    "",
					OverrideToReturn: "{}",
					QueryPercent:     int32(50),
				},
			}

			By("returning an Invalid Argument error", func() {
				var err error
				QueryPercentByAltConfig, err = ConvertAltSpecToQueryPercentByAltConfig(alterationSpecs)
				Expect(err.Error()).To(Equal("rpc error: code = InvalidArgument desc = assigned percentage for this endpoint exceeds 100% of possible queries"))
			})
		})
	})
	Context("with one alteration less than 100", func() {
		It("should create a QueryPercentByAltConfig with correct configs", func() {
			alterationSpecs = []*pb.AlterationSpec{
				{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
					QueryPercent:     int32(40),
				},
			}

			By("returning no errors", func() {
				var err error
				QueryPercentByAltConfig, err = ConvertAltSpecToQueryPercentByAltConfig(alterationSpecs)

				Expect(err).To(BeNil())
			})

			By("returning 1 element", func() {
				Expect(len(QueryPercentByAltConfig)).To(Equal(1))
			})

			By("by assigning a query percentage of 40 to CANCELED error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
				}
				pct_canceled, ok_canceled := QueryPercentByAltConfig[altCfg]

				Expect(ok_canceled).To(BeTrue())
				Expect(pct_canceled).To(Equal(QueryPercent(40)))
			})
		})
	})

	Context("with one alteration lacking query percent", func() {
		It("should create a QueryPercentByAltConfig with correct configs", func() {
			alterationSpecs = []*pb.AlterationSpec{
				{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
			}

			By("returning no errors", func() {
				var err error
				QueryPercentByAltConfig, err = ConvertAltSpecToQueryPercentByAltConfig(alterationSpecs)
				Expect(err).To(BeNil())
			})

			By("returning 1 element", func() {
				Expect(len(QueryPercentByAltConfig)).To(Equal(1))
			})

			By("by assigning a query percentage of 100 to CANCELED error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
				}
				pct_canceled, ok_canceled := QueryPercentByAltConfig[altCfg]

				Expect(ok_canceled).To(BeTrue())
				Expect(pct_canceled).To(Equal(QueryPercent(100)))
			})
		})
	})

	Context("with three alterations, two of which lack a queryPercent", func() {
		It("should create a QueryPercentByAltConfig with correct configs", func() {
			alterationSpecs = []*pb.AlterationSpec{
				{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "ALREADY_EXISTS",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "",
					OverrideToReturn: "{}",
					QueryPercent:     int32(50),
				},
			}

			By("returning no errors", func() {
				var err error
				QueryPercentByAltConfig, err = ConvertAltSpecToQueryPercentByAltConfig(alterationSpecs)
				Expect(err).To(BeNil())
			})

			By("returning 3 elements", func() {
				Expect(len(QueryPercentByAltConfig)).To(Equal(3))
			})

			By("by assigning a query percentage of 25 to CANCELED error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
				}
				pct_canceled, ok_canceled := QueryPercentByAltConfig[altCfg]

				Expect(ok_canceled).To(BeTrue())
				Expect(pct_canceled).To(Equal(QueryPercent(25)))
			})

			By("by assigning a query percentage of 25 to ALREADY_EXISTS error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "ALREADY_EXISTS",
					OverrideToReturn: "",
				}
				pct_exists, ok_exists := QueryPercentByAltConfig[altCfg]

				Expect(ok_exists).To(BeTrue())
				Expect(pct_exists).To(Equal(QueryPercent(25)))
			})

			By("by assigning a query percentage of 50 to empty override", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "",
					OverrideToReturn: "{}",
				}
				pct_emptyret, ok_emptyret := QueryPercentByAltConfig[altCfg]

				Expect(ok_emptyret).To(BeTrue())
				Expect(pct_emptyret).To(Equal(QueryPercent(50)))
			})
		})
	})

	Context("with seven alterations, six of which lack a queryPercent", func() {
		BeforeEach(func() {
			alterationSpecs = []*pb.AlterationSpec{
				{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
					QueryPercent:     int32(90),
				},
				{
					ErrorToReturn:    "ALREADY_EXISTS",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "UNKNOWN",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "INVALID_ARGUMENT",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "DEADLINE_EXCEEDED",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "NOT_FOUND",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "PERMISSION_DENIED",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
			}

			By("returning no errors", func() {
				var err error
				QueryPercentByAltConfig, err = ConvertAltSpecToQueryPercentByAltConfig(alterationSpecs)
				Expect(err).To(BeNil())
			})
		})

		It("should create a QueryPercentByAltConfig with correct configs", func() {
			By("returning 7 elements", func() {
				Expect(len(QueryPercentByAltConfig)).To(Equal(7))
			})

			By("by assigning a query percentage of 90 to CANCELED error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
				}
				pct, ok := QueryPercentByAltConfig[altCfg]

				Expect(ok).To(BeTrue())
				Expect(pct).To(Equal(QueryPercent(90)))
			})

			By("by assigning a query percentage of 1 to ALREADY_EXISTS error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "ALREADY_EXISTS",
					OverrideToReturn: "",
				}
				pct, ok := QueryPercentByAltConfig[altCfg]

				Expect(ok).To(BeTrue())
				Expect(pct).To(Equal(QueryPercent(1)))
			})

			By("by assigning a query percentage of 1 to UNKNOWN error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "UNKNOWN",
					OverrideToReturn: "",
				}
				pct, ok := QueryPercentByAltConfig[altCfg]

				Expect(ok).To(BeTrue())
				Expect(pct).To(Equal(QueryPercent(1)))
			})

			By("by assigning a query percentage of 1 to INVALID_ARGUMENT error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "INVALID_ARGUMENT",
					OverrideToReturn: "",
				}
				pct, ok := QueryPercentByAltConfig[altCfg]

				Expect(ok).To(BeTrue())
				Expect(pct).To(Equal(QueryPercent(1)))
			})

			By("by assigning a query percentage of 1 to DEADLINE_EXCEEDED error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "DEADLINE_EXCEEDED",
					OverrideToReturn: "",
				}
				pct, ok := QueryPercentByAltConfig[altCfg]

				Expect(ok).To(BeTrue())
				Expect(pct).To(Equal(QueryPercent(1)))
			})

			By("by assigning a query percentage of 1 to NOT_FOUND error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "NOT_FOUND",
					OverrideToReturn: "",
				}
				pct, ok := QueryPercentByAltConfig[altCfg]

				Expect(ok).To(BeTrue())
				Expect(pct).To(Equal(QueryPercent(1)))
			})

			By("by assigning a query percentage of 1 to PERMISSION_DENIED error", func() {
				altCfg := AlterationConfiguration{
					ErrorToReturn:    "PERMISSION_DENIED",
					OverrideToReturn: "",
				}
				pct, ok := QueryPercentByAltConfig[altCfg]

				Expect(ok).To(BeTrue())
				Expect(pct).To(Equal(QueryPercent(5)))
			})
		})
	})

	Context("with twelve alterations, eleven of which lack a queryPercent", func() {
		It("should fail", func() {
			alterationSpecs = []*pb.AlterationSpec{
				{
					ErrorToReturn:    "CANCELED",
					OverrideToReturn: "",
					QueryPercent:     int32(90),
				},
				{
					ErrorToReturn:    "ALREADY_EXISTS",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "UNKNOWN",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "INVALID_ARGUMENT",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "DEADLINE_EXCEEDED",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "NOT_FOUND",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "PERMISSION_DENIED",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "RESOURCE_EXHAUSTED",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "FAILED_PRECONDITION",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "ABORTED",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "OUT_OF_RANGE",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
				{
					ErrorToReturn:    "UNIMPLEMENTED",
					OverrideToReturn: "",
					QueryPercent:     0,
				},
			}

			By("returning an InvalidArgument error", func() {
				var err error
				QueryPercentByAltConfig, err = ConvertAltSpecToQueryPercentByAltConfig(alterationSpecs)
				Expect(err.Error()).To(Equal("rpc error: code = InvalidArgument desc = alterations must have at least 1% chance of occurring; endpoint has too many alterations configured so its total configurations exceed 100%"))
			})
		})
	})
})
