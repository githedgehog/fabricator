// Copyright 2024 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"encoding/json"
	"fmt"
	"log"
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

func ExtractOutletID(url string) int {
	parts := strings.Split(url, "/")
	outletID := parts[len(parts)-1]
	id, err := strconv.Atoi(outletID)
	if err != nil {
		log.Fatalf("Error extracting outlet ID: %v", err)
	}

	return id
}

func GetPDUIPFromURL(url string) string {
	parts := strings.Split(url, "/")

	return parts[2]
}
