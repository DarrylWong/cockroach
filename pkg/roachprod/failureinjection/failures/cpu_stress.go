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
	"github.com/cockroachdb/errors"
	"strings"
)

// PageFault creates page faults on the node. Note stress-ng causes long-lasting thrashing
// on the cluster, so use with caution and give appropriate time for it to restore.
type PageFault struct {
	c      *install.SyncedCluster
	cancel context.CancelFunc
}

const PageFaultName = "page-fault"

func MakePageFaulter(clusterName string, l *logger.Logger, secure bool) (FailureMode, error) {
	c, err := roachprod.GetClusterFromCache(l, clusterName, install.SecureOption(secure))
	if err != nil {
		return nil, err
	}

	return &PageFault{c: c}, nil
}

func registerPageFault(r *FailureRegistry) {
	r.add(PageFaultName, &PageFaultArgs{}, MakePageFaulter)
}

func (p *PageFault) Description() string {
	return PageFaultName
}

func (p *PageFault) run(ctx context.Context, l *logger.Logger, nodes install.Nodes, args ...string) error {
	cmd := strings.Join(args, " ")
	l.Printf("page fault: %s", cmd)
	return p.c.Run(ctx, l, l.Stdout, l.Stderr, install.WithNodes(nodes), fmt.Sprintf("page fault: %s", cmd), cmd)
}

type PageFaultArgs struct {
	Workers int
	Nodes   install.Nodes
}

func (p *PageFault) Setup(ctx context.Context, l *logger.Logger, args FailureArgs) error {
	return install.Install(ctx, l, p.c, []string{"stress-ng"})
}

func (p *PageFault) Inject(ctx context.Context, l *logger.Logger, args FailureArgs) error {
	failureArgs := args.(PageFaultArgs)
	pageFaultCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	nodes := p.c.Nodes
	if failureArgs.Nodes != nil {
		nodes = failureArgs.Nodes
	}
	go func() {
		if err := p.run(pageFaultCtx, l, nodes, "sudo", "stress-ng", "--userfaultfd", fmt.Sprint(failureArgs.Workers)); err != nil {
			if pageFaultCtx.Err() == nil {
				l.Printf("error running stress-ng: %s", err)
			}
		}
	}()
	return nil
}

func (p *PageFault) Restore(ctx context.Context, l *logger.Logger, args FailureArgs) error {
	failureArgs := args.(PageFaultArgs)
	nodes := p.c.Nodes
	if failureArgs.Nodes != nil {
		nodes = failureArgs.Nodes
	}

	if p.cancel == nil {
		return errors.New("no page fault failure currently injected")
	}
	p.cancel()
	// TODO: instead, see if it's possible to have the goroutine in inject sigint
	if err := p.run(ctx, l, nodes, "sudo", "killall", "stress-ng"); err != nil {
		return errors.Errorf("Unable to stop stress-ng")
	}

	return nil
}

func (p *PageFault) Cleanup(ctx context.Context, l *logger.Logger) error {
	// TODO: uninstall stress-ng
	return nil
}
