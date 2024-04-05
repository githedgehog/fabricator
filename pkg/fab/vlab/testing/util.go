package testing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/pkg/errors"
	agentapi "go.githedgehog.com/fabric/api/agent/v1alpha2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func WaitForSwitchesReady(ctx context.Context, kube client.WithWatch, expectedSwitches []string) error {
	start := time.Now()

	ready := map[string]bool{}
	for _, switchName := range expectedSwitches {
		ready[switchName] = false
	}

	errs := 0
	retries := 8

	for {
		time.Sleep(15 * time.Second) // TODO make configurable

		agents := agentapi.AgentList{}
		if err := kube.List(ctx, &agents, client.InNamespace("default")); err != nil {
			errs++
			if errs <= retries {
				slog.Warn("Error listing agents", "retries", fmt.Sprintf("%d/%d", errs, retries), "err", err)

				continue
			}

			return errors.Wrapf(err, "error listing agents")
		}

		for _, agent := range agents.Items {
			if agent.Generation == agent.Status.LastAppliedGen && time.Since(agent.Status.LastHeartbeat.Time) < 30*time.Second {
				ready[agent.Name] = true

				continue
			}
		}

		allReady := true
		for _, swReady := range ready {
			if !swReady {
				allReady = false
			}
		}

		readyList := []string{}
		notReadyList := []string{}
		for sw, swReady := range ready {
			if swReady {
				readyList = append(readyList, sw)
			} else {
				notReadyList = append(notReadyList, sw)
			}
		}

		slog.Info("Switches ready status", "ready", readyList, "notReady", notReadyList)

		if allReady {
			slog.Info("All switches are ready", "took", time.Since(start))

			return nil
		}
	}
}
