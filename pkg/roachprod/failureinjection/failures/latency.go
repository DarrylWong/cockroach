// Copyright 2025 The Cockroach Authors.
//
// Use of this software is governed by the CockroachDB Software License
// included in the /LICENSE file.

package failures

import (
	"context"
	"fmt"
	"github.com/cockroachdb/cockroach/pkg/roachprod"
	"github.com/cockroachdb/cockroach/pkg/roachprod/install"
	"github.com/cockroachdb/cockroach/pkg/roachprod/logger"
	"time"
)

// NetworkLatency is a failure mode that injects artificial network latency between nodes
// using tc and iptables.
type NetworkLatency struct {
	GenericFailure
}

// ArtificialLatency represents the one way artificial (added) latency from one group of nodes to another.
type ArtificialLatency struct {
	Source      install.Nodes
	Destination install.Nodes
	Delay       time.Duration
}

type NetworkLatencyArgs struct {
	ArtificialLatencies []ArtificialLatency
}

func registerNetworkLatencyFailure(r *FailureRegistry) {
	r.add(NetworkLatencyName, NetworkLatencyArgs{}, MakeNetworkLatencyFailure)
}

func MakeNetworkLatencyFailure(
	clusterName string, l *logger.Logger, secure bool,
) (FailureMode, error) {
	c, err := roachprod.GetClusterFromCache(l, clusterName, install.SecureOption(secure))
	if err != nil {
		return nil, err
	}

	genericFailure := GenericFailure{c: c, runTitle: "latency"}
	return &NetworkLatency{genericFailure}, nil
}

const NetworkLatencyName = "network-latency"

func (f *NetworkLatency) Description() string {
	return NetworkLatencyName
}

func (f *NetworkLatency) Setup(ctx context.Context, l *logger.Logger, args FailureArgs) error {
	_ = f.Restore(ctx, l, args)
	return nil
}

const (
	addFilterCmd = `
sudo tc qdisc replace dev %[1]s root handle 1: htb default 12
sudo tc class add dev %[1]s parent 1: classid 1:1 htb rate 1000mbit
sudo tc filter add dev %[1]s parent 1: protocol ip u32 match ip dst {ip:%[3]d}/32 flowid 1:1
sudo tc filter add dev %[1]s parent 1: protocol ip u32 match ip dst {ip:%[3]d:public}/32 flowid 1:1
sudo tc qdisc add dev %[1]s parent 1:1 handle 10: netem delay %[2]s
`
	removeFilterCmd = `
sudo tc qdisc del dev %s root
`
)

func constructAddNetworkLatencyCmd(targetNode install.Node, interfaces []string, addedLatency time.Duration) string {
	var filters string
	for _, iface := range interfaces {
		filters = filters + fmt.Sprintf(addFilterCmd, iface, addedLatency, targetNode)
	}

	return filters
}

// TODO: make this remove only the ip rules added before
func constructRemoveNetworkLatencyCmd(targetNode install.Node, interfaces []string) string {
	var filters string
	for _, iface := range interfaces {
		filters = filters + fmt.Sprintf(removeFilterCmd, iface)
	}

	return filters
}

func (f *NetworkLatency) Inject(ctx context.Context, l *logger.Logger, args FailureArgs) error {
	latencies := args.(NetworkLatencyArgs).ArtificialLatencies
	for _, latency := range latencies {
		for _, dest := range latency.Destination {
			interfaces, err := f.NetworkInterfaces(ctx, l)
			if err != nil {
				return err
			}
			cmd := constructAddNetworkLatencyCmd(dest, interfaces, latency.Delay)
			l.Printf("Adding artificial latency from nodes %d to node %d with cmd: %s", latency.Source, dest, cmd)
			if err := f.Run(ctx, l, latency.Source, cmd); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *NetworkLatency) Restore(ctx context.Context, l *logger.Logger, args FailureArgs) error {
	latencies := args.(NetworkLatencyArgs).ArtificialLatencies
	for _, latency := range latencies {
		for _, dest := range latency.Destination {
			interfaces, err := f.NetworkInterfaces(ctx, l)
			if err != nil {
				return err
			}
			cmd := constructRemoveNetworkLatencyCmd(dest, interfaces)
			l.Printf("Removing artificial latency from nodes %d to node %d with cmd: %s", latency.Source, dest, cmd)
			if err := f.Run(ctx, l, latency.Source, cmd); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *NetworkLatency) Cleanup(ctx context.Context, l *logger.Logger, args FailureArgs) error {
	// TODO
	return nil
}

func (f *NetworkLatency) WaitForFailureToPropagate(ctx context.Context, l *logger.Logger, args FailureArgs) error {
	// TODO
	return nil
}

func (f *NetworkLatency) WaitForFailureToRestore(ctx context.Context, l *logger.Logger, args FailureArgs) error {
	// TODO
	return nil
}
