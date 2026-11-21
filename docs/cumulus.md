# Cumulus

## Running Cumulus in VLAB

Cumulus switches are provisioned in a two-tier Clos topology and configured the same way as SONiC ones: VPC subnets are
VLANs on the server-facing ports and bonds, mapped to the subnet VNIs with an anycast gateway, and each VPC gets its own
VRF with the VPC VNI as L3VNI.

Current features:
- L2VNI VPCs with multiple subnets, tagged and native VLAN attachments on unbundled, bundled and ESLAG connections
- DHCP relay for VPC subnets to the fabric DHCP server on the control node
- VPC peerings incl. subnet permits, isolated and restricted subnets, VPC permits and static routes
- Gateway peerings, incl. the gateway priority failover
- Static and BGP externals with external peerings
- BFD for spine-leaf connections
- RoCE mode enabled for lossless traffic, ECN, PFC and Adaptive Routing
- ZTP over DHCP for existing and new switches provisioning
- Agent self-upgrades

Known limitations:
- Only two-tier Clos topology, no mesh
- No L3VNI VPCs, host BGP subnets or DHCP relay to other servers
- No proxy-ARP static externals, static external connections or BGP prefix limits for externals
- No ports/bgp stats collection

### Setting up VLAB

Only difference is to enable Cumulus support, use Cumulus VX switch profiles and generate compatible wiring:

```
# enable cumulus support
hhfab init --dev --include-cumulus -f

# 2 spines, an ESLAG leaf pair and an orphan leaf with the default set of servers
hhfab vlab gen --sp=cumulus-vx --eslag-leaf-groups=2 --orphan-leafs-count=1

# starting VLAB as usual
hhfab vlab up
```

And now we can use usual tools to setup VPCs, peerings and test connectivity:

```
# create VPCs with the servers attached to them
hhfab vlab setup-vpcs

# peer the first two VPCs
hhfab vlab setup-peerings 1+2

# test connectivity as usual
hhfab vlab test-connectivity
```
