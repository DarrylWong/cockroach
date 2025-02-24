package failureinjection

import (
	"context"
	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/cluster"
	"github.com/cockroachdb/cockroach/pkg/roachprod/failureinjection/failures"
	"github.com/cockroachdb/cockroach/pkg/roachprod/install"
	"github.com/cockroachdb/cockroach/pkg/roachprod/logger"
	"time"
)

type ArtificialLatencyInjector struct {
	c      cluster.Cluster
	l      *logger.Logger
	failer failures.FailureMode
}

func MakeArtificialLatencyInjector(c cluster.Cluster, l *logger.Logger) (*ArtificialLatencyInjector, error) {
	latencyFailer, err := failures.MakeNetworkLatencyFailure(c.MakeNodes(), l, c.IsSecure())
	if err != nil {
		return nil, err
	}
	return &ArtificialLatencyInjector{
		c:      c,
		l:      l,
		failer: latencyFailer,
	}, nil
}

type OneWayLatencies struct {
	RegionA string
	RegionB string
	Latency time.Duration
}

var MultiregionOneWayLatencyMap = func() map[string]map[string]time.Duration {
	regionLatencies := []OneWayLatencies{
		{
			RegionA: "us-east",
			RegionB: "us-west",
			Latency: 33 * time.Millisecond,
		},
		{
			RegionA: "us-east",
			RegionB: "europe-west",
			Latency: 30 * time.Millisecond,
		},
		{
			RegionA: "us-west",
			RegionB: "europe-west",
			Latency: 70 * time.Millisecond,
		},
	}

	latencyMap := make(map[string]map[string]time.Duration)
	for _, latencies := range regionLatencies {
		if _, ok := latencyMap[latencies.RegionA]; !ok {
			latencyMap[latencies.RegionA] = make(map[string]time.Duration)
		}
		if _, ok := latencyMap[latencies.RegionB]; !ok {
			latencyMap[latencies.RegionB] = make(map[string]time.Duration)
		}
		latencyMap[latencies.RegionA][latencies.RegionB] = latencies.Latency
		latencyMap[latencies.RegionB][latencies.RegionA] = latencies.Latency
	}
	return latencyMap
}

func (a *ArtificialLatencyInjector) CreateDefaultMultiRegionCluster(ctx context.Context) (func() error, error) {
	latencyMap := MultiregionOneWayLatencyMap()
	regionToNodeMap := make(map[string][]install.Node)
	artificialLatencies := make([]failures.ArtificialLatency, 0)
	for i, node := range a.c.CRDBNodes() {
		switch i % 3 {
		case 0:
			regionToNodeMap["us-east"] = append(regionToNodeMap["us-east"], install.Node(node))
		case 1:
			regionToNodeMap["us-west"] = append(regionToNodeMap["us-west"], install.Node(node))
		case 2:
			regionToNodeMap["europe-west"] = append(regionToNodeMap["europe-west"], install.Node(node))
		}
	}
	for regionA, srcNodes := range regionToNodeMap {
		for regionB, destNodes := range regionToNodeMap {
			delay := latencyMap[regionA][regionB]
			artificialLatencies = append(artificialLatencies, failures.ArtificialLatency{
				Source:      srcNodes,
				Destination: destNodes,
				Delay:       delay,
			})
		}
	}

	if err := a.failer.Inject(ctx, a.l, failures.NetworkLatencyArgs{
		ArtificialLatencies: artificialLatencies,
	}); err != nil {
		return nil, err
	}

	return func() error {
		return a.failer.Restore(ctx, a.l, failures.NetworkLatencyArgs{
			ArtificialLatencies: artificialLatencies,
		})
	}, nil
}
