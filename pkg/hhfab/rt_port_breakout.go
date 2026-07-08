package hhfab

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"golang.org/x/sync/errgroup"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const NumTestPorts = 7

// Dont test the virtual switch
// test 1 from the back and 1 from the front
// confirm the breakout worked
// move on to the next port
// test multiple switches at once
func findEndingPorts(numPorts int, agent *agentapi.Agent) []string {
	// This function relies on only supporting switches with 64 or 32 breakout ports.

	currPort := 0
	i := 0
	portList := []string{}
	_, exists := agent.Spec.SwitchProfile.Ports["E1/32"]
	if exists {
		currPort = 32
	}
	// If there isn't a 64th port this will fall through
	_, exists = agent.Spec.SwitchProfile.Ports["E1/64"]
	if exists {
		currPort = 64
	}
	for i < numPorts {
		portList = append(portList, "E1/"+strconv.Itoa(currPort))
		currPort = currPort - 1
		i++
	}
	return portList
}

func portBreakoutVerify(agent *agentapi.Agent) error {
	// 1. Get a slice with the first 7 and last 7 breakout ports
	startingPorts := [NumTestPorts]string{}
	endingPorts := [NumTestPorts]string{}
	for i := 1; i < NumTestPorts+1; i++ {
		_, exists := agent.Spec.SwitchProfile.Ports["E1/"+strconv.Itoa(i)]
		if exists {
			startingPorts = append(startingPorts, "E1/"+strconv.Itoa(i))
		}
	}
	endingPorts := findEndingPorts(NumTestPorts, agent)

	// 2. save the existing status of the port, restore it on exit
	// LOGAN - this might be the whole switch, which is great. just copy it and restore it.
	statePreTests, preTestExists := agent.Status.State.Breakouts

	// 3. Return all ports to their default breakout state
	defaultBreakouts, err := agent.Spec.SwitchProfile.GetBreakoutDefaults(&agent.Spec.Switch)
	if err != nil {
		return fmt.Errorf("getting default breakouts for switch %s: %w", agent.Name, err)
	}
	for i := range defaultBreakouts {
		if err := setPortBreakout(ctx, testCtx.kube, agent.Name, unusedPort, defaultBreakouts[i], true); err != nil {
			return fmt.Errorf("setting breakout mode back to default for port %s on switch %s: %w", unusedPort, agent.Name, err)
		}
	}
	// 4. issue the command for two ports at a time, do all the breakouts, waiting for completion before moving onto the second
	for port := range startingPorts {
		// use port to find its breakout capabilities
		// loop on the breakout modes
		// set the breakout, verify, and move on to the next one.
	}
	// 5.
}

func breakoutSanityTest(ctx context.Context, testCtx *VPCPeeringTestCtx) (bool, []RevertFunc, error) {
	// get all agents in the fabric
	agents := &agentapi.AgentList{}
	if err := testCtx.kube.List(ctx, agents); err != nil {
		return false, nil, fmt.Errorf("listing agents: %w", err)
	}
	g := &errgroup.Group{}
	for _, agent := range agents.Items {
		g.Go(func() error {
			// first of all, disable RoCE if it is enabled, as breakout operations are forbidden while RoCE is enabled
			if err := setRoCE(ctx, testCtx.kube, agent.Name, false); err != nil {
				return fmt.Errorf("disabling RoCE on switch %s: %w", agent.Name, err)
			}
			err := portBreakoutVerify(agent)
			if err != nil {
				// TODO handle error
			}
			// get which ports are used on this switch
			conns := &wiringapi.ConnectionList{}
			if err := testCtx.kube.List(ctx, conns, kclient.MatchingLabels{
				wiringapi.ListLabelSwitch(agent.Name): wiringapi.ListLabelValue,
			}); err != nil {
				return fmt.Errorf("listing connections for switch %s: %w", agent.Name, err)
			}

			usedPorts := make(map[string]bool, len(conns.Items))
			for _, conn := range conns.Items {
				_, _, connPorts, _, err := conn.Spec.Endpoints()
				if err != nil {
					return fmt.Errorf("getting endpoints for connection %s: %w", conn.Name, err)
				}
				for _, connPort := range connPorts {
					if !strings.HasPrefix(connPort, agent.Name+"/") {
						continue
					}

					portName := strings.SplitN(connPort, "/", 2)[1]
					if strings.Count(portName, "/") == 2 {
						breakoutName := portName[:strings.LastIndex(portName, "/")]
						usedPorts[breakoutName] = true
					}
					usedPorts[portName] = true
				}
			}

			// get default breakout modes for each port
			defaultBreakouts, err := agent.Spec.SwitchProfile.GetBreakoutDefaults(&agent.Spec.Switch)
			if err != nil {
				return fmt.Errorf("getting default breakouts for switch %s: %w", agent.Name, err)
			}

			// now go over all the ports in the switch profile and find the first unused port
			swProfile := &wiringapi.SwitchProfile{}
			if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: "default", Name: agent.Spec.Switch.Profile}, swProfile); err != nil {
				return fmt.Errorf("getting switch profile %s for switch %s: %w", agent.Spec.Switch.Profile, agent.Name, err)
			}
			var unusedPort string
			var breakoutProfile *wiringapi.SwitchProfilePortProfileBreakout
			for portName, port := range swProfile.Spec.Ports {
				if _, ok := usedPorts[portName]; !ok {
					// this port is not used, but can it be used for breakout?
					if port.Profile == "" {
						continue
					}
					portProfile, ok := swProfile.Spec.PortProfiles[port.Profile]
					if !ok {
						return fmt.Errorf("port profile %s of port %s not found in switch profile %s for switch %s: %w", port.Profile, portName, swProfile.Name, agent.Name, err)
					}
					if portProfile.Breakout == nil {
						continue
					}
					// skip ports where portAutoNegs has sub-ports that won't be valid in any
					// 1-offset breakout mode (we only test with 1-offset modes, which only
					// generate sub-port /1); e.g. E1/1 configured as 2x400G with autoNeg on
					// E1/1/2 can't be switched to 1x800G without the webhook rejecting it
					hasConflict := false
					for autoNegPort := range agent.Spec.Switch.PortAutoNegs {
						if strings.HasPrefix(autoNegPort, portName+"/") && strings.TrimPrefix(autoNegPort, portName+"/") != "1" {
							hasConflict = true

							break
						}
					}
					if hasConflict {
						slog.Debug("Skipping port with portAutoNegs conflicting with 1-offset breakout modes", "port", portName, "switch", agent.Name)

						continue
					}
					unusedPort = portName
					breakoutProfile = portProfile.Breakout
					slog.Debug("Found unused port for breakout", "port", unusedPort, "switch", agent.Name, "switchProfile", swProfile.Name)

					break
				}
			}
			if unusedPort == "" || breakoutProfile == nil {
				slog.Warn("No unused ports found on switch", "switch", agent.Name)

				return nil
			}

			// pick a random non-default supported breakout mode
			targetMode := ""
			for mode, breakout := range breakoutProfile.Supported {
				// only use the breakout mode that has a single resulting port to avoid issues with max ports for the pipelines
				if mode != defaultBreakouts[unusedPort] && len(breakout.Offsets) == 1 {
					targetMode = mode

					break
				}
			}
			if targetMode == "" {
				slog.Warn("No non-default breakout modes found for port", "port", unusedPort, "switch", agent.Name)

				return nil
			}
			currState, exists := agent.Status.State.Breakouts[unusedPort]
			currMode := ""
			if exists {
				currMode = currState.Mode
			} else {
				currMode = defaultBreakouts[unusedPort]
			}
			// we had a bug where the breakout mode could only be set the first time, so we need to check if the current mode is different from the default
			if currMode == defaultBreakouts[unusedPort] {
				slog.Debug("Unused port is in default breakout mode, setting it to target mode", "port", unusedPort, "switch", agent.Name, "defaultMode", defaultBreakouts[unusedPort], "targetMode", targetMode)
				if err := setPortBreakout(ctx, testCtx.kube, agent.Name, unusedPort, targetMode, true); err != nil {
					// revert change anyway
					slog.Debug("Setting breakout mode failed, reverting to default", "port", unusedPort, "switch", agent.Name, "defaultMode", defaultBreakouts[unusedPort])
					_ = setPortBreakout(ctx, testCtx.kube, agent.Name, unusedPort, defaultBreakouts[unusedPort], false)

					return fmt.Errorf("setting breakout mode for port %s on switch %s: %w", unusedPort, agent.Name, err)
				}
				currMode = targetMode
			}
			// now set it to default mode again
			slog.Debug("Setting breakout mode back to default", "port", unusedPort, "switch", agent.Name, "defaultMode", defaultBreakouts[unusedPort], "currentMode", currMode)
			if err := setPortBreakout(ctx, testCtx.kube, agent.Name, unusedPort, defaultBreakouts[unusedPort], true); err != nil {
				return fmt.Errorf("setting breakout mode back to default for port %s on switch %s: %w", unusedPort, agent.Name, err)
			}
			slog.Debug("Breakout test passed for switch", "switch", agent.Name, "port", unusedPort, "mode", targetMode)

			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return false, nil, fmt.Errorf("running breakout test for switches: %w", err)
	}

	return false, nil, nil
}
