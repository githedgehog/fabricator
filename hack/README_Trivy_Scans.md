# Trivy Security Scanning System

## Overview

| File | Purpose |
|------|---------|
| `hack/vlab-trivy-runner.sh` | Main orchestration script |
| `hack/sarif-consolidator.sh` | SARIF processing and deduplication |
| `hack/trivy-setup.sh` | Online mode setup (control-1) |
| `hack/trivy-setup-airgapped.sh` | Airgapped mode setup (gateway-1) |
| `hack/trivy-setup-sonic-airgapped.sh` | SONiC switch setup |

The scripts perform comprehensive vulnerability scanning across a Hedgehog Fabric VLAB deployment using [Trivy](https://trivy.dev/). Rather than scanning isolated container images, we scan **running containers in a realistic network topology** to catch deployment-specific vulnerabilities and configuration issues.

The scripts generate security reports compatible with GitHub's Security tab, providing centralized vulnerability tracking and automated security alerts.

## VLAB Architecture

Our security scans run against a minimal VLAB (Virtual Lab) topology that represents a real Hedgehog Fabric deployment:

```mermaid
graph TD

classDef gateway fill:#FFF2CC,stroke:#999,stroke-width:1px,color:#000
classDef spine   fill:#F8CECC,stroke:#B85450,stroke-width:1px,color:#000
classDef leaf    fill:#DAE8FC,stroke:#6C8EBF,stroke-width:1px,color:#000
classDef server  fill:#D5E8D4,stroke:#82B366,stroke-width:1px,color:#000
classDef mclag   fill:#F0F8FF,stroke:#6C8EBF,stroke-width:1px,color:#000

subgraph Gateways
	direction TB
	Gateway_1["gateway-1"]
end

subgraph Spines
	direction TB
	Spine_01["spine-01<br>spine"]
	Spine_02["spine-02<br>spine"]
end

subgraph Leaves [Leaves]
	direction LR
	Leaf_01["leaf-01<br>server-leaf"]
end

subgraph Servers
	direction TB
	Server_01["server-01"]
end

%% Gateways -> Spines
Gateway_1 ---|"enp2s1↔E1/2"| Spine_01
Gateway_1 ---|"enp2s2↔E1/2"| Spine_02

%% Spine_01 -> Leaves
Spine_01 ---|"E1/2↔E1/1"| Leaf_01

%% Spine_02 -> Leaves
Spine_02 ---|"E1/3↔E1/1"| Leaf_01

%% Leaves -> Servers
Leaf_01 ---|"enp2s1↔E1/1"| Server_01

class Gateway_1 gateway
class Spine_01,Spine_02 spine
class Leaf_01 leaf
class Server_01 server
class MCLAG mclag
linkStyle default stroke:#666,stroke-width:2px
linkStyle 0,1 stroke:#CC9900,stroke-dasharray: 2 2,stroke-width:2px,color:#CC9900 %% Gateway links
linkStyle 2,3 stroke:#CC3333,stroke-width:4px %% Fabric links
linkStyle 4 stroke:#999999,stroke-width:2px %% Unbundled server link
style Gateways fill:none,stroke:none
style Spines fill:none,stroke:none
style Leaves fill:none,stroke:none
style Servers fill:none,stroke:none
```

### Node Types & Scanning Approaches

| Node Type | Role | Container Runtime | Scanning Approach | Network Access |
|-----------|------|------------------|-------------------|----------------|
| **control-1** | Control plane | K3s/containerd | Online | Internet access |
| **gateway-1** | Border gateway | K3s/containerd | Airgapped | Restricted |
| **spine/leaf switches** | SONiC switches | Docker | SONiC Airgapped | Isolated |

## Why VLAB Instead of Image Scanning?

### Traditional Approach Problems:
- **Scattered images** across registries (ghcr.io, docker.io, 172.30.0.1:31000)
- **Missing runtime context** - no deployment-specific configurations
- **Version drift** - images in registry vs. actually deployed

### VLAB Advantages:
- **Realistic deployment** - actual running containers with real configurations
- **Registry consistency** - scans exactly what's deployed
- **CI integration** - uses existing VLAB infrastructure

## Trivy Scanning Approaches

The system uses three scanning modes based on network access constraints:

**Online Mode (control-1)**: Direct internet access for real-time vulnerability DB updates
```bash
trivy image --format sarif --output report.sarif <image>
```

**Airgapped Mode (gateway-1, switches)**: Pre-cached vulnerability database, offline scanning
```bash
# Gateway: K3s containers
trivy image --offline-scan --format sarif <image>

# Switches: Docker containers exported to tar
docker save <image> | trivy image --input /dev/stdin --format sarif
```

**Output formats**: SARIF (GitHub integration), JSON (programmatic), TXT (human-readable)

### SONiC Switch Load Balancing

SONiC switches scans are load balanced across available VMs to prevent SSH session timeouts during long scans. Images are distributed across available switches for parallel processing, reducing individual session times from 45+ minutes to ~15 minutes per switch.

## Scan Outputs & Processing

### Output Formats
Each scan produces multiple output formats:
- **SARIF files** - For GitHub Security integration and automated processing
- **JSON files** - For programmatic analysis and custom tooling
- **TXT files** - Human-readable tabular reports for manual review

### Scan Results Structure
```
trivy-reports/
├── control-1/
│   ├── container_images.txt                    # List of scanned images
│   ├── fabric_v0.87.0_critical.sarif          # SARIF format (GitHub)
│   ├── fabric_v0.87.0_critical.json           # JSON format (programmatic)
│   ├── fabric_v0.87.0_critical.txt            # TXT format (human-readable)
│   └── ...
└── gateway-1/
    ├── container_images.txt
    └── ...
```

### Raw SARIF Files
The SARIF consolidator processes files from:
```
raw-sarif-reports/
├── control-1/
│   ├── 20250729-120000_fabric_v0.87.0_critical.sarif
│   ├── 20250729-120000_zot_v2.1.1_critical.sarif
│   └── ...
├── gateway-1/
│   ├── 20250729-120100_klipper-helm_critical.sarif
│   └── ...
└── sonic-switches/
    ├── 20250729-120200_bgp_container_critical.sarif
    └── ...
```

### Deduplication Challenge
**Problem**: Same images deployed from different registries create duplicate vulnerabilities:
```bash
# Same container, different registries = duplicate reports
172.30.0.1:31000/githedgehog/fabricator/zot:v2.1.1  # 13 vulnerabilities
ghcr.io/githedgehog/fabricator/zot:v2.1.1           # 13 vulnerabilities (DUPLICATES!)
```

**Solution**: Use `imageID` (SHA256) for logical deduplication:
```bash
# Both have same imageID = same binary content = merge reports
imageID: sha256:b65f0e9f2e5dc7518c4bfae2649e681e7224915a756d014d8ca83cd1154c9df9
```

### Consolidation Process
```bash
hack/sarif-consolidator.sh
├── 1. Group SARIF files by VM
├── 2. Map SARIF files to container images
├── 3. Deduplicate by imageID within each VM
├── 4. Merge vulnerabilities (unique by ruleId + location)
├── 5. Add VM context to vulnerability reports
└── 6. Generate final consolidated SARIF
```

## Image Mapping Logic

### The Challenge
SARIF files contain container references that must be mapped back to the deployed container images:
- **Online mode**: `imageName` contains direct registry paths (`docker.io/rancher/klipper-helm:v0.9.7`)
- **Airgapped mode**: `imageName` contains tar file paths (`/tmp/trivy-export-$/docker.io_rancher_klipper-helm_v0.9.7.tar`)

### Mapping Process

#### 1. Extract Image Metadata
```bash
# From each SARIF file, extract:
imageName=$(jq -r '.runs[0].properties.imageName' file.sarif)
imageID=$(jq -r '.runs[0].properties.imageID' file.sarif)  # SHA256 hash
```

#### 2. Handle Airgapped Reconstruction
For tar file paths, reconstruct the original image name:
```bash
# /tmp/trivy-export-$/docker.io_rancher_klipper-helm_v0.9.7.tar
# becomes: docker.io/rancher/klipper-helm:v0.9.7
```

#### 3. Validate Against Container List
Each VM has a `container_images.txt` file listing actually deployed images:
```bash
172.30.0.1:31000/githedgehog/fabric/fabric-boot:v0.84.3
docker.io/rancher/klipper-helm:v0.9.7-build20250616
...
```

#### 4. Deduplication by ImageID
Group SARIF files by `imageID` (SHA256) within each VM:
```bash
# Same imageID = same binary content = merge vulnerabilities
172.30.0.1:31000/githedgehog/fabricator/zot:v2.1.1  # imageID: sha256:b65f0e9f...
ghcr.io/githedgehog/fabricator/zot:v2.1.1           # imageID: sha256:b65f0e9f... (SAME)
# Result: Single vulnerability report using first image as representative
```

#### 5. VM Context Preservation
Each vulnerability retains its deployment context:
```json
{
  "message": "[control-1/zot:v2.1.1] Critical vulnerability in libssl",
  "properties": {
    "vmName": "control-1",
    "containerWithVersion": "zot:v2.1.1",
    "sourceImage": "172.30.0.1:31000/githedgehog/fabricator/zot:v2.1.1"
  }
}
```

## Output Files & Artifacts

### Directory Structure
```
# Raw scan results per VM
trivy-reports/
├── control-1/
│   ├── container_images.txt                    # List of scanned images
│   ├── fabric_v0.87.0_critical.sarif          # SARIF format (GitHub)
│   ├── fabric_v0.87.0_critical.json           # JSON format (programmatic)
│   ├── fabric_v0.87.0_critical.txt            # TXT format (human-readable)
│   └── ...
├── gateway-1/
│   ├── container_images.txt
│   └── ...
└── sonic-switches/
    ├── container_images.txt
    └── ...

# Raw SARIF files (input to consolidator)
raw-sarif-reports/
├── control-1/
│   ├── 20250729-120000_fabric_v0.87.0_critical.sarif
│   ├── 20250729-120000_zot_v2.1.1_critical.sarif
│   └── ...
├── gateway-1/
│   └── ...
└── sonic-switches/
    └── ...

# Consolidated reports (GitHub integration)
sarif-reports/
├── trivy-consolidated-control-1.sarif          # Per-VM consolidated
├── trivy-consolidated-gateway-1.sarif
├── trivy-consolidated-sonic-switches.sarif
└── trivy-security-scan.sarif                   # Final GitHub upload
```

### File Descriptions

**container_images.txt**: Authoritative list of deployed images per VM
```
172.30.0.1:31000/githedgehog/fabric/fabric-boot:v0.84.3
172.30.0.1:31000/githedgehog/fabric/fabric:v0.84.3
docker.io/rancher/klipper-helm:v0.9.7-build20250616
```

**Individual SARIF files**: Raw scan results per container
- Contains vulnerability details, imageID, imageName
- Used for mapping and deduplication logic

**Consolidated SARIF files**: Processed per-VM reports
- Deduplicated vulnerabilities within each VM
- VM context added to each vulnerability
- Clean artifact paths (vm-name/container:version/binary)

**Final SARIF report**: Single file for GitHub Security tab
- All VMs merged into one report
- Preserves VM context in vulnerability messages
- Enables centralized security dashboard

## GitHub Security Integration

The consolidated SARIF enables:
- **Centralized vulnerability dashboard**
- **Automated security alerts**
- **Issue tracking** with affected files/containers
- **Historical trending** of vulnerability counts
- **Integration** with pull request checks

### VM Context Preservation
Each vulnerability report includes:
```json
{
  "ruleId": "CVE-2023-1234",
  "message": "[control-1/zot:v2.1.1] Critical vulnerability in libssl",
  "properties": {
    "vmName": "control-1",
    "vmType": "control",
    "containerName": "zot",
    "scanContext": "runtime-deployment-online"
  }
}
```

## Workflow Options

### Manual Dispatch Options
```yaml
# .github/workflows/security-scan.yml
workflow_dispatch:
  inputs:
    scan_scope:
      description: 'Select components to scan'
      required: true
      default: 'control-gateway'
      type: choice
      options:
        - 'control-gateway'
        - 'control-only'
        - 'gateway-only'
        - 'switch-only'
        - 'all'
```

## Getting Started

### Run a Basic Scan
```bash
# Scan core infrastructure (recommended for development)
./hack/vlab-trivy-runner.sh --control-only --gateway-only

# Scan everything (CI/comprehensive security review
./hack/vlab-trivy-runner.sh --all

# Use existing VLAB (if already running)
./hack/vlab-trivy-runner.sh --skip-vlab --control-only
```

### Understanding the Output
```bash
# View consolidated results
ls sarif-reports/
cat sarif-reports/trivy-security-scan.sarif | jq '.runs[0].results | length'

# Check specific VM vulnerabilitie
cat sarif-reports/trivy-consolidated-control-1.sarif | \
  jq '.runs[0].results[] | select(.level=="error") | .message.text'
```

### GitHub Integration
The system automatically:
1. **Uploads SARIF** to GitHub Security tab
2. **Sets environment variables** for downstream jobs
3. **Generates PR summaries** with vulnerability counts
