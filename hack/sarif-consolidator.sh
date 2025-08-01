#!/bin/bash
# Copyright 2025 Hedgehog
# SPDX-License-Identifier: Apache-2.0

set -e

RAW_SARIF_DIR="${1:-raw-sarif-reports}"
RESULTS_DIR="${2:-trivy-reports}"
SARIF_OUTPUT_DIR="sarif-reports"

GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m'

if ! command -v jq &> /dev/null; then
    echo -e "${RED}ERROR: jq is required but not installed${NC}"
    exit 1
fi

if [ ! -d "$RAW_SARIF_DIR" ]; then
    echo -e "${RED}ERROR: Raw SARIF directory not found: $RAW_SARIF_DIR${NC}"
    echo "Please run vlab-trivy-runner.sh first to collect raw SARIF files"
    exit 1
fi

echo -e "${GREEN}Starting SARIF consolidation and processing${NC}"
echo "Raw SARIF directory: $RAW_SARIF_DIR"
echo "Results directory: $RESULTS_DIR"
echo "Output directory: $SARIF_OUTPUT_DIR"
echo ""

mkdir -p "$SARIF_OUTPUT_DIR"

TOTAL_CRITICAL_VULNS=0
TOTAL_HIGH_VULNS=0
TOTAL_IMAGES_SCANNED=0
DEDUP_CRITICAL=0
DEDUP_HIGH=0

extract_container_info() {
    local image_name="$1"
    local container_name=""
    local version=""

    container_name=$(echo "$image_name" | sed -E 's|.*/([^/:]+).*|\1|')

    if [[ "$image_name" == *":"* ]]; then
        version=$(echo "$image_name" | sed -E 's|.*:([^/]+)$|\1|')
    else
        version="latest"
    fi

    # Handle edge cases
    if [ -z "$container_name" ] || [ "$container_name" = "$image_name" ]; then
        # No / found, use the part before :
        container_name=$(echo "$image_name" | sed -E 's|^([^:]+):?.*|\1|')
    fi

    echo "${container_name}:${version}"
}

process_vm_sarif() {
    local vm_name="$1"
    local vm_sarif_dir="$RAW_SARIF_DIR/$vm_name"
    local vm_results_dir="$RESULTS_DIR/$vm_name"

    if [ ! -d "$vm_sarif_dir" ]; then
        echo -e "${YELLOW}No SARIF directory found for $vm_name, skipping${NC}"
        return 1
    fi

    echo -e "${YELLOW}Processing SARIF files for $vm_name${NC}"

    # Find all SARIF files for this VM
    sarif_files=()
    while IFS= read -r -d '' file; do
        sarif_files+=("$file")
    done < <(find "$vm_sarif_dir" -name '*_critical.sarif' -type f -print0 2>/dev/null)

    if [ ${#sarif_files[@]} -eq 0 ]; then
        echo -e "${YELLOW}No SARIF files found for $vm_name${NC}"
        return 1
    fi

    echo "Found ${#sarif_files[@]} SARIF files for $vm_name"

    # Load container images list
    local container_images=()
    if [ -f "$vm_results_dir/container_images.txt" ]; then
        while IFS= read -r line; do
            [ -n "$line" ] && container_images+=("$line")
        done < "$vm_results_dir/container_images.txt"
    fi

    local image_count=${#container_images[@]}
    TOTAL_IMAGES_SCANNED=$((TOTAL_IMAGES_SCANNED + image_count))

    # Determine VM type and scan mode
    local vm_type="control"
    local scan_mode="online"

    if [[ "$vm_name" == *"gateway"* ]]; then
        vm_type="gateway"
        scan_mode="airgapped"
    elif [[ "$vm_name" == *"leaf"* ]] || [[ "$vm_name" == *"switch"* ]]; then
        vm_type="switch"
        scan_mode="sonic-airgapped"
    fi

    local consolidated_sarif="$SARIF_OUTPUT_DIR/trivy-consolidated-${vm_name}.sarif"

    if [ ${#sarif_files[@]} -eq 1 ]; then
        # Single file - ensure it has proper SARIF structure
        jq '
        {
          "version": (.version // "2.1.0"),
          "$schema": (."$schema" // "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json"),
          "runs": .runs
        }
        ' "${sarif_files[0]}" > "$consolidated_sarif"
        echo "Single SARIF file copied for $vm_name"
    else
        # Multiple files, merge them
        echo "Merging ${#sarif_files[@]} SARIF files for $vm_name..."
        jq -s '
            .[0] as $base |
            (map(.runs[0].results // []) | add | unique_by(.ruleId + .locations[0].physicalLocation.artifactLocation.uri)) as $all_results |
            (map(.runs[0].tool.driver.rules // []) | add | unique_by(.id)) as $all_rules |
            {
              "version": ($base.version // "2.1.0"),
              "$schema": ($base."$schema" // "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json"),
              "runs": [
                ($base.runs[0] |
                  .tool.driver.rules = $all_rules |
                  .results = $all_results
                )
              ]
            }
        ' "${sarif_files[@]}" > "$consolidated_sarif"

        if [ ! -s "$consolidated_sarif" ] || ! jq empty "$consolidated_sarif" 2>/dev/null; then
            echo -e "${RED}Failed to merge SARIF files for $vm_name${NC}"
            return 1
        fi
    fi

    # Process and enhance the consolidated SARIF file
    echo "Enhancing SARIF file for $vm_name..."

    # Create container images JSON array
    local containers_json="[]"
    if [ $image_count -gt 0 ]; then
        containers_json=$(printf '%s\n' "${container_images[@]}" | jq -R . | jq -s .)
    fi

    # Get deployment context
    local deployment_id="${GITHUB_RUN_ID:-unknown}"
    local commit_sha="${GITHUB_SHA:-unknown}"
    local repo="${GITHUB_REPOSITORY:-unknown}"
    local actor="${GITHUB_ACTOR:-unknown}"
    local registry_repo="${HHFAB_REG_REPO:-127.0.0.1:30000}"

    # Create image mapping by reading imageName and imageID from SARIF files
    echo "Mapping SARIF files to container images..."
    declare -A sarif_to_image_map
    declare -A imageID_to_representative
    local mapped_count=0
    local total_files=${#sarif_files[@]}

    for file in "${sarif_files[@]}"; do
        filename=$(basename "$file")
        echo "  Processing file: $filename"

        # Extract imageName and imageID from SARIF file
        image_name=$(jq -r '.runs[0].properties.imageName // empty' "$file" 2>/dev/null)
        image_id=$(jq -r '.runs[0].properties.imageID // empty' "$file" 2>/dev/null)

        if [ -z "$image_name" ] || [ "$image_name" = "null" ]; then
            echo "    ✗ No imageName found in SARIF file"
            continue
        fi

        echo "    Found imageName: $image_name"

        # Handle airgapped mode where imageName is a tar file path
        if [[ "$image_name" == "/tmp/trivy-export-"* ]]; then
            tar_basename=$(basename "$image_name" .tar)
            if [[ "$tar_basename" =~ ^.*\$\/(.+)$ ]]; then
                image_part="${BASH_REMATCH[1]}"
            else
                image_part="$tar_basename"
            fi

            # Convert underscores back to proper image format
            if [[ "$image_part" =~ ^docker\.io_(.+)_v([0-9].*)$ ]]; then
                path_part="${BASH_REMATCH[1]}"
                version="v${BASH_REMATCH[2]}"
                image_path_fixed=$(echo "$path_part" | sed 's/_/\//g')
                reconstructed_image="docker.io/${image_path_fixed}:${version}"
            elif [[ "$image_part" =~ ^docker\.io_(.+)_([0-9].*)$ ]]; then
                path_part="${BASH_REMATCH[1]}"
                version="${BASH_REMATCH[2]}"
                image_path_fixed=$(echo "$path_part" | sed 's/_/\//g')
                reconstructed_image="docker.io/${image_path_fixed}:${version}"
            elif [[ "$image_part" =~ ^172\.30\.0\.1_31000_(.+)_v([0-9].*)$ ]]; then
                path_part="${BASH_REMATCH[1]}"
                version="v${BASH_REMATCH[2]}"
                image_path_fixed=$(echo "$path_part" | sed 's/_/\//g')
                reconstructed_image="172.30.0.1:31000/${image_path_fixed}:${version}"
            else
                reconstructed_image=$(echo "$image_part" | sed 's/_/\//g' | sed 's|\([^/]*\)$|:\1|')
            fi

            echo "    Airgapped mode detected - reconstructed: $reconstructed_image"
            image_name="$reconstructed_image"
        fi

        # Find exact match in container images list
        local found_match=false
        for container_image in "${container_images[@]}"; do
            if [[ "$image_name" == "$container_image" ]]; then
                found_match=true
                break
            fi
        done

        if [ "$found_match" = true ]; then
            # Use imageID for deduplication within this VM
            if [ -n "$image_id" ] && [ "$image_id" != "null" ] && [ "$image_id" != "empty" ]; then
                if [[ -z "${imageID_to_representative[$image_id]}" ]]; then
                    imageID_to_representative[$image_id]="$image_name"
                    echo "    ✓ NEW IMAGE: $filename -> $image_name (imageID: $image_id)"
                else
                    echo "    ✓ DUPLICATE IMAGE: $filename -> ${imageID_to_representative[$image_id]} (same imageID: $image_id)"
                fi
                sarif_to_image_map["$file"]="${imageID_to_representative[$image_id]}"
            else
                # No imageID, treat as unique
                sarif_to_image_map["$file"]="$image_name"
                echo "    ✓ MATCHED: $filename -> $image_name (no imageID)"
            fi
            mapped_count=$((mapped_count + 1))
        else
            echo "    ✗ NO MATCH FOUND in container list for: $image_name"
        fi
        echo ""
    done

    echo "Mapping summary: Successfully mapped $mapped_count/$total_files SARIF files to container images"
    echo ""

    # Enhanced jq processing with per-file image mapping
    temp_consolidated="$consolidated_sarif.temp"
    echo '{
      "version": "2.1.0",
      "$schema": "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json",
      "runs": [{
        "tool": {
          "driver": {
            "name": "trivy",
            "rules": []
          }
        },
        "results": []
      }]
    }' > "$temp_consolidated"

    # Group SARIF files by representative image for processing
    echo "Processing SARIF files grouped by representative image..."
    declare -A image_to_sarif_files

    for file in "${sarif_files[@]}"; do
        mapped_image="${sarif_to_image_map[$file]}"
        if [ -n "$mapped_image" ]; then
            if [[ -z "${image_to_sarif_files[$mapped_image]}" ]]; then
                image_to_sarif_files[$mapped_image]="$file"
            else
                image_to_sarif_files[$mapped_image]="${image_to_sarif_files[$mapped_image]} $file"
            fi
        fi
    done

    local processed_count=0
    local skipped_count=0

    for representative_image in "${!image_to_sarif_files[@]}"; do
        sarif_files_for_image="${image_to_sarif_files[$representative_image]}"
        sarif_files_array=($sarif_files_for_image)
        file_count=${#sarif_files_array[@]}

        container_with_version=$(extract_container_info "$representative_image")

        echo "  Processing representative image: $representative_image"
        echo "    Container: $container_with_version"
        echo "    Merging $file_count SARIF file(s): $(echo $sarif_files_for_image | tr ' ' '\n' | xargs -I {} basename {})"

        # Check if any of the SARIF files have vulnerabilities
        total_vulnerabilities=0
        for file in $sarif_files_for_image; do
            result_count=$(jq '.runs[0].results | length' "$file" 2>/dev/null || echo 0)
            total_vulnerabilities=$((total_vulnerabilities + result_count))
        done

        if [ $total_vulnerabilities -eq 0 ]; then
            echo "    Skipping - no vulnerabilities found across all files"
            skipped_count=$((skipped_count + 1))
            continue
        fi

        echo "    Total vulnerabilities across files: $total_vulnerabilities"

        # Create a merged SARIF file for this representative image
        merged_sarif_temp="/tmp/merged-${representative_image//\//_}-$.sarif"

        if [ $file_count -eq 1 ]; then
            # Single file - use directly
            cp "${sarif_files_array[0]}" "$merged_sarif_temp"
        else
            # Multiple files - merge them
            echo "    Merging vulnerabilities from $file_count files..."
            jq -s '
                .[0] as $base |
                (map(.runs[0].results // []) | add | unique_by(.ruleId + .locations[0].physicalLocation.artifactLocation.uri)) as $all_results |
                (map(.runs[0].tool.driver.rules // []) | add | unique_by(.id)) as $all_rules |
                {
                  "version": ($base.version // "2.1.0"),
                  "$schema": ($base."$schema" // "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json"),
                  "runs": [
                    ($base.runs[0] |
                      .tool.driver.rules = $all_rules |
                      .results = $all_results
                    )
                  ]
                }
            ' $sarif_files_for_image > "$merged_sarif_temp"
        fi

        # Process the merged SARIF file
        jq --arg vm_name "$vm_name" \
           --arg vm_type "$vm_type" \
           --arg scan_time "$(date -Iseconds)" \
           --arg deployment_id "$deployment_id" \
           --arg commit_sha "$commit_sha" \
           --arg repo "$repo" \
           --arg actor "$actor" \
           --arg registry_repo "$registry_repo" \
           --arg scan_mode "$scan_mode" \
           --arg source_image "$representative_image" \
           --arg container_with_version "$container_with_version" \
           --argjson container_images "$containers_json" \
           '
           # Extract container details
           ($container_with_version | split(":")[0]) as $container_name |
           ($container_with_version | split(":")[1]) as $container_version |

           # Process each result to fix artifact paths and add container context
           .runs[0].results = (.runs[0].results | map(
             # Store original artifact URI for processing
             (.locations[0].physicalLocation.artifactLocation.uri // "unknown") as $original_uri |

             # Validate URI type
             if ($original_uri | type) != "string" then
               . # Skip invalid URIs
             else
               # Extract meaningful binary name from URI
               (if ($original_uri | test("^/tmp/trivy-export-.*\\.tar$")) then
                 # For trivy export tar files, use the container name
                 $container_name
               elif ($original_uri | test("^usr/bin/.*")) then
                 # For usr/bin paths, use the binary name
                 ($original_uri | split("/")[-1])
               elif ($original_uri | test("/")) then
                 # For other paths with slashes, use the last component
                 ($original_uri | split("/")[-1])
               else
                 # For simple names, use as-is
                 $original_uri
               end) as $binary_name |

               # Create clean artifact path
               ($vm_name + "/" + $container_with_version + "/" + $binary_name) as $new_uri |

               # Update locations with new artifact path
               .locations = (.locations | map(
                 .physicalLocation.artifactLocation.uri = $new_uri |
                 .message.text = ("[" + $vm_name + "/" + $container_with_version + "] " + .message.text)
               )) |

               # Add container-specific properties
               .properties = (.properties // {}) + {
                 vmName: $vm_name,
                 vmType: $vm_type,
                 containerName: $container_name,
                 containerVersion: $container_version,
                 containerWithVersion: $container_with_version,
                 sourceImage: $source_image,
                 originalArtifactUri: $original_uri,
                 binaryName: $binary_name,
                 scanContext: ("runtime-deployment-" + $scan_mode),
                 artifactPath: ($vm_name + "/" + $container_with_version),
                 deduplicated: true
               }
             end
           ))
           ' "$merged_sarif_temp" > "${merged_sarif_temp}.processed"

        # Merge this representative image's results into the consolidated file
        jq -s '
            .[0] as $consolidated |
            .[1] as $new_file |

            $consolidated |
            .runs[0].results += ($new_file.runs[0].results // []) |
            .runs[0].tool.driver.rules += ($new_file.runs[0].tool.driver.rules // []) |
            .runs[0].tool.driver.rules |= unique_by(.id)
        ' "$temp_consolidated" "${merged_sarif_temp}.processed" > "${temp_consolidated}.tmp" && \
        mv "${temp_consolidated}.tmp" "$temp_consolidated"

        # Clean up temporary files
        rm -f "$merged_sarif_temp" "${merged_sarif_temp}.processed"

        processed_count=$((processed_count + 1))
        echo "    ✓ Processed representative image: $representative_image"
        echo ""
    done

    echo ""
    echo "File processing summary:"
    echo "  - Processed $processed_count SARIF files with vulnerabilities"
    echo "  - Skipped $skipped_count clean containers"
    echo ""

    # Add final metadata to consolidated file
    jq --arg vm_name "$vm_name" \
       --arg vm_type "$vm_type" \
       --arg scan_time "$(date -Iseconds)" \
       --arg deployment_id "$deployment_id" \
       --arg commit_sha "$commit_sha" \
       --arg repo "$repo" \
       --arg actor "$actor" \
       --arg registry_repo "$registry_repo" \
       --arg scan_mode "$scan_mode" \
       --argjson container_images "$containers_json" \
       '
       # Count vulnerabilities from individual SARIF files
       ([.runs[0].results[]? | select(.level == "error" and (.message.text | contains("CRITICAL")))] | length) as $critical_count |
       ([.runs[0].results[]? | select(.level == "error" and (.message.text | contains("HIGH")))] | length) as $high_count |
       ([.runs[0].results[]? | select(.level == "warning")] | length) as $medium_count |
       ([.runs[0].results[]? | select(.level == "note")] | length) as $low_count |

       # Extract version information from container images
       ($container_images | map({
         image: .,
         container: (. | split("/")[-1]),
         name: (. | split("/")[-1] | split(":")[0]),
         version: (. | split("/")[-1] | split(":")[1] // "latest")
       })) as $container_details |

       # Preserve SARIF structure and add comprehensive properties
       {
         "version": (.version // "2.1.0"),
         "$schema": (."$schema" // "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json"),
         "runs": [
           (.runs[0] |
             .properties = {
               vmContext: {
                 name: $vm_name,
                 type: $vm_type,
                 scanTimestamp: $scan_time,
                 environment: "vlab",
                 scanMode: $scan_mode,
                 totalContainerImages: ($container_images | length)
               },
               containerContext: {
                 scannedImages: $container_images,
                 registry: $registry_repo,
                 containerDetails: $container_details,
                 aggregatedVulnerabilities: {
                   critical: $critical_count,
                   high: $high_count,
                   medium: $medium_count,
                   low: $low_count,
                   total: ($critical_count + $high_count + $medium_count + $low_count)
                 }
               },
               deploymentContext: {
                 deploymentId: $deployment_id,
                 commitSha: $commit_sha,
                 repository: $repo,
                 triggeredBy: $actor,
                 workflowRun: ("https://github.com/" + $repo + "/actions/runs/" + $deployment_id)
               },
               scanMetadata: {
                 tool: "trivy",
                 category: ("vm-container-runtime-scan-" + $scan_mode),
                 scanScope: "production-deployment",
                 consolidatedReport: true,
                 imageCount: ($container_images | length)
               }
             } |
             .tool.driver.informationUri = ("https://github.com/" + $repo + "/security")
           )
         ]
       }
       ' "$temp_consolidated" > "$consolidated_sarif"

    rm -f "$temp_consolidated"

    # Check if enhancement succeeded
    if [ -s "$consolidated_sarif" ] && jq empty "$consolidated_sarif" 2>/dev/null; then

        # Count vulnerabilities for this VM from the consolidated SARIF
        local vm_critical=$(jq '[.runs[0].results[]? | select(.level == "error" and (.message.text | contains("CRITICAL")))] | length' "$consolidated_sarif" 2>/dev/null || echo 0)
        local vm_high=$(jq '[.runs[0].results[]? | select(.level == "error" and (.message.text | contains("HIGH")))] | length' "$consolidated_sarif" 2>/dev/null || echo 0)

        TOTAL_CRITICAL_VULNS=$((TOTAL_CRITICAL_VULNS + vm_critical))
        TOTAL_HIGH_VULNS=$((TOTAL_HIGH_VULNS + vm_high))

        echo "✓ Enhanced SARIF for $vm_name:"
        echo "  - VM: $vm_name ($vm_type, $scan_mode mode)"
        echo "  - Container images: $image_count"
        echo "  - SARIF files mapped: $mapped_count/$total_files"
        echo "  - Files processed: $processed_count (with vulnerabilities)"
        echo "  - Files skipped: $skipped_count (clean containers)"
        echo "  - Critical vulnerabilities (unique per VM): $vm_critical"
        echo "  - High vulnerabilities (unique per VM): $vm_high"
        echo "  - Artifact paths: $vm_name/container:version/binary"
        echo ""

        return 0
    else
        echo -e "${RED}Enhancement failed for $vm_name${NC}"
        return 1
    fi
}

# Process SARIF files for each VM
VM_COUNT=0
SUCCESS_COUNT=0

for vm_dir in "$RAW_SARIF_DIR"/*; do
    if [ -d "$vm_dir" ]; then
        vm_name=$(basename "$vm_dir")
        VM_COUNT=$((VM_COUNT + 1))

        if process_vm_sarif "$vm_name"; then
            SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
        fi
    fi
done

if [ $SUCCESS_COUNT -eq 0 ]; then
    echo -e "${RED}No SARIF files were successfully processed${NC}"
    exit 1
fi

# Create final consolidated SARIF report
echo -e "${YELLOW}Creating final consolidated SARIF report...${NC}"
final_sarif="$SARIF_OUTPUT_DIR/trivy-security-scan.sarif"
sarif_files=()

# Collect all VM-specific SARIF files
while IFS= read -r -d '' file; do
    sarif_files+=("$file")
done < <(find "$SARIF_OUTPUT_DIR" -name "trivy-consolidated-*.sarif" -type f -print0 2>/dev/null)

if [ ${#sarif_files[@]} -eq 0 ]; then
    echo -e "${RED}No consolidated SARIF files found to merge${NC}"
    exit 1
fi

if [ ${#sarif_files[@]} -eq 1 ]; then
    cp "${sarif_files[0]}" "$final_sarif"
    echo "Single SARIF file copied to final report"
else
    echo "Merging ${#sarif_files[@]} VM SARIF files into final report..."

    jq -s '
        .[0] as $base |
        (map(.runs[0].results // []) | add | unique_by(.ruleId + .locations[0].physicalLocation.artifactLocation.uri)) as $all_results |
        (map(.runs[0].tool.driver.rules // []) | add | unique_by(.id)) as $all_rules |

        {
          "version": "2.1.0",
          "$schema": "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/main/sarif-2.1/schema/sarif-schema-2.1.0.json",
          "runs": [
            ($base.runs[0] |
              .tool.driver.rules = $all_rules |
              .results = $all_results |
              .properties.scanMetadata = (.properties.scanMetadata // {} | . + {
                finalConsolidation: true,
                vmCount: (. | length),
                deduplicationStrategy: "unique_by_rule_id",
                preservesAllLocations: true
              })
            )
          ]
        }
    ' "${sarif_files[@]}" > "$final_sarif"
fi

if [ ! -s "$final_sarif" ] || ! jq empty "$final_sarif" 2>/dev/null; then
    echo -e "${RED}Failed to create final SARIF report${NC}"
    exit 1
fi

# Calculate deduplicated vulnerability counts from final SARIF
DEDUP_CRITICAL=$(jq '[.runs[0].tool.driver.rules[]? | select(.properties.tags | contains(["CRITICAL"]))] | length' "$final_sarif" 2>/dev/null || echo 0)
DEDUP_HIGH=$(jq '[.runs[0].tool.driver.rules[]? | select(.properties.tags | contains(["HIGH"]))] | length' "$final_sarif" 2>/dev/null || echo 0)

# Ensure valid numbers
DEDUP_CRITICAL=${DEDUP_CRITICAL:-0}
DEDUP_HIGH=${DEDUP_HIGH:-0}

if ! [[ "$DEDUP_CRITICAL" =~ ^[0-9]+$ ]]; then
    DEDUP_CRITICAL=0
fi
if ! [[ "$DEDUP_HIGH" =~ ^[0-9]+$ ]]; then
    DEDUP_HIGH=0
fi

total_results=$(jq '.runs[0].results | length' "$final_sarif" 2>/dev/null || echo 0)

echo -e "${GREEN}Final consolidated SARIF report created: $final_sarif${NC}"
echo "  - Total vulnerability instances: $total_results"
echo "  - Unique rules (deduplicated): $((DEDUP_CRITICAL + DEDUP_HIGH))"

# Generate summary
echo ""
echo -e "${GREEN}=== SARIF Processing Summary ===${NC}"
echo "Successfully processed: $SUCCESS_COUNT/$VM_COUNT VMs"
echo "Total container images scanned: $TOTAL_IMAGES_SCANNED"
echo ""
echo -e "${GREEN}=== Vulnerability Summary ===${NC}"
echo "Raw vulnerability instances:"
echo "  - Critical: $TOTAL_CRITICAL_VULNS"
echo "  - High: $TOTAL_HIGH_VULNS"
echo "  - Total: $((TOTAL_CRITICAL_VULNS + TOTAL_HIGH_VULNS))"
echo ""
echo "Deduplicated vulnerabilities (unique rules):"
echo "  - Critical: $DEDUP_CRITICAL"
echo "  - High: $DEDUP_HIGH"
echo "  - Total unique: $((DEDUP_CRITICAL + DEDUP_HIGH))"
echo ""
echo "Files created:"
echo "  - Final SARIF: $final_sarif"
for file in "$SARIF_OUTPUT_DIR"/trivy-consolidated-*.sarif; do
    [ -f "$file" ] && echo "  - VM SARIF: $(basename "$file")"
done

if [ ! -z "$GITHUB_ENV" ]; then
    echo "SARIF_FILE=$final_sarif" >> "$GITHUB_ENV"
    echo "UPLOAD_SARIF=true" >> "$GITHUB_ENV"
fi

echo -e "${GREEN}SARIF consolidation completed successfully${NC}"
