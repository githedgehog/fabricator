# Cumulus

## Running experimental Cumulus support in VLAB

Current implementation is limited and demo/experimental only, though it's totally suitable to demonstrate provisioning
of the Cumulus switches in a two-tier Clos topology. Multiple VPCs could be configured and traffic would work across
different leaf switches within a single VPC.

Current features:
- L3-only EVPN fabric with point-to-point server connections implementing NVIDIA Deployment Guide
- BFD for spine-leaf connections
- RoCE mode enabled for lossless traffic
- ECN, PFC enabled
- Adaptive Routing is enabled and setup
- ZTP over DHCP for existing and new switches provisioning
- Agent self-upgrades

Known limitations:
- Only two-tier Clos topology
- No ports/bgp stats collection
- No gateway support (though could be done very soon)
- Only VPCs with a single subnet from 10.0.0.0/8 are supported
- VPC Subnet VLANs are ignored (but still required)
- Only single VPCAttachment per switch port is supported due to the switch front panel ports being directly added to
  the VPC VRF
- Only Unbundled (single port) connections are supported
- No VPCPeering on switches supported
- Only p2p (point-to-point) VPCAttachments are supported

### Setting up VLAB

Only difference is to enable Cumulus support, use Cumulus VX switch profiles and generate compatible wiring:

```
# enable cumulus support
hhfab init --dev --include-cumulus -f

# simple 2 spine, 2 leaf topology with a pair of servers attached to each leaf
hhfab vlab gen --mclag-leafs-count=0 --eslag-leaf-groups="" --orphan-leafs-count=2 --bundled-servers=0 --unbundled-servers=2 --sp=cumulus-vx

# peerings aren't supported yet

# starting VLAB as usual
hhfab vlab up
```

And now we can use usual tools to setup VPCs and test connectivity:

```
# create single VPC with single subnet and 10 servers in it, p2p mode would be automatically enabled
hhfab vlab setup-vpcs --subnets=1 --servers=10

# test connectivity as usual
hhfab vlab test-connectivity
```

In this setup, `server-01` is attached to `leaf-01` via a p2p link and has `10.0.1.0/31` IP address, while
`server-03` is attached to `leaf-02` via a p2p link and has `10.0.1.4/31` IP address. You can ping and run iperf3
between these servers to test connectivity.

Note: all servers are connected via p2p links and have even IPs from /31 while switches have odd ones, but default
route is automatically configured on servers as well to be able to access other servers from the same VPC (and
other after peering is supported).
