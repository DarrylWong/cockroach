// Copyright 2025 The Cockroach Authors.
//
// Use of this software is governed by the CockroachDB Software License
// included in the /LICENSE file.

package tests

import (
	"context"
	"fmt"
	"github.com/cockroachdb/cockroach/pkg/cmd/roachprod/grafana"
	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/roachtestutil/task"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/cluster"
	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/option"
	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/registry"
	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/spec"
	"github.com/cockroachdb/cockroach/pkg/cmd/roachtest/test"
	"github.com/cockroachdb/cockroach/pkg/roachprod/failureinjection/failures"
	"github.com/cockroachdb/cockroach/pkg/roachprod/install"
	"github.com/cockroachdb/cockroach/pkg/roachprod/logger"
	"github.com/cockroachdb/errors"
)

type failureSmokeTest struct {
	testName    string
	failureName string
	args        failures.FailureArgs
	runWorkload bool // Run a light workload in the background for traffic

	// Time to wait for the failure to propagate after inject/restore.
	waitForFailureToPropagate time.Duration
	// Validate that the failure was injected correctly, called after Setup() and Inject().
	validateFailure func(ctx context.Context, l *logger.Logger, c cluster.Cluster) error
	// Validate that the failure was restored correctly, called after Restore().
	validateRestore func(ctx context.Context, l *logger.Logger, c cluster.Cluster) error
}

func (t *failureSmokeTest) run(
	ctx context.Context, l *logger.Logger, c cluster.Cluster, fr *failures.FailureRegistry,
) error {
	// TODO(darryl): In the future, roachtests should interact with the failure injection library
	// through helper functions in roachtestutil so they don't have to interface with roachprod
	// directly.
	failure, err := fr.GetFailure(c.MakeNodes(c.CRDBNodes()), t.failureName, l, c.IsSecure())
	if err != nil {
		return err
	}
	if err = failure.Setup(ctx, l, t.args); err != nil {
		return err
	}
	if err = c.AddGrafanaAnnotation(ctx, l, grafana.AddAnnotationRequest{
		Text: fmt.Sprintf("%s injected", t.testName),
	}); err != nil {
		return err
	}
	if err = failure.Inject(ctx, l, t.args); err != nil {
		return err
	}

	// Allow the failure to take effect.
	if t.waitForFailureToPropagate > 0 {
		l.Printf("sleeping for %s to allow failure to take effect", t.waitForFailureToPropagate)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(t.waitForFailureToPropagate):
		}
	}

	if err = t.validateFailure(ctx, l, c); err != nil {
		return err
	}
	if err = failure.Restore(ctx, l, t.args); err != nil {
		return err
	}
	if err = c.AddGrafanaAnnotation(ctx, l, grafana.AddAnnotationRequest{
		Text: fmt.Sprintf("%s restored", t.testName),
	}); err != nil {
		return err
	}

	// Allow the cluster to return to normal.
	if t.waitForFailureToPropagate > 0 {
		l.Printf("sleeping for %s to allow cluster to return to normal", t.waitForFailureToPropagate)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(t.waitForFailureToPropagate):
		}
	}

	if err = t.validateRestore(ctx, l, c); err != nil {
		return err
	}
	if err = failure.Cleanup(ctx, l); err != nil {
		return err
	}
	return nil
}

func (t *failureSmokeTest) noopRun(
	ctx context.Context, l *logger.Logger, c cluster.Cluster, fr *failures.FailureRegistry,
) error {
	if err := t.validateFailure(ctx, l, c); err == nil {
		return errors.New("no failure was injected but validation still passed")
	}
	if err := t.validateRestore(ctx, l, c); err != nil {
		return errors.Wrapf(err, "no failure was injected but post restore validation still failed")
	}
	return nil
}

// getCPULoad checks /proc/loadavg and returns the avg run load over the last minute.
func getCPULoad(ctx context.Context, c cluster.Cluster, node option.NodeListOption, l *logger.Logger) (float64, error) {
	res, err := c.RunWithDetailsSingleNode(ctx, l, option.WithNodes(node), "cat /proc/loadavg")
	if err != nil {
		return 0, err
	}
	oneMinAvg := strings.Fields(res.Stdout)[0]
	cpuLoad, err := strconv.ParseFloat(oneMinAvg, 64)
	if err != nil {
		return 0, err
	}
	return cpuLoad, nil
}

var bidirectionalNetworkPartitionTest = failureSmokeTest{
	testName:    "bidirectional network partition",
	failureName: failures.IPTablesNetworkPartitionName,
	args: failures.NetworkPartitionArgs{
		Partitions: []failures.NetworkPartition{{
			Source:      install.Nodes{1},
			Destination: install.Nodes{3},
			Type:        failures.Bidirectional,
		}},
	},
	waitForFailureToPropagate: 5 * time.Second,
	runWorkload:               true,
	validateFailure: func(ctx context.Context, l *logger.Logger, c cluster.Cluster) error {
		blocked, err := checkPortBlocked(ctx, l, c, c.Nodes(1), c.Nodes(3))
		if err != nil {
			return err
		}
		if !blocked {
			return fmt.Errorf("expected connections from node 1 to node 3 to be blocked")
		}

		blocked, err = checkPortBlocked(ctx, l, c, c.Nodes(1), c.Nodes(2))
		if err != nil {
			return err
		}
		if blocked {
			return fmt.Errorf("expected connections from node 1 to node 2 to not be blocked")
		}
		return nil
	},
	validateRestore: func(ctx context.Context, l *logger.Logger, c cluster.Cluster) error {
		blocked, err := checkPortBlocked(ctx, l, c, c.Nodes(1), c.Nodes(3))
		if err != nil {
			return err
		}
		if blocked {
			return fmt.Errorf("expected connections from node 1 to node 3 to not be blocked")
		}
		return err
	},
}

var asymmetricNetworkPartitionTest = failureSmokeTest{
	testName:    "asymmetric network partition",
	failureName: failures.IPTablesNetworkPartitionName,
	args: failures.NetworkPartitionArgs{
		Partitions: []failures.NetworkPartition{{
			Source:      install.Nodes{1},
			Destination: install.Nodes{3},
			Type:        failures.Incoming,
		}},
	},
	waitForFailureToPropagate: 5 * time.Second,
	runWorkload:               true,
	validateFailure: func(ctx context.Context, l *logger.Logger, c cluster.Cluster) error {
		blocked, err := checkPortBlocked(ctx, l, c, c.Nodes(1), c.Nodes(3))
		if err != nil {
			return err
		}
		if blocked {
			return fmt.Errorf("expected connections from node 1 to node 3 to be open")
		}

		blocked, err = checkPortBlocked(ctx, l, c, c.Nodes(3), c.Nodes(1))
		if err != nil {
			return err
		}
		if !blocked {
			return fmt.Errorf("expected connections from node 3 to node 1 to be blocked")
		}
		return nil
	},
	validateRestore: func(ctx context.Context, l *logger.Logger, c cluster.Cluster) error {
		blocked, err := checkPortBlocked(ctx, l, c, c.Nodes(1), c.Nodes(3))
		if err != nil {
			return err
		}
		if blocked {
			return fmt.Errorf("expected connections from node 1 to node 3 to not be blocked")
		}
		return err
	},
}

var majorPageFaultTest = failureSmokeTest{
	testName:    "stress-ng page faults",
	failureName: failures.PageFaultName,
	args: failures.PageFaultArgs{
		Workers: 16,
		Nodes:   install.Nodes{1},
	},
	// The CPU can take a while to recover from stress-ng so wait for a while.
	waitForFailureToPropagate: 5 * time.Minute,
	validateFailure: func(ctx context.Context, l *logger.Logger, c cluster.Cluster) error {
		// Check the load on the first and second VMs. We expect the first VM to have a much
		// higher CPU usage due to stress-ng. Technically, the exact stat we're looking at
		// is CPU load + I/O load, but we just care to see it is higher over a period of time
		// not the specific values.
		n1CPU, err := getCPULoad(ctx, c, c.Node(1), l)
		if err != nil {
			return err
		}
		n2CPU, err := getCPULoad(ctx, c, c.Node(2), l)
		if err != nil {
			return err
		}
		l.Printf("n1 cpu load during page faults: %f", n1CPU)
		l.Printf("n2 cpu load during page faults: %f", n2CPU)
		// stress-ng should cause the CPU usage to spike way up, but since we're
		// dealing with somewhat opaque values, don't make too strong of an assertion.
		// We purposely use an idle cluster so any significant jump should be due to
		// stress-ng.
		if n1CPU < n2CPU*5 {
			return errors.Errorf("expected n1 cpu load to be at least 5x higher than n2, got %f vs %f", n1CPU, n2CPU)
		} else if n1CPU < 1 {
			// Sometimes, the load on n2 can be extremely low so also check that we aren't masking
			// an error.
			return errors.Errorf("expected n1 cpu load to be much higher than 1, go %f", n1CPU)
		}
		return nil
	},
	validateRestore: func(ctx context.Context, l *logger.Logger, c cluster.Cluster) error {
		n1CPU, err := getCPULoad(ctx, c, c.Node(1), l)
		if err != nil {
			return err
		}
		n2CPU, err := getCPULoad(ctx, c, c.Node(2), l)
		if err != nil {
			return err
		}
		l.Printf("n1 cpu load during page faults: %f", n1CPU)
		l.Printf("n2 cpu load during page faults: %f", n2CPU)
		// Similar to above, n2CPU can be very low so don't rely
		// on just the ratio.
		if n1CPU > n2CPU*5 && n1CPU > 1 {
			return errors.Errorf("expected n1 cpu load within 5x n2 cpu load, got %f vs %f", n1CPU, n2CPU)
		}
		return nil
	},
}

// Helper function that uses nmap to check if connections between two nodes is blocked.
func checkPortBlocked(
	ctx context.Context, l *logger.Logger, c cluster.Cluster, from, to option.NodeListOption,
) (bool, error) {
	if err := c.Install(ctx, l, c.CRDBNodes(), "nmap"); err != nil {
		return false, err
	}
	// `nmap -oG` example output:
	// Host: {IP} {HOST_NAME}	Status: Up
	// Host: {IP} {HOST_NAME}	Ports: 26257/open/tcp//cockroach///
	// We care about the port scan result and whether it is filtered or open.
	res, err := c.RunWithDetailsSingleNode(ctx, l, option.WithNodes(from), fmt.Sprintf("nmap -p {pgport%[1]s} {ip%[1]s} -oG - | awk '/Ports:/{print $5}'", to))
	if err != nil {
		return false, err
	}
	return strings.Contains(res.Stdout, "filtered"), nil
}

func setupFailureSmokeTests(ctx context.Context, t test.Test, c cluster.Cluster) error {
	c.Start(ctx, t.L(), option.DefaultStartOpts(), install.MakeClusterSettings(), c.CRDBNodes())
	c.Run(ctx, option.WithNodes(c.WorkloadNode()), "./cockroach workload init tpcc --warehouses=100 {pgurl:1}")
	return nil
}

func runFailureSmokeTest(ctx context.Context, t test.Test, c cluster.Cluster, noopFailer bool) {
	if err := setupFailureSmokeTests(ctx, t, c); err != nil {
		t.Fatal(err)
	}
	fr := failures.NewFailureRegistry()
	fr.Register()

	var failureSmokeTests = []failureSmokeTest{
		bidirectionalNetworkPartitionTest,
		asymmetricNetworkPartitionTest,
		majorPageFaultTest,
	}

	// Randomize the order of the tests in case any of the failures have unexpected side
	// effects that may mask failures, e.g. a cgroups disk stall isn't properly restored
	// which causes a dmsetup disk stall to appear to work even if it doesn't.
	rand.Shuffle(len(failureSmokeTests), func(i, j int) {
		failureSmokeTests[i], failureSmokeTests[j] = failureSmokeTests[j], failureSmokeTests[i]
	})

	for _, test := range failureSmokeTests {
		t.L().Printf("running %s test", test.testName)
		if noopFailer {
			if err := test.noopRun(ctx, t.L(), c, fr); err != nil {
				t.Fatal(err)
			}
		} else {
			var cancel context.CancelFunc
			if test.runWorkload {
				// Run a light workload in the background so we have some traffic in the database.
				cancel = t.GoWithCancel(func(goCtx context.Context, l *logger.Logger) error {
					c.Run(goCtx, option.WithNodes(c.WorkloadNode()), "./cockroach workload run tpcc --tolerate-errors --warehouses=100 {pgurl:1-3}")
					return nil
				}, task.WithContext(ctx), task.Name("TPCC workload"))
			}
			if err := test.run(ctx, t.L(), c, fr); err != nil {
				t.Fatal(err)
			}
			if cancel != nil {
				cancel()
			}
		}
		t.L().Printf("%s test complete", test.testName)
	}
}

func registerFISmokeTest(r registry.Registry) {
	r.Add(registry.TestSpec{
		Name:             "failure-injection-smoke-test",
		Owner:            registry.OwnerTestEng,
		Cluster:          r.MakeClusterSpec(4, spec.WorkloadNode(), spec.CPU(2), spec.WorkloadNodeCPU(2), spec.ReuseNone()),
		CompatibleClouds: registry.OnlyGCE,
		// TODO(darryl): When the FI library starts seeing more use through roachtests, CLI, etc. switch this to Nightly.
		Suites: registry.ManualOnly,
		Run: func(ctx context.Context, t test.Test, c cluster.Cluster) {
			runFailureSmokeTest(ctx, t, c, false /* noopFailer */)
		},
	})
	r.Add(registry.TestSpec{
		Name:             "failure-injection-noop-smoke-test",
		Owner:            registry.OwnerTestEng,
		Cluster:          r.MakeClusterSpec(4, spec.WorkloadNode(), spec.CPU(2), spec.WorkloadNodeCPU(2), spec.ReuseNone()),
		CompatibleClouds: registry.OnlyGCE,
		Suites:           registry.ManualOnly,
		Run: func(ctx context.Context, t test.Test, c cluster.Cluster) {
			runFailureSmokeTest(ctx, t, c, true /* noopFailer */)
		},
	})
}
