// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-Present Datadog, Inc.

package cloudfoundry

import (
	"fmt"
	"net"
	"sync"
	"time"

	"code.cloudfoundry.org/garden"
	"code.cloudfoundry.org/garden/client"
	"code.cloudfoundry.org/garden/client/connection"
	"github.com/DataDog/datadog-agent/pkg/util/containers"
	"github.com/DataDog/datadog-agent/pkg/util/containers/metrics"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-agent/pkg/util/retry"
)

var (
	globalGardenUtil     *GardenUtil
	globalGardenUtilLock sync.Mutex
)

// GardenUtil wraps interactions with a local garden API.
type GardenUtil struct {
	retrier retry.Retrier
	cli     client.Client
}

func GetGardenUtil() (*GardenUtil, error) {
	globalGardenUtilLock.Lock()
	defer globalGardenUtilLock.Unlock()
	network := "unix"
	address := "/var/vcap/data/garden/garden.sock"
	if globalGardenUtil == nil {
		globalGardenUtil = &GardenUtil{
			cli: client.New(connection.New(network, address)),
		}
		globalGardenUtil.retrier.SetupRetrier(&retry.Config{
			Name:          "gardenUtil",
			AttemptMethod: globalGardenUtil.cli.Ping,
			Strategy:      retry.RetryCount,
			RetryCount:    10,
			RetryDelay:    30 * time.Second,
		})
	}
	if err := globalGardenUtil.retrier.TriggerRetry(); err != nil {
		log.Debugf("could not initiate connection to garden server %s using network %s: %s", address, network, err)
		return nil, err
	}
	return globalGardenUtil, nil
}

func (gu *GardenUtil) gardenContainers() ([]*containers.Container, error) {
	gardenContainers, err := gu.cli.Containers(nil)
	if err != nil {
		return nil, fmt.Errorf("error listing garden containers: %v", err)
	}

	var containerList = make([]*containers.Container, len(gardenContainers))
	var containerMap = make(map[string]*garden.Container, len(gardenContainers))
	handles := make([]string, len(gardenContainers))
	for i, gardenContainer := range gardenContainers {
		handles[i] = gardenContainer.Handle()
		containerMap[gardenContainer.Handle()] = &gardenContainer
	}
	gardenContainerInfo, err := gu.cli.BulkInfo(handles)
	if err != nil {
		return nil, fmt.Errorf("error getting info for garden containers: %v", err)
	}

	for i, handle := range handles {
		infoEntry := gardenContainerInfo[handle]
		if err := infoEntry.Err; err != nil {
			log.Debugf("could not get info for container %s: %v", handle, err)
			continue
		}
		container := containers.Container{
			Type:        "garden",
			ID:          handle,
			EntityID:    containers.BuildTaggerEntityName(handle),
			Name:        handle,
			Image:       "",
			ImageID:     "",
			State:       infoEntry.Info.State,
			Excluded:    false,
			Health:      "",
			AddressList: parseContainerPorts(infoEntry.Info),
		}
		containerList[i] = &container
	}
	return containerList, nil
}

func (gu *GardenUtil) ListContainers() ([]*containers.Container, error) {
	cList, err := gu.gardenContainers()
	if err != nil {
		return nil, fmt.Errorf("could not get docker containers: %s", err)
	}

	cgByContainer, err := metrics.ScrapeAllCgroups()
	if err != nil {
		return nil, fmt.Errorf("could not get cgroups: %s", err)
	}
	for _, container := range cList {
		if container.State != containers.ContainerActiveState {
			continue
		}
		cgroup, ok := cgByContainer[container.ID]
		if !ok {
			log.Debugf("No matching cgroups for container %s, skipping", container.ID[:12])
			continue
		}
		container.SetCgroups(cgroup)
		err = container.FillCgroupLimits()
		if err != nil {
			log.Debugf("Cannot get limits for container %s: %s, skipping", container.ID[:12], err)
			continue
		}
	}
	return cList, nil
}

func (gu *GardenUtil) UpdateContainerMetrics(cList []*containers.Container) error {
	for _, container := range cList {
		if container.State != containers.ContainerActiveState {
			continue
		}

		err := container.FillCgroupMetrics()
		if err != nil {
			log.Debugf("Cannot get metrics for container %s: %s", container.ID[:12], err)
			continue
		}
	}
	return nil
}

func setContainerLimits(container *containers.Container, gardenContainer *garden.Container) {
	cpuLimits, err := (*gardenContainer).CurrentCPULimits()
	if err != nil {
		log.Debugf("Error getting CPU limits for garden container %s: %v", container.ID, err)
	} else {
		if cpuLimits.Weight != 0 {
			container.CPULimit = float64(cpuLimits.Weight)
		} else {
			container.CPULimit = float64(cpuLimits.LimitInShares)
		}
	}
	memLimits, err := (*gardenContainer).CurrentMemoryLimits()
	if err != nil {
		log.Debugf("Error getting memory limits for garden container %s: %v", container.ID, err)
	} else {
		container.MemLimit = memLimits.LimitInBytes
	}
}

func parseContainerPorts(info garden.ContainerInfo) []containers.NetworkAddress {
	var addresses = make([]containers.NetworkAddress, len(info.MappedPorts))
	for i, port := range info.MappedPorts {
		addresses[i] = containers.NetworkAddress{
			IP:       net.IP(info.ExternalIP),
			Port:     int(port.HostPort),
			Protocol: "tcp",
		}
	}
	return addresses
}

func setContainerMetrics(container *containers.Container, gardenMetrics garden.Metrics) {
	container.Memory = &metrics.CgroupMemStat{
		ContainerID:             container.ID,
		Cache:                   gardenMetrics.MemoryStat.Cache,
		Swap:                    gardenMetrics.MemoryStat.Swap,
		SwapPresent:             true,
		RSS:                     gardenMetrics.MemoryStat.Rss,
		MappedFile:              gardenMetrics.MemoryStat.MappedFile,
		Pgpgin:                  gardenMetrics.MemoryStat.Pgpgin,
		Pgpgout:                 gardenMetrics.MemoryStat.Pgpgout,
		Pgfault:                 gardenMetrics.MemoryStat.Pgfault,
		Pgmajfault:              gardenMetrics.MemoryStat.Pgmajfault,
		InactiveAnon:            gardenMetrics.MemoryStat.InactiveAnon,
		ActiveAnon:              gardenMetrics.MemoryStat.ActiveAnon,
		InactiveFile:            gardenMetrics.MemoryStat.InactiveFile,
		ActiveFile:              gardenMetrics.MemoryStat.ActiveFile,
		Unevictable:             gardenMetrics.MemoryStat.Unevictable,
		HierarchicalMemoryLimit: gardenMetrics.MemoryStat.HierarchicalMemoryLimit,
		HierarchicalMemSWLimit:  gardenMetrics.MemoryStat.HierarchicalMemswLimit,
		TotalCache:              gardenMetrics.MemoryStat.TotalCache,
		TotalRSS:                gardenMetrics.MemoryStat.TotalRss,
		TotalMappedFile:         gardenMetrics.MemoryStat.TotalMappedFile,
		TotalPgpgIn:             gardenMetrics.MemoryStat.TotalPgpgin,
		TotalPgpgOut:            gardenMetrics.MemoryStat.TotalPgpgout,
		TotalPgFault:            gardenMetrics.MemoryStat.TotalPgfault,
		TotalPgMajFault:         gardenMetrics.MemoryStat.TotalPgmajfault,
		TotalInactiveAnon:       gardenMetrics.MemoryStat.TotalInactiveAnon,
		TotalActiveAnon:         gardenMetrics.MemoryStat.TotalActiveAnon,
		TotalInactiveFile:       gardenMetrics.MemoryStat.TotalInactiveFile,
		TotalActiveFile:         gardenMetrics.MemoryStat.TotalActiveFile,
		TotalUnevictable:        gardenMetrics.MemoryStat.TotalUnevictable,
		MemUsageInBytes:         gardenMetrics.MemoryStat.TotalUsageTowardLimit,
	}
	container.CPU = &metrics.CgroupTimesStat{
		ContainerID: container.ID,
		System:      gardenMetrics.CPUStat.System / 1e7,
		User:        gardenMetrics.CPUStat.User / 1e7,
		SystemUsage: gardenMetrics.CPUStat.Usage / 1e7,
	}
	container.Network = metrics.ContainerNetStats{
		&metrics.InterfaceNetStats{
			NetworkName: "default",
			BytesSent:   gardenMetrics.NetworkStat.TxBytes,
			BytesRcvd:   gardenMetrics.NetworkStat.RxBytes,
		},
	}
}
