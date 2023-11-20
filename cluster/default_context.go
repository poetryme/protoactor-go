// Copyright (C) 2017 - 2022 Asynkron.se <http://www.asynkron.se>

package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/asynkron/protoactor-go/actor"
	"github.com/asynkron/protoactor-go/remote"
)

// Defines a type to provide DefaultContext configurations / implementations.
type ContextProducer func(*Cluster) Context

// Defines a default cluster context hashBytes structure.
type DefaultContext struct {
	cluster *Cluster
}

var _ Context = (*DefaultContext)(nil)

// Creates a new DefaultContext value and returns
// a pointer to its memory address as a Context.
func newDefaultClusterContext(cluster *Cluster) Context {
	clusterContext := DefaultContext{
		cluster: cluster,
	}

	return &clusterContext
}

func (dcc *DefaultContext) Request(identity, kind string, message interface{}, opts ...GrainCallOption) (interface{}, error) {
	var err error

	var resp interface{}

	var counter int
	callConfig := DefaultGrainCallConfig(dcc.cluster)
	for _, o := range opts {
		o(callConfig)
	}

	_context := callConfig.Context

	// get the configuration from the composed Cluster value
	cfg := dcc.cluster.Config.ToClusterContextConfig(dcc.cluster.Logger())

	start := time.Now()

	dcc.cluster.Logger().Debug(fmt.Sprintf("Requesting %s:%s Message %#v", identity, kind, message))

	// crate a new Timeout Context
	ttl := callConfig.Timeout

	ctx, cancel := context.WithTimeout(context.Background(), ttl)
	defer cancel()

selectloop:
	for {
		select {
		case <-ctx.Done():
			// TODO: handler throttling and messaging here
			err = fmt.Errorf("request failed: %w", ctx.Err())

			break selectloop
		default:
			pid := dcc.getCachedPid(identity, kind)
			if pid == nil {
				dcc.cluster.Logger().Debug(fmt.Sprintf("Requesting %s:%s did not get PID from IdentityLookup", identity, kind))
				counter = callConfig.RetryAction(counter)
				continue
			}

			resp, err = _context.RequestFuture(pid, message, ttl).Result()
			if err != nil {
				dcc.cluster.Logger().Error("cluster.RequestFuture failed", slog.Any("error", err), slog.Any("pid", pid))
				switch err {
				case actor.ErrTimeout, remote.ErrTimeout, actor.ErrDeadLetter, remote.ErrDeadLetter:
					counter = callConfig.RetryAction(counter)
					dcc.cluster.PidCache.Remove(identity, kind)
					err = nil // reset our error variable as we can succeed still

					continue
				default:
					break selectloop
				}
			}

			// TODO: add metrics to increment retries
		}
	}

	totalTime := time.Since(start)
	// TODO: add metrics ot set histogram for total request time

	if contextError := ctx.Err(); contextError != nil && cfg.requestLogThrottle() == actor.Open {
		// context timeout exceeded, report and return
		dcc.cluster.Logger().Warn("Request retried but failed", slog.String("identity", identity), slog.String("kind", kind), slog.Duration("duration", totalTime))
	}

	return resp, err
}

// gets the cached PID for the given identity
// it can return nil if none is found.
func (dcc *DefaultContext) getCachedPid(identity, kind string) *actor.PID {
	pid, _ := dcc.cluster.PidCache.Get(identity, kind)

	return pid
}
