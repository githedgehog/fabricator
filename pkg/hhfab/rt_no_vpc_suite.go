// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	gwapi "go.githedgehog.com/fabric/api/gateway/v1alpha1"
	vpcapi "go.githedgehog.com/fabric/api/vpc/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	"go.githedgehog.com/fabric/pkg/hhfctl"
	fabricatorapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	"go.githedgehog.com/fabricator/pkg/util/sshutil"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const promStatusSuccess = "success"

// prometheusQueryResponse represents the response from a Prometheus instant query.
type prometheusQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  []interface{}     `json:"value"`
		} `json:"result"`
	} `json:"data"`
}

func makeNoVpcsSuite() *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "No VPCs Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name: "Breakout ports",
			F:    breakoutTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
			},
		},
		{
			Name: "Loki Observability",
			F:    lokiObservabilityTest,
			SkipFlags: SkipFlags{
				NoLoki: true,
			},
		},
		{
			Name: "Prometheus Observability",
			F:    prometheusObservabilityTest,
			SkipFlags: SkipFlags{
				NoProm: true,
			},
		},
		{
			Name: "HostBGP Multihoming",
			F:    hostBGPTest,
			SkipFlags: SkipFlags{
				SubInterfaces: true,
				NoServers:     true,
			},
		},
	}
	suite.Tests = len(suite.TestCases)

	return suite
}

// test breakout ports. for each switch in the fabric:
// 1. take the first unused breakout port (to avoiid conflicts)
// 2. change breakout to some non default mode
// 3. wait for all switches to be ready for 1 minute
// 4. check that all agents report the breakout to be completed and that the port is in the expected mode
func breakoutTest(ctx context.Context, testCtx *VPCPeeringTestCtx) (bool, []RevertFunc, error) {
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

// Structure to hold observability endpoint details
type ObsEndpoint struct {
	URL      string
	Username string
	Password string
}

// Transform observability endpoints from push to query URLs
func getObservabilityQueryURLs(ctx context.Context, kube kclient.Client) (ObsEndpoint, ObsEndpoint, string, error) {
	var loki, prometheus ObsEndpoint
	var env string

	fabricator := &fabricatorapi.Fabricator{}
	if err := kube.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: kmetav1.NamespaceDefault}, fabricator); err != nil {
		return ObsEndpoint{}, ObsEndpoint{}, "", fmt.Errorf("getting Fabricator object: %w", err)
	}

	// Check if Loki targets exist and iterate through all of them
	if fabricator.Spec.Config.Observability.Targets.Loki != nil {
		for targetName, lokiTarget := range fabricator.Spec.Config.Observability.Targets.Loki {
			if lokiTarget.URL != "" {
				slog.Debug("Found Loki target", "name", targetName, "url", lokiTarget.URL)

				// Transform push URL to query URL based on URL pattern
				lokiPushURL := lokiTarget.URL

				// Handle different URL patterns
				if strings.Contains(lokiPushURL, "grafana.net") {
					// Grafana Cloud URL
					loki.URL = strings.Replace(lokiPushURL, "/api/v1/push", "/api/v1", 1)
				} else {
					// Generic URL
					loki.URL = strings.TrimSuffix(lokiPushURL, "/push")
					if !strings.HasSuffix(loki.URL, "/api/v1") {
						loki.URL += "/api/v1"
					}
				}

				// Extract auth credentials if present
				if lokiTarget.BasicAuth != nil {
					loki.Username = lokiTarget.BasicAuth.Username
					loki.Password = lokiTarget.BasicAuth.Password
				}

				// Extract environment label if present
				if lokiTarget.Labels != nil {
					if envLabel, ok := lokiTarget.Labels["env"]; ok {
						env = envLabel
					}
				}

				// Take the first valid URL we find
				break
			}
		}
	}

	// Check if Prometheus targets exist and iterate through all of them
	if fabricator.Spec.Config.Observability.Targets.Prometheus != nil {
		for targetName, promTarget := range fabricator.Spec.Config.Observability.Targets.Prometheus {
			if promTarget.URL != "" {
				slog.Debug("Found Prometheus target", "name", targetName, "url", promTarget.URL)

				// Transform push URL to query URL based on URL pattern
				promPushURL := promTarget.URL

				// Handle different URL patterns
				if strings.Contains(promPushURL, "grafana.net") {
					// Grafana Cloud URL
					prometheus.URL = strings.Replace(promPushURL, "/api/prom/push", "/api/prom/api/v1", 1)
				} else {
					// Generic URL - for standalone Prometheus
					if strings.Contains(promPushURL, "/api/v1/write") {
						prometheus.URL = strings.Replace(promPushURL, "/write", "", 1)
					} else {
						prometheus.URL = strings.TrimSuffix(promPushURL, "/push")
						if !strings.HasSuffix(prometheus.URL, "/api/v1") {
							prometheus.URL += "/api/v1"
						}
					}
				}

				// Extract auth credentials if present
				if promTarget.BasicAuth != nil {
					prometheus.Username = promTarget.BasicAuth.Username
					prometheus.Password = promTarget.BasicAuth.Password
				}

				// Take the first valid URL we find
				break
			}
		}
	}

	return loki, prometheus, env, nil
}

const (
	LabelAppInstance   = "app.kubernetes.io/instance"
	AlloyCtrlComponent = "alloy-ctrl"
)

func lokiObservabilityTest(ctx context.Context, testCtx *VPCPeeringTestCtx) (bool, []RevertFunc, error) {
	lokiEndpoint, _, env, err := getObservabilityQueryURLs(ctx, testCtx.kube)
	if err != nil {
		return true, nil, fmt.Errorf("error getting observability endpoints: %w", err)
	}

	if lokiEndpoint.URL == "" {
		slog.Info("No Loki target found, Loki test will be skipped")

		return true, nil, nil
	}

	// Get credentials from endpoint object
	hasAuth := lokiEndpoint.Username != "" && lokiEndpoint.Password != ""

	slog.Debug("Using Loki endpoint",
		"url", lokiEndpoint.URL,
		"auth_from_endpoint", hasAuth,
		"env", env)

	// List switches to check for logs
	switches := &wiringapi.SwitchList{}
	if err := testCtx.kube.List(ctx, switches); err != nil {
		return false, nil, fmt.Errorf("listing switches: %w", err)
	}

	// Get gateway devices from K8s API (with simplified error handling)
	var gateways []string
	gatewayList := &gwapi.GatewayList{}
	if err := testCtx.kube.List(ctx, gatewayList); err == nil {
		for _, gw := range gatewayList.Items {
			gateways = append(gateways, gw.Name)
		}
	}

	// Get alloy controller pods (simplified logic)
	alloyPods := &corev1.PodList{}
	if err := testCtx.kube.List(ctx, alloyPods,
		kclient.InNamespace(comp.FabNamespace),
		kclient.MatchingLabels{LabelAppInstance: AlloyCtrlComponent}); err != nil {
		// Try alternate method if first approach fails
		allPods := &corev1.PodList{}
		if err := testCtx.kube.List(ctx, allPods, kclient.InNamespace(comp.FabNamespace)); err == nil {
			for _, pod := range allPods.Items {
				if strings.HasPrefix(pod.Name, AlloyCtrlComponent+"-") {
					alloyPods.Items = append(alloyPods.Items, pod)
				}
			}
		}
	}

	// Build list of expected hostnames
	expectedHostnames := make([]string, 0, len(switches.Items)+len(alloyPods.Items)+len(gateways))
	for _, sw := range switches.Items {
		expectedHostnames = append(expectedHostnames, sw.Name)
	}
	expectedHostnames = append(expectedHostnames, gateways...)
	for _, pod := range alloyPods.Items {
		expectedHostnames = append(expectedHostnames, pod.Name)
	}

	slog.Info("Checking logs for devices", "expected", expectedHostnames)

	// Set up HTTP client and perform connectivity check
	client := &http.Client{
		Timeout: 15 * time.Second,
	}
	labelsURL := fmt.Sprintf("%s/labels", lokiEndpoint.URL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, labelsURL, nil)
	if err != nil {
		return false, nil, fmt.Errorf("failed to create loki API request: %w", err)
	}

	// Add auth if needed
	if hasAuth {
		req.SetBasicAuth(lokiEndpoint.Username, lokiEndpoint.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, nil, fmt.Errorf("loki connectivity check failed: %w", err)
	}

	body, bodyErr := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Check connectivity and authentication
	if resp.StatusCode == http.StatusUnauthorized {
		if bodyErr != nil {
			return false, nil, fmt.Errorf("loki authentication failed (could not read body: %w)", bodyErr) //nolint:goerr113
		}

		return false, nil, fmt.Errorf("loki authentication failed: %s", string(body)) //nolint:goerr113
	}
	if resp.StatusCode != http.StatusOK {
		if bodyErr != nil {
			return false, nil, fmt.Errorf("loki API connectivity check failed: status %d (could not read body: %w)", resp.StatusCode, bodyErr) //nolint:goerr113
		}

		return false, nil, fmt.Errorf("loki API connectivity check failed: status %d", resp.StatusCode) //nolint:goerr113
	}

	if bodyErr != nil {
		return false, nil, fmt.Errorf("failed to read response body from loki connectivity check: %w", bodyErr)
	}

	// Track results
	missingLogs := make([]string, 0, len(expectedHostnames))
	foundLogs := make([]string, 0, len(expectedHostnames))
	var errorSample string

	// Define maximum age for logs to be considered fresh
	const maxLogAgeSecs = 300 // 5 minutes

	// Check each device for logs
	for _, hostname := range expectedHostnames {
		// Build time range for query
		endTime := time.Now().UnixNano()
		startTime := time.Now().Add(-5 * time.Minute).UnixNano()

		// Build query with environment label if available
		var query string
		if env != "" {
			query = fmt.Sprintf(`{hostname="%s", env="%s"}`, hostname, env)
		} else {
			query = fmt.Sprintf(`{hostname="%s"}`, hostname)
		}

		queryURL := fmt.Sprintf("%s/query_range?query=%s&start=%d&end=%d&limit=20",
			lokiEndpoint.URL,
			url.QueryEscape(query),
			startTime,
			endTime)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
		if err != nil {
			if errorSample == "" {
				errorSample = fmt.Sprintf("Failed to create request: %v", err)
			}
			missingLogs = append(missingLogs, hostname)

			continue
		}

		// Use credentials from endpoint object
		if hasAuth {
			req.SetBasicAuth(lokiEndpoint.Username, lokiEndpoint.Password)
		}

		resp, err := client.Do(req)
		if err != nil {
			if errorSample == "" {
				errorSample = fmt.Sprintf("HTTP request failed: %v", err)
			}
			missingLogs = append(missingLogs, hostname)

			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			if errorSample == "" {
				errorSample = fmt.Sprintf("Failed to read response: %v", err)
			}
			missingLogs = append(missingLogs, hostname)

			continue
		}

		var lokiResp struct {
			Status string `json:"status"`
			Data   struct {
				ResultType string `json:"resultType"`
				Result     []struct {
					Stream map[string]string `json:"stream"`
					Values [][]string        `json:"values"`
				} `json:"result"`
			} `json:"data"`
		}

		if err := json.Unmarshal(body, &lokiResp); err != nil {
			if errorSample == "" {
				errorSample = fmt.Sprintf("Failed to parse response: %v", err)
			}
			missingLogs = append(missingLogs, hostname)

			continue
		}

		// Count total entries and get a sample if available
		entryCount := 0
		var sample string
		for _, result := range lokiResp.Data.Result {
			entryCount += len(result.Values)
			if sample == "" && len(result.Values) > 0 && len(result.Values[0]) > 1 {
				sample = result.Values[0][1]
			}
		}

		// Log only count and one sample entry
		slog.Debug("Loki query details", "hostname", hostname, "status", resp.Status, "query", query, "entries", entryCount, "sample", sample)

		if resp.StatusCode != http.StatusOK {
			if errorSample == "" {
				errorSample = fmt.Sprintf("HTTP status %d for query: %s", resp.StatusCode, query)
			}
			missingLogs = append(missingLogs, hostname)

			continue
		}

		if lokiResp.Status != promStatusSuccess {
			if errorSample == "" {
				errorSample = fmt.Sprintf("Query failed with status: %s", lokiResp.Status)
			}
			missingLogs = append(missingLogs, hostname)

			continue
		}

		// Count log entries and check freshness
		logCount := 0
		freshLogsFound := false
		for _, result := range lokiResp.Data.Result {
			for _, value := range result.Values {
				logCount++

				// Check log freshness if we have timestamp
				if len(value) > 0 {
					if ts, err := strconv.ParseInt(value[0], 10, 64); err == nil {
						logTime := time.Unix(0, ts)
						if time.Since(logTime) <= maxLogAgeSecs*time.Second {
							freshLogsFound = true
						}
					}
				}
			}
		}

		if logCount == 0 {
			missingLogs = append(missingLogs, hostname)

			continue
		}

		if !freshLogsFound {
			slog.Debug("Only stale logs found for device (older than 5 minutes)", "hostname", hostname)
			missingLogs = append(missingLogs, hostname)

			continue
		}

		// Logs found!
		foundLogs = append(foundLogs, hostname)
		slog.Info("Found logs for device", "hostname", hostname, "count", logCount, "env", env)
	}

	// Only fail if ALL expected devices are missing logs
	if len(foundLogs) == 0 && len(expectedHostnames) > 0 {
		// Show a sample error to help debug without overwhelming with all errors
		if errorSample != "" {
			return false, nil, fmt.Errorf("no logs found for any devices: %s", errorSample) //nolint:goerr113
		}

		return false, nil, fmt.Errorf("no logs found for any devices; checked %d devices", len(expectedHostnames)) //nolint:goerr113
	}

	if len(missingLogs) > 0 {
		// Some logs are missing, but not all - just warn rather than fail
		slog.Warn("Some devices missing logs in Loki", "missing_count", len(missingLogs), "total_count", len(expectedHostnames))
		slog.Debug("Devices missing logs", "missing", missingLogs)
	}

	return false, nil, nil
}

func prometheusObservabilityTest(ctx context.Context, testCtx *VPCPeeringTestCtx) (bool, []RevertFunc, error) {
	_, prometheusEndpoint, env, err := getObservabilityQueryURLs(ctx, testCtx.kube)
	if err != nil {
		return true, nil, fmt.Errorf("error getting observability endpoints: %w", err)
	}

	if prometheusEndpoint.URL == "" {
		slog.Info("No Prometheus target found, Prometheus test will be skipped")

		return true, nil, nil
	}

	// Get credentials from endpoint object
	hasAuth := prometheusEndpoint.Username != "" && prometheusEndpoint.Password != ""

	slog.Debug("Using Prometheus endpoint",
		"url", prometheusEndpoint.URL,
		"auth_from_endpoint", hasAuth,
		"env", env)

	// List switches to check for metrics
	switches := &wiringapi.SwitchList{}
	if err := testCtx.kube.List(ctx, switches); err != nil {
		return false, nil, fmt.Errorf("listing switches: %w", err)
	}

	// Build list of switch names to check
	expectedSwitches := make([]string, 0, len(switches.Items))
	for _, sw := range switches.Items {
		expectedSwitches = append(expectedSwitches, sw.Name)
	}

	slog.Info("Checking metrics for switches", "expected", expectedSwitches)

	client := &http.Client{Timeout: 15 * time.Second}

	// First verify we can access Prometheus API with a simple query
	queryURL := fmt.Sprintf("%s/query?query=%s", prometheusEndpoint.URL, "up")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
	if err != nil {
		return false, nil, fmt.Errorf("failed to create prometheus API request: %w", err)
	}

	// Use credentials from endpoint object
	if hasAuth {
		req.SetBasicAuth(prometheusEndpoint.Username, prometheusEndpoint.Password)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, nil, fmt.Errorf("prometheus connectivity check failed: %w", err)
	}

	body, bodyErr := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Check connectivity and authentication
	if resp.StatusCode == http.StatusUnauthorized {
		if bodyErr != nil {
			return false, nil, fmt.Errorf("prometheus authentication failed (could not read body: %w)", bodyErr) //nolint:goerr113
		}

		return false, nil, fmt.Errorf("prometheus authentication failed: %s", string(body)) //nolint:goerr113
	}
	if resp.StatusCode != http.StatusOK {
		if bodyErr != nil {
			return false, nil, fmt.Errorf("prometheus API connectivity check failed: status %d (could not read body: %w)", resp.StatusCode, bodyErr) //nolint:goerr113
		}

		return false, nil, fmt.Errorf("prometheus API connectivity check failed: status %d", resp.StatusCode) //nolint:goerr113
	}

	if bodyErr != nil {
		return false, nil, fmt.Errorf("failed to read response body from prometheus connectivity check: %w", bodyErr)
	}

	// Define maximum age for metrics to be considered fresh
	const maxMetricAgeSecs = 300 // 5 minutes

	// Query for fabric_agent_agent_generation across all switches
	metricName := "fabric_agent_agent_generation"
	var queryExpr string
	if env != "" {
		queryExpr = fmt.Sprintf("%s{env=\"%s\"}", metricName, env)
	} else {
		queryExpr = metricName
	}

	queryURL = fmt.Sprintf("%s/query?query=%s", prometheusEndpoint.URL, url.QueryEscape(queryExpr))

	req, err = http.NewRequestWithContext(ctx, http.MethodGet, queryURL, nil)
	if err != nil {
		return false, nil, fmt.Errorf("failed to create metric query: %w", err)
	}

	// Add authentication
	if hasAuth {
		req.SetBasicAuth(prometheusEndpoint.Username, prometheusEndpoint.Password)
	}

	resp, err = client.Do(req)
	if err != nil {
		return false, nil, fmt.Errorf("failed to execute metric query: %w", err)
	}

	body, bodyErr = io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if bodyErr != nil {
			return false, nil, fmt.Errorf("metric query failed: status %d (could not read body: %w)", resp.StatusCode, bodyErr) //nolint:goerr113
		}

		return false, nil, fmt.Errorf("metric query failed: status %d", resp.StatusCode) //nolint:goerr113
	}

	if bodyErr != nil {
		return false, nil, fmt.Errorf("failed to read metric query response: %w", bodyErr)
	}

	var promResp prometheusQueryResponse
	if err := json.Unmarshal(body, &promResp); err != nil {
		return false, nil, fmt.Errorf("failed to parse prometheus response: %w", err)
	}

	// Log query details with status and count
	slog.Debug("Prometheus query details", "query", queryExpr, "status", resp.Status, "count", len(promResp.Data.Result))

	if promResp.Status != promStatusSuccess {
		return false, nil, fmt.Errorf("metric query returned non-success status: %s", promResp.Status) //nolint:goerr113
	}

	// Check if we got any results
	if len(promResp.Data.Result) == 0 {
		return false, nil, fmt.Errorf("no '%s' metrics found for any switches with env=%s", metricName, env) //nolint:goerr113
	}

	// Process results
	foundHostnames := make([]string, 0, len(promResp.Data.Result))

	// Check freshness and collect found metrics
	for _, result := range promResp.Data.Result {
		hostname, ok := result.Metric["hostname"]
		if !ok {
			continue
		}

		// Extract timestamp
		var timestamp time.Time
		if len(result.Value) > 0 {
			if ts, ok := result.Value[0].(float64); ok {
				timestamp = time.Unix(int64(ts), 0)
			}
		}

		// Get value as string
		var valueStr string
		if len(result.Value) > 1 {
			valueStr = fmt.Sprintf("%v", result.Value[1])
		}

		// Check metric freshness
		isFresh := true
		if timestamp.Unix() > 0 && time.Since(timestamp) > maxMetricAgeSecs*time.Second {
			slog.Debug("Stale metric found for switch (older than 5 minutes)", "switch", hostname)
			isFresh = false
		}

		if !isFresh {
			continue
		}

		foundHostnames = append(foundHostnames, hostname)

		// Log each device with its value and timestamp
		slog.Debug("Prometheus metric", "hostname", hostname, "value", valueStr, "timestamp", timestamp.Format(time.RFC3339))

		slog.Info("Found metric", "metric", metricName, "switch", hostname, "value", valueStr, "env", env)
	}

	if !slices.ContainsFunc(expectedSwitches, func(sw string) bool {
		return slices.Contains(foundHostnames, sw)
	}) {
		slog.Warn("Note: Switch names in metrics don't match Kubernetes names",
			"k8s_names", strings.Join(expectedSwitches, ", "),
			"metric_names", strings.Join(foundHostnames, ", "))
	}

	// As long as we found metrics, consider the test successful
	if len(foundHostnames) == 0 {
		return false, nil, fmt.Errorf("no fresh '%s' metrics found for any switches", metricName) //nolint:goerr113
	}

	slog.Info("Verified Prometheus metrics delivery", "metric", metricName, "metrics_count", len(foundHostnames), "switches_checked", len(switches.Items))

	// Check gateway metrics if gateways exist
	if err := testCtx.checkGatewayMetrics(ctx, prometheusEndpoint, env, hasAuth, client); err != nil {
		return false, nil, err
	}

	return false, nil, nil
}

// gwMetricDef defines a gateway metric type to check.
type gwMetricDef struct {
	name   string // human-readable name
	metric string // PromQL metric name
}

// checkGatewayMetrics verifies that all gateways have the expected metrics in Prometheus.
func (testCtx *VPCPeeringTestCtx) checkGatewayMetrics(ctx context.Context, prometheusEndpoint ObsEndpoint, env string, hasAuth bool, client *http.Client) error {
	const maxMetricAgeSecs = 300 // 5 minutes

	// Check if gateway is enabled before trying to list gateways
	fabricator := &fabricatorapi.Fabricator{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: comp.FabNamespace, Name: kmetav1.NamespaceDefault}, fabricator); err != nil {
		return fmt.Errorf("getting Fabricator object: %w", err)
	}

	if !fabricator.Spec.Config.Gateway.Enable {
		return nil
	}

	gatewayList := &gwapi.GatewayList{}
	if err := testCtx.kube.List(ctx, gatewayList); err != nil {
		return fmt.Errorf("listing gateways: %w", err)
	}

	if len(gatewayList.Items) == 0 {
		return nil
	}

	expectedGateways := make([]string, 0, len(gatewayList.Items))
	for _, gw := range gatewayList.Items {
		expectedGateways = append(expectedGateways, gw.Name)
	}

	gwObs := fabricator.Spec.Config.Gateway.Observability

	// Build list of gateway metrics to check based on what's enabled
	var gatewayMetrics []gwMetricDef
	if gwObs != nil {
		if gwObs.Dataplane.Metrics {
			gatewayMetrics = append(gatewayMetrics, gwMetricDef{name: "Dataplane", metric: "vpc_packet_count"})
		}
		if gwObs.FRR.Metrics {
			gatewayMetrics = append(gatewayMetrics, gwMetricDef{name: "FRR", metric: "frr_bgp_peer_state"})
		}
		if gwObs.Unix.Metrics {
			gatewayMetrics = append(gatewayMetrics, gwMetricDef{name: "Unix", metric: "node_load1"})
		}
	}

	if len(gatewayMetrics) == 0 {
		slog.Info("No gateway metrics enabled in Fabricator config, skipping gateway metrics check")

		return nil
	}

	slog.Info("Checking gateway metrics",
		"expected_gateways", expectedGateways,
		"gateway_count", len(expectedGateways),
		"enabled_metrics", len(gatewayMetrics))

	// Track which gateways have metrics for each type
	// gateway -> metric_type -> found
	gatewayMetricsFound := make(map[string]map[string]bool)
	for _, gw := range expectedGateways {
		gatewayMetricsFound[gw] = make(map[string]bool)
	}

	// Check each metric type
	for _, gm := range gatewayMetrics {
		var gwQueryExpr string
		if env != "" {
			gwQueryExpr = fmt.Sprintf("%s{env=\"%s\"}", gm.metric, env)
		} else {
			gwQueryExpr = gm.metric
		}

		gwQueryURL := fmt.Sprintf("%s/query?query=%s", prometheusEndpoint.URL, url.QueryEscape(gwQueryExpr))

		gwReq, err := http.NewRequestWithContext(ctx, http.MethodGet, gwQueryURL, nil)
		if err != nil {
			return fmt.Errorf("failed to create %s metric query: %w", gm.name, err)
		}

		if hasAuth {
			gwReq.SetBasicAuth(prometheusEndpoint.Username, prometheusEndpoint.Password)
		}

		gwResp, err := client.Do(gwReq)
		if err != nil {
			return fmt.Errorf("failed to execute %s metric query: %w", gm.name, err)
		}

		gwBody, gwBodyErr := io.ReadAll(gwResp.Body)
		gwResp.Body.Close()

		if gwResp.StatusCode != http.StatusOK {
			if gwBodyErr != nil {
				return fmt.Errorf("%s metric query failed: status %d (could not read body: %w)", gm.name, gwResp.StatusCode, gwBodyErr) //nolint:goerr113
			}

			return fmt.Errorf("%s metric query failed: status %d, body: %s", gm.name, gwResp.StatusCode, string(gwBody)) //nolint:goerr113
		}

		if gwBodyErr != nil {
			return fmt.Errorf("failed to read %s metric response: %w", gm.name, gwBodyErr)
		}

		var gwPromResp prometheusQueryResponse
		if err := json.Unmarshal(gwBody, &gwPromResp); err != nil {
			return fmt.Errorf("failed to parse %s metric response: %w", gm.name, err)
		}

		slog.Debug("Gateway metric query",
			"metric_type", gm.name,
			"query", gwQueryExpr,
			"status", gwPromResp.Status,
			"result_count", len(gwPromResp.Data.Result))

		if gwPromResp.Status != promStatusSuccess {
			return fmt.Errorf("%s metric query returned non-success status: %s", gm.name, gwPromResp.Status) //nolint:goerr113
		}

		// Process results and match to gateways
		for _, result := range gwPromResp.Data.Result {
			hostname, ok := result.Metric["hostname"]
			if !ok {
				continue
			}

			// Check if this hostname is one of our expected gateways
			if !slices.Contains(expectedGateways, hostname) {
				continue
			}

			// Check freshness
			var gwTimestamp time.Time
			if len(result.Value) > 0 {
				if ts, ok := result.Value[0].(float64); ok {
					gwTimestamp = time.Unix(int64(ts), 0)
				}
			}

			if gwTimestamp.Unix() > 0 && time.Since(gwTimestamp) > maxMetricAgeSecs*time.Second {
				slog.Debug("Stale gateway metric found",
					"gateway", hostname,
					"metric_type", gm.name,
					"age_seconds", time.Since(gwTimestamp).Seconds())

				continue
			}

			gatewayMetricsFound[hostname][gm.name] = true
			slog.Debug("Found gateway metric",
				"gateway", hostname,
				"metric_type", gm.name,
				"metric", gm.metric)
		}
	}

	// Evaluate results - ALL gateways must have ALL metric types
	var failedGateways []string
	for gw, metrics := range gatewayMetricsFound {
		var missingMetrics []string
		for _, gm := range gatewayMetrics {
			if !metrics[gm.name] {
				missingMetrics = append(missingMetrics, gm.name)
			}
		}

		if len(missingMetrics) > 0 {
			failedGateways = append(failedGateways, fmt.Sprintf("%s (missing: %s)", gw, strings.Join(missingMetrics, ", ")))
			slog.Error("Gateway missing metrics",
				"gateway", gw,
				"missing_metrics", missingMetrics)
		} else {
			slog.Info("Gateway has all required metrics", "gateway", gw)
		}
	}

	if len(failedGateways) > 0 {
		return fmt.Errorf("gateways missing required metrics: %s", strings.Join(failedGateways, "; ")) //nolint:goerr113
	}

	slog.Info("Verified gateway metrics delivery",
		"gateway_count", len(expectedGateways),
		"metric_types_checked", len(gatewayMetrics))

	return nil
}

// prerequisites: no existing VPCs, at least 1 unbundled multihomed server, at least 2 other servers.
// look for a server with unbundled connections to different switches; create two separate hostBGP VPCs,
// and attach them both to the server via all of its connections.
// Create two regular VPCs and attach them to the 2 other servers.
// Peer each hostBGP VPC with one of the regular VPCs and check connectivity between them
// by directly running ping and iperfs (test connectivity does not support multiple addresses).
// Bring down one link and ensure connectivity is still achieved via another link.
// Finally restore everything to the initial state by wiping all VPCs and their attachments.
func hostBGPTest(ctx context.Context, testCtx *VPCPeeringTestCtx) (bool, []RevertFunc, error) {
	reverts := []RevertFunc{}

	mhServers, err := findUnbundledMHServers(ctx, testCtx.kube, 1)
	if err != nil {
		return false, reverts, fmt.Errorf("looking for unbundled multihomed servers: %w", err)
	}
	if len(mhServers) != 1 {
		return true, reverts, fmt.Errorf("no available unbundled multihomed server") //nolint:err113
	}
	mhServer := mhServers[0]
	slog.Debug("Found unbundled multihomed server", "server", mhServer.Server.Name)

	// Collect all unbundled connections for the multihomed server (needed to build attachments)
	mhConns := &wiringapi.ConnectionList{}
	if err := testCtx.kube.List(ctx, mhConns, kclient.MatchingLabels{
		wiringapi.LabelConnectionType:                   wiringapi.ConnectionTypeUnbundled,
		wiringapi.ListLabelServer(mhServer.Server.Name): wiringapi.ListLabelValue,
	}); err != nil {
		return false, reverts, fmt.Errorf("listing unbundled connections for %s: %w", mhServer.Server.Name, err)
	}

	alloc, err := newVPCSubnetAllocator(ctx, testCtx.kube, testCtx.setupOpts.VLANNamespace, testCtx.setupOpts.IPv4Namespace)
	if err != nil {
		return false, reverts, fmt.Errorf("setting up VPC subnet allocator: %w", err)
	}
	defer alloc.stop()

	// Create two hostBGP VPCs, each attached to the server via all its unbundled connections
	const (
		hbgpVPCAName    = "hostbgp-01"
		hbgpVPCBName    = "hostbgp-02"
		regularVPCAName = "regular-01"
		regularVPCBName = "regular-02"
		regularSubnetID = "subnet-01"
	)

	allVPCs := []*vpcapi.VPC{}
	allAttaches := []*vpcapi.VPCAttachment{}
	type HostBGPVPC struct {
		VPC     *vpcapi.VPC
		VPCName string
		Subnet  netip.Prefix
		Vlan    uint16
		VIP     netip.Prefix
	}
	var hostBGP struct {
		A HostBGPVPC
		B HostBGPVPC
	}
	type RegularVPC struct {
		VPC          *vpcapi.VPC
		Server       *wiringapi.Server
		Connection   *wiringapi.Connection
		ServerPrefix netip.Prefix
	}
	var regularVPCA, regularVPCB RegularVPC

	for _, vpcName := range []string{hbgpVPCAName, hbgpVPCBName} {
		hbgp := HostBGPVPC{VPCName: vpcName}
		hbgp.Subnet, err = alloc.allocSubnet()
		if err != nil {
			return false, reverts, fmt.Errorf("allocating subnet for %s: %w", vpcName, err)
		}
		hbgp.Vlan, err = alloc.allocVLAN()
		if err != nil {
			return false, reverts, fmt.Errorf("allocating VLAN for %s: %w", vpcName, err)
		}
		hbgp.VPC = &vpcapi.VPC{
			ObjectMeta: kmetav1.ObjectMeta{Name: vpcName, Namespace: kmetav1.NamespaceDefault},
			Spec: vpcapi.VPCSpec{
				Mode:          testCtx.setupOpts.VPCMode,
				IPv4Namespace: testCtx.setupOpts.IPv4Namespace,
				VLANNamespace: testCtx.setupOpts.VLANNamespace,
				Subnets: map[string]*vpcapi.VPCSubnet{
					"subnet-01": {
						Subnet:  hbgp.Subnet.String(),
						HostBGP: true,
						VLAN:    hbgp.Vlan,
					},
				},
			},
		}
		allVPCs = append(allVPCs, hbgp.VPC)

		switch vpcName {
		case hbgpVPCAName:
			hostBGP.A = hbgp

		case hbgpVPCBName:
			hostBGP.B = hbgp
		}
		for i := range mhConns.Items {
			allAttaches = append(allAttaches, makeVPCAttachment(mhConns.Items[i].Name, vpcName, "subnet-01"))
		}
	}

	// Find 2 other servers and create regular VPCs for them
	serverList := &wiringapi.ServerList{}
	if err := testCtx.kube.List(ctx, serverList); err != nil {
		return false, reverts, fmt.Errorf("listing servers: %w", err)
	}

	for _, server := range serverList.Items {
		if server.Name == mhServer.Server.Name {
			continue
		}
		connList := &wiringapi.ConnectionList{}
		if err := testCtx.kube.List(ctx, connList, kclient.MatchingLabels{
			wiringapi.ListLabelServer(server.Name): wiringapi.ListLabelValue,
		}); err != nil {
			return false, reverts, fmt.Errorf("listing connections for server %s: %w", server.Name, err)
		}
		if len(connList.Items) == 0 {
			continue
		}
		connIndex := 0
		if testCtx.setupOpts.VPCMode != vpcapi.VPCModeL2VNI {
			found := false
			for i, conn := range connList.Items {
				if conn.Spec.ESLAG != nil {
					continue
				}
				found = true
				connIndex = i

				break
			}
			if !found {
				slog.Debug("skipping server as it only has ESLAG connections and we are not in L2VNI mode", "server", server.Name)

				continue
			}
		}

		if regularVPCA.Server == nil {
			regularVPCA.Server = &server
			regularVPCA.Connection = &connList.Items[connIndex]
		} else {
			regularVPCB.Server = &server
			regularVPCB.Connection = &connList.Items[connIndex]

			break
		}
	}
	if regularVPCB.Server == nil {
		return true, reverts, fmt.Errorf("not enough servers to create two regular VPCs") //nolint:err113
	}
	slog.Debug("Found regular servers for hostBGP test", "serverA", regularVPCA.Server.Name, "connA", regularVPCA.Connection.Name, "serverB", regularVPCB.Server.Name, "connB", regularVPCB.Connection.Name)

	for _, vpcName := range []string{regularVPCAName, regularVPCBName} {
		regularSubnet, err := alloc.allocSubnet()
		if err != nil {
			return false, reverts, fmt.Errorf("allocating subnet for VPC %s: %w", vpcName, err)
		}
		regularVLAN, err := alloc.allocVLAN()
		if err != nil {
			return false, reverts, fmt.Errorf("allocating VLAN for VPC %s: %w", vpcName, err)
		}
		regularVPC := &vpcapi.VPC{
			ObjectMeta: kmetav1.ObjectMeta{Name: vpcName, Namespace: kmetav1.NamespaceDefault},
			Spec: vpcapi.VPCSpec{
				Mode:          testCtx.setupOpts.VPCMode,
				IPv4Namespace: testCtx.setupOpts.IPv4Namespace,
				VLANNamespace: testCtx.setupOpts.VLANNamespace,
				Subnets: map[string]*vpcapi.VPCSubnet{
					regularSubnetID: {
						Subnet: regularSubnet.String(),
						VLAN:   regularVLAN,
						DHCP:   vpcapi.VPCDHCP{Enable: true},
					},
				},
			},
		}
		allVPCs = append(allVPCs, regularVPC)
		switch vpcName {
		case regularVPCAName:
			regularVPCA.VPC = regularVPC
			allAttaches = append(allAttaches, makeVPCAttachment(regularVPCA.Connection.Name, vpcName, regularSubnetID))
		case regularVPCBName:
			regularVPCB.VPC = regularVPC
			allAttaches = append(allAttaches, makeVPCAttachment(regularVPCB.Connection.Name, vpcName, regularSubnetID))
		}
	}

	// Register cleanup revert before creating any resources
	reverts = append(reverts, func(ctx context.Context) error {
		return hhfctl.VPCWipeWithClient(ctx, testCtx.kube)
	})

	for _, vpc := range allVPCs {
		if _, err := CreateOrUpdateVpc(ctx, testCtx.kube, vpc); err != nil {
			return false, reverts, fmt.Errorf("creating VPC %s: %w", vpc.Name, err)
		}
	}
	for _, attach := range allAttaches {
		if err := testCtx.kube.Create(ctx, attach); err != nil {
			return false, reverts, fmt.Errorf("creating attachment %s: %w", attach.Name, err)
		}
	}

	peeringA := vpcapi.VPCPeering{
		ObjectMeta: kmetav1.ObjectMeta{Name: fmt.Sprintf("%s--%s", hbgpVPCAName, regularVPCA.VPC.Name), Namespace: kmetav1.NamespaceDefault},
		Spec: vpcapi.VPCPeeringSpec{
			Permit: []map[string]vpcapi.VPCPeer{
				{
					hbgpVPCAName: {
						Subnets: []string{"subnet-01"},
					},
					regularVPCA.VPC.Name: {
						Subnets: []string{"subnet-01"},
					},
				},
			},
		},
	}
	if err := testCtx.kube.Create(ctx, &peeringA); err != nil {
		return false, reverts, fmt.Errorf("creating peering %s: %w", peeringA.Name, err)
	}
	peeringB := vpcapi.VPCPeering{
		ObjectMeta: kmetav1.ObjectMeta{Name: fmt.Sprintf("%s--%s", hbgpVPCBName, regularVPCB.VPC.Name), Namespace: kmetav1.NamespaceDefault},
		Spec: vpcapi.VPCPeeringSpec{
			Permit: []map[string]vpcapi.VPCPeer{
				{
					hbgpVPCBName: {
						Subnets: []string{"subnet-01"},
					},
					regularVPCB.VPC.Name: {
						Subnets: []string{"subnet-01"},
					},
				},
			},
		},
	}
	if err := testCtx.kube.Create(ctx, &peeringB); err != nil {
		return false, reverts, fmt.Errorf("creating peering %s: %w", peeringB.Name, err)
	}

	if err := DoVLABWait(ctx, testCtx.vlabCfg.WorkDir, testCtx.vlabCfg.CacheDir); err != nil {
		return false, reverts, fmt.Errorf("waiting for VLAB: %w", err)
	}

	// get SSH clients for all servers
	sshClients := map[string]*sshutil.Config{}
	for _, server := range []string{mhServer.Server.Name, regularVPCA.Server.Name, regularVPCB.Server.Name} {
		ssh, err := testCtx.getSSH(ctx, server)
		if err != nil {
			return false, reverts, fmt.Errorf("getting SSH for %s: %w", server, err)
		}
		sshClients[server] = ssh
	}

	g := errgroup.Group{}
	g.Go(func() error {
		ssh, ok := sshClients[mhServer.Server.Name]
		if !ok {
			return fmt.Errorf("no SSH client for %s", mhServer.Server.Name) //nolint:err113
		}
		// Cleanup any previous config
		if _, _, err := ssh.Run(ctx, "docker stop -t 1 hostbgp"); err != nil {
			// Ignore – container may not be running
			_ = err
		}
		if _, stderr, err := ssh.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
			return fmt.Errorf("hhnet cleanup on %s: %w: %s", mhServer.Server.Name, err, stderr)
		}
		params := make([]HostBGPParams, 2)
		conns := make([]*wiringapi.Connection, len(mhConns.Items))
		for i, conn := range mhConns.Items {
			conns[i] = &conn
		}
		for i, vpc := range []*vpcapi.VPC{hostBGP.A.VPC, hostBGP.B.VPC} {
			vpcsub, ok := vpc.Spec.Subnets["subnet-01"]
			if !ok {
				return fmt.Errorf("no subnet-01 in hostBGP VPC %s", vpc.Name) //nolint:err113
			}
			prefix, err := netip.ParsePrefix(vpcsub.Subnet)
			if err != nil {
				return fmt.Errorf("error parsing VPC's %s subnet: %w", vpc.Name, err)
			}
			params[i] = HostBGPParams{VPCLabel: vpc.Name, Connections: conns, VLAN: vpcsub.VLAN, Subnet: prefix, ServerOffset: 1}
		}
		cmd, err := getServerHostBGPCmd(params)
		if err != nil {
			return fmt.Errorf("error getting hostBGP command: %w", err)
		}
		slog.Debug("Starting hostBGP container", "server", mhServer.Server.Name, "args", cmd)
		_, stderr, err := ssh.Run(ctx, "docker run --network=host --privileged --rm --detach --name hostbgp ghcr.io/githedgehog/host-bgp "+cmd)
		if err != nil {
			return fmt.Errorf("starting hostbgp on %s: %w: %s", mhServer.Server.Name, err, stderr)
		}
		time.Sleep(1 * time.Second)
		stdout, stderr, err := ssh.Run(ctx, "/opt/bin/hhnet getvips")
		if err != nil {
			return fmt.Errorf("fetching ip addresses on loopback: %w: out: %s", err, stderr)
		}
		for line := range strings.Lines(stdout) {
			line = strings.TrimSpace(line)
			prefix, err := netip.ParsePrefix(line)
			if err != nil {
				return fmt.Errorf("parsing VIP %q: %w", line, err)
			}
			switch {
			case hostBGP.A.Subnet.Contains(prefix.Addr()):
				hostBGP.A.VIP = prefix
			case hostBGP.B.Subnet.Contains(prefix.Addr()):
				hostBGP.B.VIP = prefix
			default:
				slog.Warn("hostBGP VIP does not match any of the VPCs subnets, ignoring it", "VIP", prefix.String())
			}
		}

		return nil
	})

	for _, regularVPC := range []RegularVPC{regularVPCA, regularVPCB} {
		g.Go(func() error {
			ssh, ok := sshClients[regularVPC.Server.Name]
			if !ok {
				return fmt.Errorf("no SSH client for %s", regularVPC.Server.Name) //nolint:err113
			}
			// Cleanup any previous config
			if _, _, err := ssh.Run(ctx, "docker stop -t 1 hostbgp"); err != nil {
				// Ignore – container may not be running
				_ = err
			}
			if _, stderr, err := ssh.Run(ctx, "/opt/bin/hhnet cleanup"); err != nil {
				return fmt.Errorf("hhnet cleanup on %s: %w: %s", regularVPC.Server.Name, err, stderr)
			}
			subnet, ok := regularVPC.VPC.Spec.Subnets["subnet-01"]
			if !ok {
				return fmt.Errorf("no subnet-01 in regular VPC %s", regularVPC.VPC.Name) //nolint:err113
			}
			netconfCmd, err := GetServerNetconfCmd(regularVPC.Connection, ServerNetconfOpts{
				VLAN:       subnet.VLAN,
				HashPolicy: testCtx.setupOpts.HashPolicy,
			})
			if err != nil {
				return fmt.Errorf("error getting netconf command for server %s: %w", regularVPC.Server.Name, err)
			}
			slog.Debug("Configuring server with hhnet", "server", regularVPC.Server.Name, "netconfCmd", netconfCmd)
			stdout, stderr, err := ssh.Run(ctx, "/opt/bin/hhnet "+netconfCmd)
			if err != nil {
				return fmt.Errorf("hhnet configure on %s: %w: %s", regularVPC.Server.Name, err, stderr)
			}
			prefix, err := netip.ParsePrefix(strings.TrimSpace(stdout))
			if err != nil {
				return fmt.Errorf("parsing acquired address %q: %w", stdout, err)
			}
			switch regularVPC.VPC.Name {
			case regularVPCA.VPC.Name:
				regularVPCA.ServerPrefix = prefix
			case regularVPCB.VPC.Name:
				regularVPCB.ServerPrefix = prefix
			}

			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return false, reverts, fmt.Errorf("error configuring networking on servers: %w", err)
	}

	// TODO: remove this when we move past the oldest release without the iperf server in the butane template
	for serverName, ssh := range sshClients {
		if err := ensureIperf3Daemon(ctx, ssh, serverName); err != nil {
			return false, reverts, fmt.Errorf("ensuring iperf3 daemon on %s: %w", serverName, err)
		}
	}

	slog.Debug("Waiting for network convergence...")
	time.Sleep(30 * time.Second)

	// these are all bidirectional
	type ConnCheckPair struct {
		SrcServer string
		DstServer string
		Expected  bool
		SrcIP     netip.Addr
		DstIP     netip.Addr
	}
	pairsToCheck := []ConnCheckPair{
		{
			SrcServer: mhServer.Server.Name,
			DstServer: regularVPCA.Server.Name,
			SrcIP:     hostBGP.A.VIP.Addr(),
			DstIP:     regularVPCA.ServerPrefix.Addr(),
			Expected:  true,
		},
		{
			SrcServer: mhServer.Server.Name,
			DstServer: regularVPCB.Server.Name,
			SrcIP:     hostBGP.A.VIP.Addr(),
			DstIP:     regularVPCB.ServerPrefix.Addr(),
			Expected:  false,
		},
		{
			SrcServer: mhServer.Server.Name,
			DstServer: regularVPCB.Server.Name,
			SrcIP:     hostBGP.B.VIP.Addr(),
			DstIP:     regularVPCB.ServerPrefix.Addr(),
			Expected:  true,
		},
		{
			SrcServer: mhServer.Server.Name,
			DstServer: regularVPCA.Server.Name,
			SrcIP:     hostBGP.B.VIP.Addr(),
			DstIP:     regularVPCA.ServerPrefix.Addr(),
			Expected:  false,
		},
	}

	manualConnCheck := func(ctx context.Context, tcOpts TestConnectivityOpts, sshClients map[string]*sshutil.Config, pairsToCheck []ConnCheckPair) []error {
		checkErrs := []error{}
		for _, pair := range pairsToCheck {
			slog.Debug("Manual connectivity check", "srcServer", pair.SrcServer, "srcIP", pair.SrcIP.String(), "dstServer", pair.DstServer, "dstIP", pair.DstIP, "expected", pair.Expected)
			srcSSH, ok := sshClients[pair.SrcServer]
			if !ok {
				return []error{fmt.Errorf("no SSH client for %s", pair.SrcServer)} //nolint:err113
			}
			dstSSH, ok := sshClients[pair.DstServer]
			if !ok {
				return []error{fmt.Errorf("no SSH client for %s", pair.DstServer)} //nolint:err113
			}
			if !pair.SrcIP.IsValid() || !pair.DstIP.IsValid() {
				return []error{fmt.Errorf("invalid IP pair: src=%s dst=%s", pair.SrcIP, pair.DstIP)} //nolint:err113
			}
			pingErr := checkPing(ctx, tcOpts.PingsCount, nil,
				pair.SrcServer, pair.DstServer, srcSSH,
				pair.DstIP, &pair.SrcIP, pair.Expected)
			if pingErr != nil {
				checkErrs = append(checkErrs, pingErr)
			}
			revPingErr := checkPing(ctx, tcOpts.PingsCount, nil,
				pair.DstServer, pair.SrcServer, dstSSH,
				pair.SrcIP, &pair.DstIP, pair.Expected)
			if revPingErr != nil {
				checkErrs = append(checkErrs, revPingErr)
			}

			if pair.Expected && pingErr == nil && revPingErr == nil {
				reach := Reachability{
					Reachable: pair.Expected,
					Reason:    ReachabilityReasonSwitchPeering,
				}
				iperfErrs := checkIPerf(ctx, tcOpts, pair.SrcServer, pair.DstServer,
					srcSSH, pair.DstIP, pair.SrcIP, reach, true)
				for _, i := range iperfErrs {
					checkErrs = append(checkErrs, i)
				}
			}
		}

		return checkErrs
	}

	if checkErrs := manualConnCheck(ctx, testCtx.tcOpts, sshClients, pairsToCheck); len(checkErrs) != 0 {
		return false, reverts, fmt.Errorf("manual connectivity checks failed: %w", errors.Join(checkErrs...))
	}

	// Link failover: bring down all connections to one of the switches
	var failoverSwitchName string
	for k := range mhServer.SwitchConns {
		failoverSwitchName = k

		break
	}
	failoverSw := &wiringapi.Switch{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: failoverSwitchName}, failoverSw); err != nil {
		return false, reverts, fmt.Errorf("getting switch %s: %w", failoverSwitchName, err)
	}
	failoverProfile := &wiringapi.SwitchProfile{}
	if err := testCtx.kube.Get(ctx, kclient.ObjectKey{Namespace: kmetav1.NamespaceDefault, Name: failoverSw.Spec.Profile}, failoverProfile); err != nil {
		return false, reverts, fmt.Errorf("getting switch profile %s: %w", failoverSw.Spec.Profile, err)
	}
	failoverPortMap, err := failoverProfile.Spec.GetAPI2NOSPortsFor(&failoverSw.Spec)
	if err != nil {
		return false, reverts, fmt.Errorf("getting API2NOS ports for %s: %w", failoverSwitchName, err)
	}

	failoverSwSSH, err := testCtx.getSSH(ctx, failoverSwitchName)
	if err != nil {
		return false, reverts, fmt.Errorf("getting SSH for switch %s: %w", failoverSwitchName, err)
	}

	slog.Debug("Disabling HH agent for link failover test", "switch", failoverSwitchName)
	if err := changeAgentStatus(ctx, failoverSwSSH, failoverSwitchName, false); err != nil {
		return false, reverts, fmt.Errorf("disabling agent on %s: %w", failoverSwitchName, err)
	}
	reverts = append(reverts, func(ctx context.Context) error {
		return changeAgentStatus(ctx, failoverSwSSH, failoverSwitchName, true)
	})

	slog.Debug("Shutting down link(s) for failover test", "switch", failoverSwitchName)
	for _, failoverConn := range mhServer.SwitchConns[failoverSwitchName] {
		failoverLink := failoverConn.Spec.Unbundled.Link
		failoverNOSPort, ok := failoverPortMap[failoverLink.Switch.LocalPortName()]
		if !ok {
			return false, reverts, fmt.Errorf("port %s not in profile %s for switch %s", failoverLink.Switch.LocalPortName(), failoverProfile.Name, failoverSwitchName) //nolint:goerr113
		}
		if err := changeSwitchPortStatus(ctx, failoverSwSSH, failoverSwitchName, failoverNOSPort, false); err != nil {
			return false, reverts, fmt.Errorf("shutting down port %s on %s: %w", failoverNOSPort, failoverSwitchName, err)
		}
		reverts = append(reverts, func(ctx context.Context) error {
			return changeSwitchPortStatus(ctx, failoverSwSSH, failoverSwitchName, failoverNOSPort, true)
		})
	}

	slog.Debug("Waiting for BGP reconvergence after link failover...")
	time.Sleep(30 * time.Second)

	if checkErrs := manualConnCheck(ctx, testCtx.tcOpts, sshClients, pairsToCheck); len(checkErrs) != 0 {
		return false, reverts, fmt.Errorf("connectivity check failed after link failover: %w", errors.Join(checkErrs...))
	}

	return false, reverts, nil
}
