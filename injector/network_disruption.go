// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2022 Datadog, Inc.

package injector

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/DataDog/chaos-controller/api/v1beta1"
	"github.com/DataDog/chaos-controller/env"
	"github.com/DataDog/chaos-controller/network"
	"github.com/DataDog/chaos-controller/types"
	chaostypes "github.com/DataDog/chaos-controller/types"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"
)

// linkOperation represents a tc operation on a set of network interfaces combined with the parent to bind to and the handle identifier to use
type linkOperation func([]string, string, uint32) error

// tcPriority the lowest priority set by tc automatically when adding a tc filter
var tcPriority = uint32(49149)

// networkDisruptionService describes a parsed Kubernetes service, representing an (ip, port, protocol) tuple
type networkDisruptionService struct {
	ip       *net.IPNet
	port     int
	protocol string
}

func (n networkDisruptionService) String() string {
	ip := ""
	if n.ip != nil {
		ip = n.ip.String()
	}

	return fmt.Sprintf("ip=%s; port=%d; protocol=%s", ip, n.port, n.protocol)
}

// networkDisruptionInjector describes a network disruption
type networkDisruptionInjector struct {
	spec       v1beta1.NetworkDisruptionSpec
	config     NetworkDisruptionInjectorConfig
	operations []linkOperation

	tcFilterPriority uint32     // keep track of the highest tc filter priority
	tcFilterMutex    sync.Mutex // since we increment tcFilterPriority in goroutines we use a mutex to lock and unlock
}

// NetworkDisruptionInjectorConfig contains all needed drivers to create a network disruption using `tc`
type NetworkDisruptionInjectorConfig struct {
	Config
	TrafficController network.TrafficController
	NetlinkAdapter    network.NetlinkAdapter
	DNSClient         network.DNSClient
	State             DisruptionState
}

type DisruptionState struct {
	State chan InjectorState
}

// tcServiceFilter describes a tc filter, representing the service filtered and its priority
type tcServiceFilter struct {
	service  networkDisruptionService
	priority uint32 // one priority per tc filters applied, the priority is the same for all interfaces
}

// serviceWatcher
type serviceWatcher struct {
	// information about the service watched
	watchedServiceSpec   v1beta1.NetworkDisruptionServiceSpec
	servicePorts         []v1.ServicePort
	labelServiceSelector string

	// filters and watcher for the pods related to the service watched
	kubernetesPodEndpointsWatcher <-chan watch.Event
	tcFiltersFromPodEndpoints     []tcServiceFilter
	podsWithoutIPs                []string
	podsResourceVersion           string

	// filters and watcher for the kubernetes service watched
	kubernetesServiceWatcher       <-chan watch.Event
	tcFiltersFromNamespaceServices []tcServiceFilter
	servicesResourceVersion        string
}

// NewNetworkDisruptionInjector creates a NetworkDisruptionInjector object with the given config,
// missing field being initialized with the defaults
func NewNetworkDisruptionInjector(spec v1beta1.NetworkDisruptionSpec, config NetworkDisruptionInjectorConfig) Injector {
	if config.TrafficController == nil {
		config.TrafficController = network.NewTrafficController(config.Log, config.DryRun)
	}

	if config.NetlinkAdapter == nil {
		config.NetlinkAdapter = network.NewNetlinkAdapter()
	}

	if config.DNSClient == nil {
		config.DNSClient = network.NewDNSClient()
	}

	config.State = DisruptionState{}

	go func() {
		config.State.State <- Created
	}()

	return &networkDisruptionInjector{
		spec:       spec,
		config:     config,
		operations: []linkOperation{},
	}
}

func (i *networkDisruptionInjector) GetDisruptionKind() chaostypes.DisruptionKindName {
	return chaostypes.DisruptionKindNetworkDisruption
}

// Inject injects the given network disruption into the given container
func (i *networkDisruptionInjector) Inject() error {
	// enter target network namespace
	if err := i.config.Netns.Enter(); err != nil {
		return fmt.Errorf("unable to enter the given container network namespace: %w", err)
	}

	i.config.Log.Infow("adding network disruptions", "drop", i.spec.Drop, "duplicate", i.spec.Duplicate, "corrupt", i.spec.Corrupt, "delay", i.spec.Delay, "delayJitter", i.spec.DelayJitter, "bandwidthLimit", i.spec.BandwidthLimit)

	// add netem
	if i.spec.Delay > 0 || i.spec.Drop > 0 || i.spec.Corrupt > 0 || i.spec.Duplicate > 0 {
		delay := time.Duration(i.spec.Delay) * time.Millisecond

		var delayJitter time.Duration

		// add a 10% delayJitter to delay by default if not specified
		if i.spec.DelayJitter == 0 {
			delayJitter = time.Duration(float64(i.spec.Delay)*0.1) * time.Millisecond
		} else {
			// convert delayJitter into a percentage then multiply that with delay to get correct percentage of delay
			delayJitter = time.Duration((float64(i.spec.DelayJitter)/100.0)*float64(i.spec.Delay)) * time.Millisecond
		}

		delayJitter = time.Duration(math.Max(float64(delayJitter), float64(time.Millisecond)))

		i.addNetemOperation(delay, delayJitter, i.spec.Drop, i.spec.Corrupt, i.spec.Duplicate)
	}

	// add tbf
	if i.spec.BandwidthLimit > 0 {
		i.addOutputLimitOperation(uint(i.spec.BandwidthLimit))
	}

	// apply operations if any
	if len(i.operations) > 0 {
		if err := i.applyOperations(); err != nil {
			return fmt.Errorf("error applying tc operations: %w", err)
		}

		i.config.Log.Debug("operations applied successfully")
	}

	i.config.Log.Info("editing pod net_cls cgroup to apply a classid to target container packets")

	// write classid to pod net_cls cgroup
	if err := i.config.Cgroup.Write("net_cls", "net_cls.classid", types.InjectorCgroupClassID); err != nil {
		return fmt.Errorf("error writing classid to pod net_cls cgroup: %w", err)
	}

	// exit target network namespace
	if err := i.config.Netns.Exit(); err != nil {
		return fmt.Errorf("unable to exit the given container network namespace: %w", err)
	}

	go func() {
		i.config.State.State <- Injected
	}()

	return nil
}

func (i *networkDisruptionInjector) UpdateConfig(config Config) {
	i.config.Config = config
}

// Clean removes all the injected disruption in the given container
func (i *networkDisruptionInjector) Clean() error {
	defer func() {
		go func() {
			i.config.State.State <- Cleaned
		}()
	}()

	// enter container network namespace
	if err := i.config.Netns.Enter(); err != nil {
		return fmt.Errorf("unable to enter the given container network namespace: %w", err)
	}

	// defer the exit on return
	defer func() {
	}()

	if err := i.clearOperations(); err != nil {
		return fmt.Errorf("error clearing tc operations: %w", err)
	}

	// write default classid to pod net_cls cgroup if it still exists
	exists, err := i.config.Cgroup.Exists("net_cls")
	if err != nil {
		return fmt.Errorf("error checking if pod net_cls cgroup still exists: %w", err)
	}

	if exists {
		if err := i.config.Cgroup.Write("net_cls", "net_cls.classid", "0x0"); err != nil {
			return fmt.Errorf("error reseting classid of pod net_cls cgroup: %w", err)
		}
	}

	// exit target network namespace
	if err := i.config.Netns.Exit(); err != nil {
		return fmt.Errorf("unable to exit the given container network namespace: %w", err)
	}

	return nil
}

// applyOperations applies the added operations by building a tc tree
// Here's what happen on tc side:
//   - a first prio qdisc will be created and attached to root
//     it'll be used to apply the first filter, filtering on packet IP destination, source/destination ports and protocol
//   - a second prio qdisc will be created and attached to the first one
//     it'll be used to apply the second filter, filtering on packet classid to identify packets coming from the targeted process
//   - operations will be chained to the second band of the second prio qdisc
//   - a cgroup filter will be created to classify packets according to their classid (if any)
//   - a filter will be created to redirect traffic related to the specified host(s) through the last prio band
//     if no host, port or protocol is specified, a filter redirecting all the traffic (0.0.0.0/0) to the disrupted band will be created
//   - a last filter will be created to redirect traffic related to the local node through a not disrupted band
//
// Here's the tc tree representation:
// root (1:) <-- prio qdisc with 4 bands with a filter classifying packets matching the given dst ip, src/dst ports and protocol with class 1:4
//
//	|- (1:1) <-- first band
//	|- (1:2) <-- second band
//	|- (1:3) <-- third band
//	|- (1:4) <-- fourth band
//	  |- (2:) <-- prio qdisc with 2 bands with a cgroup filter to classify packets according to their classid (packets with classid 2:2 will be affected by operations)
//	    |- (2:1) <-- first band
//	    |- (2:2) <-- second band
//	      |- (3:) <-- first operation
//	        |- (4:) <-- second operation
//	          ...
func (i *networkDisruptionInjector) applyOperations() error {
	i.tcFilterPriority = tcPriority

	// get interfaces
	links, err := i.config.NetlinkAdapter.LinkList()
	if err != nil {
		return fmt.Errorf("error listing interfaces: %w", err)
	}

	// build a map of link name and link interface
	interfaces := []string{}
	for _, link := range links {
		interfaces = append(interfaces, link.Name())
	}

	// retrieve the default route information
	defaultRoutes, err := i.config.NetlinkAdapter.DefaultRoutes()
	if err != nil {
		return fmt.Errorf("error getting the default route: %w", err)
	}

	i.config.Log.Infof("detected default gateway IPs %s", defaultRoutes)

	// get the targeted pod node IP from the environment variable
	nodeIP, ok := os.LookupEnv(env.InjectorTargetPodHostIP)
	if !ok {
		return fmt.Errorf("%s environment variable must be set with the target pod node IP", env.InjectorTargetPodHostIP)
	}

	i.config.Log.Infof("target pod node IP is %s", nodeIP)

	nodeIPNet := &net.IPNet{
		IP:   net.ParseIP(nodeIP),
		Mask: net.CIDRMask(32, 32),
	}

	// create cloud provider metadata service ipnet
	metadataIPNet := &net.IPNet{
		IP:   net.ParseIP("169.254.169.254"),
		Mask: net.CIDRMask(32, 32),
	}

	// set the tx qlen if not already set as it is required to create a prio qdisc without dropping
	// all the outgoing traffic
	// this qlen will be removed once the injection is done if it was not present before
	for _, link := range links {
		if link.TxQLen() == 0 {
			i.config.Log.Infof("setting tx qlen for interface %s", link.Name())

			// set qlen
			if err := link.SetTxQLen(1000); err != nil {
				return fmt.Errorf("can't set tx queue length on interface %s: %w", link.Name(), err)
			}

			// defer the tx qlen clear
			defer func(link network.NetlinkLink) {
				i.config.Log.Infof("clearing tx qlen for interface %s", link.Name())

				if err := link.SetTxQLen(0); err != nil {
					i.config.Log.Errorw("can't clear %s link transmission queue length: %w", link.Name(), err)
				}
			}(link)
		}
	}

	// create a new qdisc for the given interface of type prio with 4 bands instead of 3
	// we keep the default priomap, the extra band will be used to filter traffic going to the specified IP
	// we only create this qdisc if we want to target traffic going to some hosts only, it avoids to apply disruptions to all the traffic for a bit of time
	priomap := [16]uint32{1, 2, 2, 2, 1, 2, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1}

	if err := i.config.TrafficController.AddPrio(interfaces, "root", 1, 4, priomap); err != nil {
		return fmt.Errorf("can't create a new qdisc: %w", err)
	}

	// parent 1:4 refers to the 4th band of the prio qdisc
	// handle starts from 2 because 1 is used by the prio qdisc
	parent := "1:4"
	handle := uint32(2)

	// if the disruption is at pod level and there's no handler to notify,
	// create a second qdisc to filter packets coming from this specific pod processes only
	// if the disruption is applied on init, we consider that some more containers may be created within
	// the pod so we can't scope the disruption to a specific set of containers
	if i.config.Level == chaostypes.DisruptionLevelPod && !i.config.OnInit {
		// create second prio with only 2 bands to filter traffic with a specific classid
		if err := i.config.TrafficController.AddPrio(interfaces, "1:4", 2, 2, [16]uint32{}); err != nil {
			return fmt.Errorf("can't create a new qdisc: %w", err)
		}

		// create cgroup filter
		if err := i.config.TrafficController.AddCgroupFilter(interfaces, "2:0", 2); err != nil {
			return fmt.Errorf("can't create the cgroup filter: %w", err)
		}
		// parent 2:2 refers to the 2nd band of the 2nd prio qdisc
		// handle starts from 3 because 1 and 2 are used by the 2 prio qdiscs
		parent = "2:2"
		handle = uint32(3)
	}

	// add operations
	for _, operation := range i.operations {
		if err := operation(interfaces, parent, handle); err != nil {
			return fmt.Errorf("could not perform operation on newly created qdisc: %w", err)
		}

		// update parent reference and handle identifier for the next operation
		// the next operation parent will be the current handle identifier
		// the next handle identifier is just an increment of the actual one
		parent = fmt.Sprintf("%d:", handle)
		handle++
	}

	// create tc filters depending on the given hosts to match
	// redirect all packets of all interfaces if no host is given
	if len(i.spec.Hosts) == 0 && len(i.spec.Services) == 0 {
		_, nullIP, _ := net.ParseCIDR("0.0.0.0/0")

		if err := i.config.TrafficController.AddFilter(interfaces, "1:0", i.getNewPriority(), 0, nil, nullIP, 0, 0, "", "1:4"); err != nil {
			return fmt.Errorf("can't add a filter: %w", err)
		}
	} else {
		// apply filters for given hosts
		if err := i.addFiltersForHosts(interfaces, i.spec.Hosts, "1:4"); err != nil {
			return fmt.Errorf("error adding filters for given hosts: %w", err)
		}

		// add or delete filters for given services depending on changes on the destination kubernetes services and associated pods
		if err := i.handleFiltersForServices(interfaces, "1:4"); err != nil {
			return fmt.Errorf("error adding filters for given services: %w", err)
		}
	}

	// the following lines are used to exclude some critical packets from any disruption such as health check probes
	// depending on the network configuration, only one of those filters can be useful but we must add all of them
	// those filters are only added if the related interface has been impacted by a disruption so far
	// NOTE: those filters must be added after every other filters applied to the interface so they are used first
	if i.config.Level == chaostypes.DisruptionLevelPod {
		// this filter allows the pod to communicate with the default route gateway IP
		for _, defaultRoute := range defaultRoutes {
			gatewayIP := &net.IPNet{
				IP:   defaultRoute.Gateway(),
				Mask: net.CIDRMask(32, 32),
			}

			if err := i.config.TrafficController.AddFilter([]string{defaultRoute.Link().Name()}, "1:0", i.getNewPriority(), 0, nil, gatewayIP, 0, 0, "", "1:1"); err != nil {
				return fmt.Errorf("can't add the default route gateway IP filter: %w", err)
			}
		}

		// this filter allows the pod to communicate with the node IP
		if err := i.config.TrafficController.AddFilter(interfaces, "1:0", i.getNewPriority(), 0, nil, nodeIPNet, 0, 0, "", "1:1"); err != nil {
			return fmt.Errorf("can't add the target pod node IP filter: %w", err)
		}
	} else if i.config.Level == chaostypes.DisruptionLevelNode {
		// GENERIC SAFEGUARDS
		// allow SSH connections on all interfaces (port 22/tcp)
		if err := i.config.TrafficController.AddFilter(interfaces, "1:0", i.getNewPriority(), 0, nil, nil, 22, 0, "tcp", "1:1"); err != nil {
			return fmt.Errorf("error adding filter allowing SSH connections: %w", err)
		}

		// CLOUD PROVIDER SPECIFIC SAFEGUARDS
		// allow cloud provider health checks on all interfaces(arp)
		if err := i.config.TrafficController.AddFilter(interfaces, "1:0", i.getNewPriority(), 0, nil, nil, 0, 0, "arp", "1:1"); err != nil {
			return fmt.Errorf("error adding filter allowing cloud providers health checks (ARP packets): %w", err)
		}

		// allow cloud provider metadata service communication
		if err := i.config.TrafficController.AddFilter(interfaces, "1:0", i.getNewPriority(), 0, nil, metadataIPNet, 0, 0, "", "1:1"); err != nil {
			return fmt.Errorf("error adding filter allowing cloud providers health checks (ARP packets): %w", err)
		}
	}

	// add filters for allowed hosts
	if err := i.addFiltersForHosts(interfaces, i.spec.AllowedHosts, "1:1"); err != nil {
		return fmt.Errorf("error adding filter for allowed hosts: %w", err)
	}

	return nil
}

func (i *networkDisruptionInjector) getNewPriority() uint32 {
	priority := uint32(0)

	i.tcFilterMutex.Lock()
	i.tcFilterPriority++
	priority = i.tcFilterPriority
	i.tcFilterMutex.Unlock()

	return priority
}

// addServiceFilters adds a list of service tc filters on a list of interfaces
func (i *networkDisruptionInjector) addServiceFilters(serviceName string, filters []tcServiceFilter, interfaces []string, flowid string) ([]tcServiceFilter, error) {
	builtServices := []tcServiceFilter{}

	for _, filter := range filters {
		filter.priority = i.getNewPriority()

		i.config.Log.Infow("found service endpoint", "resolvedEndpoint", filter.service.String(), "resolvedService", serviceName)

		err := i.config.TrafficController.AddFilter(interfaces, "1:0", filter.priority, 0, nil, filter.service.ip, 0, filter.service.port, filter.service.protocol, flowid)
		if err != nil {
			return nil, err
		}

		builtServices = append(builtServices, filter)
	}

	return builtServices, nil
}

// removeServiceFilter delete tc filters using its priority
func (i *networkDisruptionInjector) removeServiceFilter(interfaces []string, tcFilter tcServiceFilter) error {
	for _, iface := range interfaces {
		if err := i.config.TrafficController.DeleteFilter(iface, tcFilter.priority); err != nil {
			return err
		}
	}

	i.config.Log.Infow(fmt.Sprintf("deleted a tc filter on %s", tcFilter.service.String()), "interfaces", strings.Join(interfaces, ", "))

	return nil
}

// removeServiceFiltersInList delete a list of tc filters inside of another list of tc filters
func (i *networkDisruptionInjector) removeServiceFiltersInList(interfaces []string, tcFilters []tcServiceFilter, tcFiltersToRemove []tcServiceFilter) ([]tcServiceFilter, error) {
	for _, serviceToRemove := range tcFiltersToRemove {
		if deletedIdx := i.findServiceFilter(tcFilters, serviceToRemove); deletedIdx >= 0 {
			if err := i.removeServiceFilter(interfaces, tcFilters[deletedIdx]); err != nil {
				return nil, err
			}

			tcFilters = append(tcFilters[:deletedIdx], tcFilters[deletedIdx+1:]...)
		}
	}

	return tcFilters, nil
}

// buildServiceFiltersFromPod builds a list of tc filters per pod endpoint using the service ports
func (i *networkDisruptionInjector) buildServiceFiltersFromPod(pod v1.Pod, servicePorts []v1.ServicePort) []tcServiceFilter {
	// compute endpoint IP (pod IP)
	_, endpointIP, _ := net.ParseCIDR(fmt.Sprintf("%s/32", pod.Status.PodIP))

	endpointsToWatch := []tcServiceFilter{}

	for _, port := range servicePorts {
		filter := tcServiceFilter{
			service: networkDisruptionService{
				ip:       endpointIP,
				port:     int(port.TargetPort.IntVal),
				protocol: string(port.Protocol),
			},
		}

		if i.findServiceFilter(endpointsToWatch, filter) == -1 { // forbid duplication
			endpointsToWatch = append(endpointsToWatch, filter)
		}
	}

	return endpointsToWatch
}

// buildServiceFiltersFromService builds a list of tc filters per service using the service ports
func (i *networkDisruptionInjector) buildServiceFiltersFromService(service v1.Service, servicePorts []v1.ServicePort) []tcServiceFilter {
	// compute service IP (cluster IP)
	_, serviceIP, _ := net.ParseCIDR(fmt.Sprintf("%s/32", service.Spec.ClusterIP))

	endpointsToWatch := []tcServiceFilter{}

	if isHeadless(service) {
		return endpointsToWatch
	}

	for _, port := range servicePorts {
		filter := tcServiceFilter{
			service: networkDisruptionService{
				ip:       serviceIP,
				port:     int(port.Port),
				protocol: string(port.Protocol),
			},
		}

		if i.findServiceFilter(endpointsToWatch, filter) == -1 { // forbid duplication
			endpointsToWatch = append(endpointsToWatch, filter)
		}
	}

	return endpointsToWatch
}

func (i *networkDisruptionInjector) handleWatchError(event watch.Event) error {
	err, ok := event.Object.(*metav1.Status)
	if ok {
		return fmt.Errorf("couldn't watch service in namespace: %s", err.Message)
	}

	return fmt.Errorf("couldn't watch service in namespace")
}

func (i *networkDisruptionInjector) findServiceFilter(tcFilters []tcServiceFilter, toFind tcServiceFilter) int {
	for idx, tcFilter := range tcFilters {
		if tcFilter.service.String() == toFind.service.String() {
			return idx
		}
	}

	return -1
}

// handlePodEndpointsOnServicePortsChange on service changes, delete old filters with the wrong service ports and create new filters
func (i *networkDisruptionInjector) handlePodEndpointsServiceFiltersOnKubernetesServiceChanges(serviceSpec v1beta1.NetworkDisruptionServiceSpec, oldFilters []tcServiceFilter, pods []v1.Pod, servicePorts []v1.ServicePort, interfaces []string, flowid string) ([]tcServiceFilter, error) {
	tcFiltersToCreate, finalTcFilters := []tcServiceFilter{}, []tcServiceFilter{}

	for _, pod := range pods {
		if pod.Status.PodIP != "" { // pods without ip are newly created and will be picked up in the other watcher
			tcFiltersToCreate = append(tcFiltersToCreate, i.buildServiceFiltersFromPod(pod, servicePorts)...) // we build the updated list of tc filters
		}
	}

	// update the list of tc filters by deleting old ones not in the new list of tc filters and creating new tc filters
	for _, oldFilter := range oldFilters {
		if idx := i.findServiceFilter(tcFiltersToCreate, oldFilter); idx >= 0 {
			finalTcFilters = append(finalTcFilters, oldFilter)
			tcFiltersToCreate = append(tcFiltersToCreate[:idx], tcFiltersToCreate[idx+1:]...)
		} else { // delete tc filters which are not in the updated list of tc filters
			if err := i.removeServiceFilter(interfaces, oldFilter); err != nil {
				return nil, err
			}
		}
	}

	createdTcFilters, err := i.addServiceFilters(serviceSpec.Name, tcFiltersToCreate, interfaces, flowid)
	if err != nil {
		return nil, err
	}

	return append(finalTcFilters, createdTcFilters...), nil
}

// handleKubernetesPodsChanges for every changes happening in the kubernetes service destination, we update the tc service filters
func (i *networkDisruptionInjector) handleKubernetesServiceChanges(event watch.Event, watcher *serviceWatcher, interfaces []string, flowid string) error {
	var err error

	if event.Type == watch.Error {
		return i.handleWatchError(event)
	}

	service, ok := event.Object.(*v1.Service)
	if !ok {
		return fmt.Errorf("couldn't watch service in namespace, invalid type of watched object received")
	}

	// keep track of resource version to continue watching pods when the watcher has timed out
	// at the right resource already computed.
	if event.Type == watch.Bookmark {
		watcher.servicesResourceVersion = service.ResourceVersion

		return nil
	}

	// We just watch the specified name service
	if watcher.watchedServiceSpec.Name != service.Name {
		return nil
	}

	if err := i.config.Netns.Enter(); err != nil {
		return fmt.Errorf("unable to enter the given container network namespace: %w", err)
	}

	podList, err := i.config.K8sClient.CoreV1().Pods(watcher.watchedServiceSpec.Namespace).List(context.Background(), metav1.ListOptions{
		LabelSelector: labels.SelectorFromValidatedSet(service.Spec.Selector).String(),
	})
	if err != nil {
		return fmt.Errorf("error watching the list of pods for the given kubernetes service (%s/%s): %w", service.Namespace, service.Name, err)
	}

	if isHeadless(*service) {
		// If this is a headless service, we want to block all traffic to the endpoint IPs
		watcher.servicePorts = append(watcher.servicePorts, v1.ServicePort{Port: 0})
	} else {
		watcher.servicePorts = service.Spec.Ports
	}

	watcher.tcFiltersFromPodEndpoints, err = i.handlePodEndpointsServiceFiltersOnKubernetesServiceChanges(watcher.watchedServiceSpec, watcher.tcFiltersFromPodEndpoints, podList.Items, service.Spec.Ports, interfaces, flowid)
	if err != nil {
		return err
	}

	nsServicesTcFilters := i.buildServiceFiltersFromService(*service, service.Spec.Ports)

	switch event.Type {
	case watch.Added:
		createdTcFilters, err := i.addServiceFilters(watcher.watchedServiceSpec.Name, nsServicesTcFilters, interfaces, flowid)
		if err != nil {
			return err
		}

		watcher.tcFiltersFromNamespaceServices = append(watcher.tcFiltersFromNamespaceServices, createdTcFilters...)
	case watch.Modified:
		if _, err := i.removeServiceFiltersInList(interfaces, watcher.tcFiltersFromNamespaceServices, watcher.tcFiltersFromNamespaceServices); err != nil {
			return err
		}

		watcher.tcFiltersFromNamespaceServices, err = i.addServiceFilters(watcher.watchedServiceSpec.Name, nsServicesTcFilters, interfaces, flowid)
		if err != nil {
			return err
		}
	case watch.Deleted:
		watcher.tcFiltersFromNamespaceServices, err = i.removeServiceFiltersInList(interfaces, watcher.tcFiltersFromNamespaceServices, nsServicesTcFilters)
		if err != nil {
			return err
		}
	}

	if err := i.config.Netns.Exit(); err != nil {
		return fmt.Errorf("unable to exit the given container network namespace: %w", err)
	}

	return nil
}

// handleKubernetesPodsChanges for every changes happening in the pods related to the kubernetes service destination, we update the tc service filters
func (i *networkDisruptionInjector) handleKubernetesPodsChanges(event watch.Event, watcher *serviceWatcher, interfaces []string, flowid string) error {
	var err error

	if event.Type == watch.Error {
		return i.handleWatchError(event)
	}

	pod, ok := event.Object.(*v1.Pod)
	if !ok {
		return fmt.Errorf("couldn't watch pods in namespace, invalid type of watched object received")
	}

	// keep track of resource version to continue watching pods when the watcher has timed out
	// at the right resource already computed.
	if event.Type == watch.Bookmark {
		watcher.servicesResourceVersion = pod.ResourceVersion

		return nil
	}

	if err = i.config.Netns.Enter(); err != nil {
		return fmt.Errorf("unable to enter the given container network namespace: %w", err)
	}

	tcFiltersFromPod := i.buildServiceFiltersFromPod(*pod, watcher.servicePorts)

	switch event.Type {
	case watch.Added:
		// if the filter already exists, we do nothing
		if i.findServiceFilter(watcher.tcFiltersFromPodEndpoints, tcFiltersFromPod[0]) >= 0 {
			break
		}

		if pod.Status.PodIP != "" {
			createdTcFilters, err := i.addServiceFilters(watcher.watchedServiceSpec.Name, tcFiltersFromPod, interfaces, flowid)
			if err != nil {
				return err
			}

			watcher.tcFiltersFromPodEndpoints = append(watcher.tcFiltersFromPodEndpoints, createdTcFilters...)
		} else {
			i.config.Log.Infow("newly created destination port has no IP yet, adding to the watch list of pods", "destinationPodName", pod.Name)

			watcher.podsWithoutIPs = append(watcher.podsWithoutIPs, pod.Name)
		}
	case watch.Modified:
		// From the list of pods without IPs that has been added, we create the one that got the IP assigned
		podToCreateIdx := -1

		for idx, podName := range watcher.podsWithoutIPs {
			if podName == pod.Name && pod.Status.PodIP != "" {
				podToCreateIdx = idx

				break
			}
		}

		if podToCreateIdx > -1 {
			tcFilters, err := i.addServiceFilters(watcher.watchedServiceSpec.Name, tcFiltersFromPod, interfaces, flowid)
			if err != nil {
				return err
			}

			watcher.tcFiltersFromPodEndpoints = append(watcher.tcFiltersFromPodEndpoints, tcFilters...)
			watcher.podsWithoutIPs = append(watcher.podsWithoutIPs[:podToCreateIdx], watcher.podsWithoutIPs[podToCreateIdx+1:]...)
		}
	case watch.Deleted:
		watcher.tcFiltersFromPodEndpoints, err = i.removeServiceFiltersInList(interfaces, watcher.tcFiltersFromPodEndpoints, tcFiltersFromPod)
		if err != nil {
			return err
		}
	}

	if err := i.config.Netns.Exit(); err != nil {
		return fmt.Errorf("unable to exit the given container network namespace: %w", err)
	}

	return nil
}

// watchServiceChanges for every changes happening in the kubernetes service destination or in the pods related to the kubernetes service destination, we update the tc service filters
func (i *networkDisruptionInjector) watchServiceChanges(watcher serviceWatcher, interfaces []string, flowid string) {
	for {
		// We create the watcher channels when it's closed
		if watcher.kubernetesServiceWatcher == nil {
			serviceWatcher, err := i.config.K8sClient.CoreV1().Services(watcher.watchedServiceSpec.Namespace).Watch(context.Background(), metav1.ListOptions{
				ResourceVersion:     watcher.servicesResourceVersion,
				AllowWatchBookmarks: true,
			})
			if err != nil {
				i.config.Log.Errorf("error watching the changes for the given kubernetes service (%s/%s): %w", watcher.watchedServiceSpec.Namespace, watcher.watchedServiceSpec.Name, err)

				return
			}

			i.config.Log.Infow("starting kubernetes service watch", "serviceName", watcher.watchedServiceSpec.Name, "serviceNamespace", watcher.watchedServiceSpec.Namespace)
			watcher.kubernetesServiceWatcher = serviceWatcher.ResultChan()
		}

		if watcher.kubernetesPodEndpointsWatcher == nil {
			podsWatcher, err := i.config.K8sClient.CoreV1().Pods(watcher.watchedServiceSpec.Namespace).Watch(context.Background(), metav1.ListOptions{
				LabelSelector:       watcher.labelServiceSelector,
				ResourceVersion:     watcher.podsResourceVersion,
				AllowWatchBookmarks: true,
			})
			if err != nil {
				i.config.Log.Errorf("error watching the list of pods for the given kubernetes service (%s/%s): %w", watcher.watchedServiceSpec.Namespace, watcher.watchedServiceSpec.Name, err)

				return
			}

			i.config.Log.Infow("starting kubernetes pods watch", "serviceName", watcher.watchedServiceSpec.Name, "serviceNamespace", watcher.watchedServiceSpec.Namespace)
			watcher.kubernetesPodEndpointsWatcher = podsWatcher.ResultChan()
		}

		select {
		case state := <-i.config.State.State:
			if state == Cleaned {
				return
			}
		case event, ok := <-watcher.kubernetesServiceWatcher: // We have changes in the service watched
			if !ok { // channel is closed
				watcher.kubernetesServiceWatcher = nil
			} else {
				i.config.Log.Debugw(fmt.Sprintf("changes in service %s/%s", watcher.watchedServiceSpec.Name, watcher.watchedServiceSpec.Namespace), "eventType", event.Type)

				if err := i.handleKubernetesServiceChanges(event, &watcher, interfaces, flowid); err != nil {
					i.config.Log.Errorf("couldn't apply changes to tc filters: %w... Rebuilding watcher", err)

					if _, err = i.removeServiceFiltersInList(interfaces, watcher.tcFiltersFromNamespaceServices, watcher.tcFiltersFromNamespaceServices); err != nil {
						i.config.Log.Errorf("couldn't clean list of tc filters: %w", err)
					}

					watcher.kubernetesServiceWatcher = nil // restart the watcher in case of error
					watcher.tcFiltersFromNamespaceServices = []tcServiceFilter{}
				}
			}
		case event, ok := <-watcher.kubernetesPodEndpointsWatcher: // We have changes in the pods watched
			if !ok { // channel is closed
				watcher.kubernetesPodEndpointsWatcher = nil
			} else {
				i.config.Log.Debugw(fmt.Sprintf("changes in pods of service %s/%s", watcher.watchedServiceSpec.Name, watcher.watchedServiceSpec.Namespace), "eventType", event.Type)

				if err := i.handleKubernetesPodsChanges(event, &watcher, interfaces, flowid); err != nil {
					i.config.Log.Errorf("couldn't apply changes to tc filters: %w... Rebuilding watcher", err)

					if _, err = i.removeServiceFiltersInList(interfaces, watcher.tcFiltersFromPodEndpoints, watcher.tcFiltersFromPodEndpoints); err != nil {
						i.config.Log.Errorf("couldn't clean list of tc filters: %w", err)
					}

					watcher.kubernetesPodEndpointsWatcher = nil // restart the watcher in case of error
					watcher.tcFiltersFromPodEndpoints = []tcServiceFilter{}
				}
			}
		}
	}
}

// handleFiltersForServices creates tc filters on given interfaces for services in disruption spec classifying matching packets in the given flowid
func (i *networkDisruptionInjector) handleFiltersForServices(interfaces []string, flowid string) error {
	// build the watchers to handle changes in services and pod endpoints
	serviceWatchers := []serviceWatcher{}

	for _, serviceSpec := range i.spec.Services {
		// retrieve serviceSpec
		k8sService, err := i.config.K8sClient.CoreV1().Services(serviceSpec.Namespace).Get(context.Background(), serviceSpec.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("error getting the given kubernetes service (%s/%s): %w", serviceSpec.Namespace, serviceSpec.Name, err)
		}

		serviceWatcher := serviceWatcher{
			watchedServiceSpec:   serviceSpec,
			servicePorts:         k8sService.Spec.Ports,
			labelServiceSelector: labels.SelectorFromValidatedSet(k8sService.Spec.Selector).String(), // keep this information to later create watchers on resources destination

			kubernetesPodEndpointsWatcher: nil,                 // watch pods related to the kubernetes service filtered on
			tcFiltersFromPodEndpoints:     []tcServiceFilter{}, // list of tc filters targeting pods related to the kubernetes service filtered on
			podsWithoutIPs:                []string{},          // some pods are created without IPs. We keep track of them to later create a tc filter on update
			podsResourceVersion:           "",

			kubernetesServiceWatcher:       nil,                 // watch service filtered on
			tcFiltersFromNamespaceServices: []tcServiceFilter{}, // list of tc filters targeting the service filtered on
			servicesResourceVersion:        "",
		}

		serviceWatchers = append(serviceWatchers, serviceWatcher)
	}

	for _, serviceWatcher := range serviceWatchers {
		go i.watchServiceChanges(serviceWatcher, interfaces, flowid)
	}

	return nil
}

// addFiltersForHosts creates tc filters on given interfaces for given hosts classifying matching packets in the given flowid
func (i *networkDisruptionInjector) addFiltersForHosts(interfaces []string, hosts []v1beta1.NetworkDisruptionHostSpec, flowid string) error {
	for _, host := range hosts {
		// resolve given hosts if needed
		ips, err := resolveHost(i.config.DNSClient, host.Host)
		if err != nil {
			return fmt.Errorf("error resolving given host %s: %w", host.Host, err)
		}

		i.config.Log.Infof("resolved %s as %s", host.Host, ips)

		for _, ip := range ips {
			// handle flow direction
			var (
				srcPort, dstPort int
				srcIP, dstIP     *net.IPNet
			)

			switch host.Flow {
			case v1beta1.FlowIngress:
				srcPort = host.Port
				srcIP = ip
			default:
				dstPort = host.Port
				dstIP = ip
			}

			// create tc filter
			if err := i.config.TrafficController.AddFilter(interfaces, "1:0", i.getNewPriority(), 0, srcIP, dstIP, srcPort, dstPort, host.Protocol, flowid); err != nil {
				return fmt.Errorf("error adding filter for host %s: %w", host.Host, err)
			}
		}
	}

	return nil
}

// AddNetem adds network disruptions using the drivers in the networkDisruptionInjector
func (i *networkDisruptionInjector) addNetemOperation(delay, delayJitter time.Duration, drop int, corrupt int, duplicate int) {
	// closure which adds netem disruptions
	operation := func(interfaces []string, parent string, handle uint32) error {
		return i.config.TrafficController.AddNetem(interfaces, parent, handle, delay, delayJitter, drop, corrupt, duplicate)
	}

	i.operations = append(i.operations, operation)
}

// AddOutputLimit adds a network bandwidth disruption using the drivers in the networkDisruptionInjector
func (i *networkDisruptionInjector) addOutputLimitOperation(bytesPerSec uint) {
	// closure which adds a bandwidth limit
	operation := func(interfaces []string, parent string, handle uint32) error {
		return i.config.TrafficController.AddOutputLimit(interfaces, parent, handle, bytesPerSec)
	}

	i.operations = append(i.operations, operation)
}

// clearOperations removes all disruptions by clearing all custom qdiscs created for the given config struct (filters will be deleted as well)
func (i *networkDisruptionInjector) clearOperations() error {
	i.config.Log.Infof("clearing root qdiscs")

	// get all interfaces
	links, err := i.config.NetlinkAdapter.LinkList()
	if err != nil {
		return fmt.Errorf("can't get interfaces per IP map: %w", err)
	}

	// clear all interfaces root qdisc so it gets back to default
	interfaces := []string{}
	for _, link := range links {
		interfaces = append(interfaces, link.Name())
	}

	// clear link qdisc if needed
	if err := i.config.TrafficController.ClearQdisc(interfaces); err != nil {
		return fmt.Errorf("error deleting root qdisc: %w", err)
	}

	return nil
}

// isHeadless returns true if the service is a headless service, i.e., has no defined ClusterIP
func isHeadless(service v1.Service) bool {
	return service.Spec.ClusterIP == "" || strings.ToLower(service.Spec.ClusterIP) == "none"
}
