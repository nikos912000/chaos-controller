// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2020 Datadog, Inc.

package injector_test

import (
	"time"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"

	"github.com/DataDog/chaos-controller/api/v1beta1"
	"github.com/DataDog/chaos-controller/container"
	. "github.com/DataDog/chaos-controller/injector"
)

var _ = Describe("Failure", func() {
	var (
		ctn    container.ContainerMock
		inj    Injector
		config NetworkConfigMock
		spec   v1beta1.NetworkDisruptionSpec
	)

	BeforeEach(func() {
		// container
		ctn = container.ContainerMock{}
		ctn.On("EnterNetworkNamespace").Return(nil)
		ctn.On("ExitNetworkNamespace").Return(nil)

		// network disruption conf
		config = NetworkConfigMock{}
		config.On("AddNetem", mock.Anything, mock.Anything, mock.Anything).Return()
		config.On("AddOutputLimit", mock.Anything).Return()
		config.On("ApplyOperations").Return(nil)
		config.On("ClearOperations").Return(nil)

		spec = v1beta1.NetworkDisruptionSpec{
			Hosts:          []string{"testhost"},
			Port:           22,
			Drop:           5,
			Corrupt:        1,
			Delay:          1000,
			BandwidthLimit: 10000,
		}
	})

	JustBeforeEach(func() {
		inj = NewNetworkDisruptionInjectorWithConfig("fake", spec, &ctn, log, ms, &config)
	})

	Describe("inj.Inject", func() {
		JustBeforeEach(func() {
			inj.Inject()
		})

		It("should enter and exit the container network namespace", func() {
			ctn.AssertCalled(GinkgoT(), "EnterNetworkNamespace")
			ctn.AssertCalled(GinkgoT(), "ExitNetworkNamespace")
		})

		It("should call AddNetem on its network disruption config", func() {
			Expect(config.AssertCalled(GinkgoT(), "AddNetem", time.Second, spec.Drop, spec.Corrupt)).To(BeTrue())
		})

		It("should call AddOutputLimit on its network disruption config", func() {
			Expect(config.AssertCalled(GinkgoT(), "AddOutputLimit", uint(spec.BandwidthLimit))).To(BeTrue())
		})

		Describe("inj.Clean", func() {
			JustBeforeEach(func() {
				inj.Clean()
			})
			It("should enter and exit the container network namespace", func() {
				Expect(ctn.AssertCalled(GinkgoT(), "EnterNetworkNamespace")).To(BeTrue())
				Expect(ctn.AssertCalled(GinkgoT(), "ExitNetworkNamespace")).To(BeTrue())
			})
			It("should call ClearOperations on its network disruption config", func() {
				Expect(config.AssertCalled(GinkgoT(), "ClearOperations")).To(BeTrue())
			})
		})
	})
})
