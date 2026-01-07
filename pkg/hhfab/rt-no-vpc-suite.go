// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package hhfab

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	fabricatorapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab/comp"
	gwapi "go.githedgehog.com/gateway/api/gateway/v1alpha1"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
)

func makeNoVpcsSuite(testCtx *VPCPeeringTestCtx) *JUnitTestSuite {
	suite := &JUnitTestSuite{
		Name: "No VPCs Suite",
	}
	suite.TestCases = []JUnitTestCase{
		{
			Name: "Breakout ports",
			F:    testCtx.breakoutTest,
			SkipFlags: SkipFlags{
				VirtualSwitch: true,
			},
		},
		{
			Name: "Loki Observability",
			F:    testCtx.lokiObservabilityTest,
			SkipFlags: SkipFlags{
				NoLoki: true,
			},
		},
		{
			Name: "Prometheus Observability",
			F:    testCtx.prometheusObservabilityTest,
			SkipFlags: SkipFlags{
				NoProm: true,
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
func (testCtx *VPCPeeringTestCtx) breakoutTest(ctx context.Context) (bool, []RevertFunc, error) {
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

func (testCtx *VPCPeeringTestCtx) lokiObservabilityTest(ctx context.Context) (bool, []RevertFunc, error) {
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

		if lokiResp.Status != "success" {
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

func (testCtx *VPCPeeringTestCtx) prometheusObservabilityTest(ctx context.Context) (bool, []RevertFunc, error) {
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

	var promResp struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []interface{}     `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &promResp); err != nil {
		return false, nil, fmt.Errorf("failed to parse prometheus response: %w", err)
	}

	// Log query details with status and count
	slog.Debug("Prometheus query details", "query", queryExpr, "status", resp.Status, "count", len(promResp.Data.Result))

	if promResp.Status != "success" {
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

	return false, nil, nil
}
