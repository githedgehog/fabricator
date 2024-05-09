// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package testing

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pkg/errors"
	agentapi "go.githedgehog.com/fabric/api/agent/v1alpha2"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1alpha2"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1alpha2"
	"go.githedgehog.com/fabric/pkg/util/apiutil"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func WaitForSwitchesReady(ctx context.Context, kube client.WithWatch, expectedSwitches []string, timeout time.Duration) error {
	start := time.Now()

	ready := map[string]bool{}
	for _, switchName := range expectedSwitches {
		ready[switchName] = false
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	interval := min(10*time.Second, timeout/10)

	const retries = 10 // ~ timeout
	errs := 0

	attempt := 0

	for {
		if attempt > 0 {
			time.Sleep(interval)
		}
		attempt++

		agents := agentapi.AgentList{}
		if err := kube.List(ctx, &agents, client.InNamespace("default")); err != nil {
			errs++
			if errs <= retries {
				slog.Warn("Error listing agents", "retries", fmt.Sprintf("%d/%d", errs, retries), "err", err)

				continue
			}

			return errors.Wrapf(err, "error listing agents")
		}

		errs = 0

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

type netconf struct {
	cmd    string
	subnet string
}

func buildNetconf(ctx context.Context, kube client.Client, server string) ([]netconf, error) {
	attached, err := apiutil.GetAttachedSubnets(ctx, kube, server)
	if err != nil {
		return nil, errors.Wrapf(err, "error getting attached subnets")
	}

	netconfs := []netconf{}
	for vpcSubnet, attachment := range attached {
		vpcParts := strings.SplitN(vpcSubnet, "/", 2)
		if len(vpcParts) != 2 {
			return nil, errors.Errorf("invalid subnet format: %s", vpcSubnet)
		}

		vpcName, subnetName := vpcParts[0], vpcParts[1]

		vpc := vpcapi.VPC{}
		if err := kube.Get(ctx, client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      vpcName,
		}, &vpc); err != nil {
			return nil, errors.Wrapf(err, "failed to get VPC %s", vpcName)
		}

		if vpc.Spec.Subnets[subnetName] == nil {
			return nil, errors.Errorf("subnet %s not found in VPC %s", subnetName, vpcName)
		}

		conn := wiringapi.Connection{}
		if err := kube.Get(ctx, client.ObjectKey{
			Namespace: metav1.NamespaceDefault,
			Name:      attachment.Connection,
		}, &conn); err != nil {
			return nil, errors.Wrapf(err, "failed to get connection %s", attachment.Connection)
		}

		vlan := uint16(0)
		if !attachment.NativeVLAN {
			vlan = vpc.Spec.Subnets[subnetName].VLAN
		}

		netconfCmd := ""
		if conn.Spec.Unbundled != nil {
			netconfCmd = fmt.Sprintf("vlan %d %s", vlan, conn.Spec.Unbundled.Link.Server.LocalPortName())
		} else {
			netconfCmd = fmt.Sprintf("bond %d", vlan)

			if conn.Spec.Bundled != nil {
				for _, link := range conn.Spec.Bundled.Links {
					netconfCmd += " " + link.Server.LocalPortName()
				}
			}
			if conn.Spec.MCLAG != nil {
				for _, link := range conn.Spec.MCLAG.Links {
					netconfCmd += " " + link.Server.LocalPortName()
				}
			}
			if conn.Spec.ESLAG != nil {
				for _, link := range conn.Spec.ESLAG.Links {
					netconfCmd += " " + link.Server.LocalPortName()
				}
			}
		}

		netconfs = append(netconfs, netconf{
			cmd:    netconfCmd,
			subnet: vpc.Spec.Subnets[subnetName].Subnet,
		})
	}

	return netconfs, nil
}

type Iperf3Report struct {
	Intervals []Iperf3ReportInterval `json:"intervals"`
	End       Iperf3ReportEnd        `json:"end"`
}

type Iperf3ReportInterval struct {
	Sum Iperf3ReportSum `json:"sum"`
}

type Iperf3ReportEnd struct {
	SumSent     Iperf3ReportSum `json:"sum_sent"`
	SumReceived Iperf3ReportSum `json:"sum_received"`
}

type Iperf3ReportSum struct {
	Bytes         int64   `json:"bytes"`
	BitsPerSecond float64 `json:"bits_per_second"`
}

func ParseIperf3Report(data string) (*Iperf3Report, error) {
	report := &Iperf3Report{}
	if err := json.Unmarshal([]byte(data), report); err != nil {
		return nil, errors.Wrapf(err, "error unmarshaling iperf3 report")
	}

	return report, nil
}

type Duration struct {
	time.Duration
}

func (duration *Duration) UnmarshalJSON(b []byte) error {
	var raw interface{}
	err := json.Unmarshal(b, &raw)
	if err != nil {
		return errors.Wrapf(err, "error unmarshaling duration")
	}

	switch value := raw.(type) {
	case float64:
		duration.Duration = time.Duration(value)
	case string:
		duration.Duration, err = time.ParseDuration(value)
		if err != nil {
			return errors.Wrapf(err, "error parsing duration")
		}
	default:
		return errors.Errorf("invalid duration type: %T", value)
	}

	return nil
}
