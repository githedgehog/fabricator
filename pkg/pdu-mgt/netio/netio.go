// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package netio

import (
	"context"
	"encoding/json"
	"io"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// NetioOutlet defines the structure for each outlet.
type NetioOutlet struct {
    ID      int     `json:"ID"`
    Name    string  `json:"Name"`
    State   int     `json:"State"`
    Current float64 `json:"Current"`
    Load    int     `json:"Load"`
}

// NetioResponse defines the response structure containing multiple outlets.
type NetioResponse struct {
    Outputs []NetioOutlet `json:"Outputs"`
}

type Agent struct {
	DeviceName string `json:"DeviceName"`
}

type NetioAgentResponse struct {
	Agent Agent `json:"Agent"`
}
var actionMap = map[string]int{
	"OFF":   0,
	"ON":    1,
	"CYCLE": 2,
}

func GetStatus(pduIP, username, password string) (*NetioResponse, error) {
	url := fmt.Sprintf("http://%s/netio.json", pduIP)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(username, password)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Check if the response status is 200 OK, otherwise log the response body
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("Error response body: %s\n", body)
		return nil, fmt.Errorf("unexpected response status: %d", resp.StatusCode)
	}

	var netioResp NetioResponse
	err = json.NewDecoder(resp.Body).Decode(&netioResp)
	if err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &netioResp, nil
}

func ControlOutlet(ctx context.Context, pduIP, username, password string, outletID int, action string) error {
	url := fmt.Sprintf("http://%s/netio.json", pduIP)
	data := fmt.Sprintf(`{"Outputs":[{"ID":%d,"Action":%d}]}`, outletID, actionMap[action])

	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(username, password)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to control outlet: %s", resp.Status)
	}

	return nil
}

// GetPDUName queries the PDU for its name.
func GetPDUName(pduIP, username, password string) (string, error) {
	url := fmt.Sprintf("http://%s/netio.json", pduIP)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(username, password)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to query PDU: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	var netioAgentResponse NetioAgentResponse
	if err := json.Unmarshal(body, &netioAgentResponse); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return netioAgentResponse.Agent.DeviceName, nil
}
