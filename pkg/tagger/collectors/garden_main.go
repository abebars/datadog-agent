// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-Present Datadog, Inc.

package collectors

import (
	"fmt"
	"time"

	"github.com/DataDog/datadog-agent/pkg/errors"
	"github.com/DataDog/datadog-agent/pkg/util/cloudfoundry"
	"github.com/DataDog/datadog-agent/pkg/util/containers"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/datadog-agent/pkg/util/retry"
)

const (
	gardenCollectorName = "cloudfoundry"
)

// GardenCollector listen to the ECS agent to get ECS metadata.
// Relies on the DockerCollector to trigger deletions, it's not intended to run standalone
type GardenCollector struct {
	infoOut    chan<- []*TagInfo
	gardenUtil *cloudfoundry.GardenUtil
	lastUpdate time.Time
	updateFreq time.Duration
}

// Detect tries to connect to the Garden API
func (c *GardenCollector) Detect(out chan<- []*TagInfo) (CollectionMode, error) {
	fmt.Println("Detecting tagger hahahahahahahahahahahhahahahahah")
	var err error
	c.gardenUtil, err = cloudfoundry.GetGardenUtil()
	if err != nil {
		if retry.IsErrWillRetry(err) {
			log.Errorf("Could not connect to the local garden server: %v", err)
			return NoCollection, err
		}
		log.Errorf("Permanent failure trying to connect with the local garden server")
	}

	c.infoOut = out
	c.updateFreq = 15 * time.Second
	return PullCollection, nil
}

// Pull implements an additional time constraints to avoid exhausting the kube-apiserver
func (c *GardenCollector) Pull() error {
	fmt.Println("Pulling tags hahahahahahahahahahahhahahahahah")
	// Time constraints, get the delta in seconds to display it in the logs:
	timeDelta := c.lastUpdate.Add(c.updateFreq).Unix() - time.Now().Unix()
	if timeDelta > 0 {
		log.Tracef("skipping, next effective Pull will be in %d seconds", timeDelta)
		return nil
	}

	containers, err := c.gardenUtil.ListContainers()
	if err != nil {
		return err
	}

	var tagInfo = make([]*TagInfo, len(containers))
	for i, c := range containers {
		tagInfo[i] = &TagInfo{
			Source:               gardenCollectorName,
			Entity:               c.EntityID,
			HighCardTags:         []string{fmt.Sprintf("container_name:%s", c.EntityID)},
			OrchestratorCardTags: nil,
			LowCardTags:          nil,
			DeleteEntity:         false,
			CacheMiss:            false,
		}
	}
	c.infoOut <- tagInfo
	c.lastUpdate = time.Now()
	return nil
}

// Pull implements an additional time constraints to avoid exhausting the kube-apiserver
func (c *GardenCollector) Fetch(entity string) ([]string, []string, []string, error) {
	fmt.Print("Fetching tags for ")
	fmt.Print(entity)
	fmt.Println(" hahahahahahahahahahahhahahahahah")

	var tagInfos = []*TagInfo{
		{
			Source:               gardenCollectorName,
			Entity:               containers.ContainerIDForEntity(entity),
			HighCardTags:         []string{fmt.Sprintf("container_name:%s", entity)},
			OrchestratorCardTags: nil,
			LowCardTags:          nil,
			DeleteEntity:         false,
			CacheMiss:            false,
		},
	}
	c.infoOut <- tagInfos
	for _, info := range tagInfos {
		if info.Entity == entity {
			return info.LowCardTags, info.OrchestratorCardTags, info.HighCardTags, nil
		}
	}
	return nil, nil, nil, errors.NewNotFound(entity)
}

func gardenFactory() Collector {
	return &GardenCollector{}
}

func init() {
	registerCollector(gardenCollectorName, gardenFactory, NodeRuntime)
}
