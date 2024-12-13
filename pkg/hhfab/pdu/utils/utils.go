// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// ParseOutletJSON parses the JSON file and extracts outlet mappings and unique PDU IPs
func ParseOutletJSON(jsonFilePath string) (map[string]string, []string, error) {
	// Read the JSON file
	data, err := os.ReadFile(jsonFilePath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read file: %w", err)
	}

	// Parse the JSON into a map
	var outlets map[string]string
	err = json.Unmarshal(data, &outlets)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal JSON: %w", err)
	}

	// Extract unique PDU IPs
	ipSet := make(map[string]struct{})
	for _, urlStr := range outlets {
		parsedURL, err := url.Parse(urlStr)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to parse URL %q: %w", urlStr, err)
		}

		ip := strings.Split(parsedURL.Host, ":")[0] // Get the host without port
		ipSet[ip] = struct{}{}
	}

	// Convert the set to a list
	var uniqueIPs []string
	for ip := range ipSet {
		uniqueIPs = append(uniqueIPs, ip)
	}

	return outlets, uniqueIPs, nil
}

func ExtractOutletID(url string) (int, error) {
	parts := strings.Split(url, "/")

	// Check if URL has at least 1 part (outlet ID is expected to be the last part)
	if len(parts) < 1 {
		return 0, fmt.Errorf("invalid URL format: expected at least one part") //nolint:goerr113
	}

	outletID := parts[len(parts)-1]
	id, err := strconv.Atoi(outletID)
	if err != nil {
		return 0, fmt.Errorf("error extracting outlet ID from '%s': %w", outletID, err) //nolint:goerr113
	}

	return id, nil
}

func GetPDUIPFromURL(url string) (string, error) {
	parts := strings.Split(url, "/")

	// URL must have at least 3 parts: http://PDUIP/outlet/PORT
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid URL format: expected at least 3 parts, got %d", len(parts)) //nolint:goerr113
	}

	return parts[2], nil
}
