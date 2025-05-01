# API Reference

## Packages
- [fabricator.githedgehog.com/v1beta1](#fabricatorgithedgehogcomv1beta1)


## fabricator.githedgehog.com/v1beta1

Package v1beta1 contains API Schema definitions for the fabricator v1beta1 API group

### Resource Types
- [ControlNode](#controlnode)
- [FabNode](#fabnode)
- [Fabricator](#fabricator)



#### ComponentStatus

_Underlying type:_ _string_





_Appears in:_
- [ComponentsStatus](#componentsstatus)

| Field | Description |
| --- | --- |
| `` |  |
| `NotFound` |  |
| `Pending` |  |
| `Ready` |  |
| `Skipped` |  |


#### ComponentsStatus



! WARNING: Make sure to update the IsReady method if you add or remove components



_Appears in:_
- [FabricatorStatus](#fabricatorstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `fabricatorAPI` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `fabricatorCtrl` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `fabricatorNodeConfig` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `certManagerCtrl` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `certManagerWebhook` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `reloader` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `zot` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `ntp` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `fabricAPI` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `fabricCtrl` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `fabricBoot` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `fabricDHCP` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `fabricProxy` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `gatewayAPI` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `gatewayCtrl` _[ComponentStatus](#componentstatus)_ |  |  |  |


#### ControlConfig







_Appears in:_
- [FabConfig](#fabconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `managementSubnet` _[Prefix](#prefix)_ |  |  |  |
| `controlVIP` _[Prefix](#prefix)_ |  |  |  |
| `tlsSAN` _string array_ |  |  |  |
| `joinToken` _string_ |  |  |  |
| `kubeClusterSubnet` _[Prefix](#prefix)_ |  |  |  |
| `kubeServiceSubnet` _[Prefix](#prefix)_ |  |  |  |
| `kubeClusterDNS` _[Addr](#addr)_ |  |  |  |
| `dummySubnet` _[Prefix](#prefix)_ |  |  |  |
| `defaultUser` _[ControlUser](#controluser)_ |  |  |  |
| `ntpServers` _string array_ |  |  |  |


#### ControlConfigRegistryUpstream







_Appears in:_
- [RegistryConfig](#registryconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `repo` _string_ |  |  |  |
| `prefix` _string_ |  |  |  |
| `noTLSVerify` _boolean_ |  |  |  |
| `username` _string_ |  |  |  |
| `password` _string_ |  |  |  |


#### ControlNode









| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `fabricator.githedgehog.com/v1beta1` | | |
| `kind` _string_ | `ControlNode` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[ControlNodeSpec](#controlnodespec)_ |  |  |  |
| `status` _[ControlNodeStatus](#controlnodestatus)_ |  |  |  |


#### ControlNodeBootstrap







_Appears in:_
- [ControlNodeSpec](#controlnodespec)
- [FabNodeSpec](#fabnodespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `disk` _string_ |  |  |  |


#### ControlNodeDummy







_Appears in:_
- [ControlNodeSpec](#controlnodespec)
- [FabNodeSpec](#fabnodespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ip` _[Prefix](#prefix)_ |  |  |  |


#### ControlNodeExternal







_Appears in:_
- [ControlNodeSpec](#controlnodespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ip` _[PrefixOrDHCP](#prefixordhcp)_ |  |  |  |
| `gateway` _[Addr](#addr)_ |  |  |  |
| `dns` _[Addr](#addr) array_ |  |  |  |
| `interface` _string_ |  |  |  |


#### ControlNodeManagement







_Appears in:_
- [ControlNodeSpec](#controlnodespec)
- [FabNodeSpec](#fabnodespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ip` _[Prefix](#prefix)_ |  |  |  |
| `interface` _string_ |  |  |  |


#### ControlNodeSpec







_Appears in:_
- [ControlNode](#controlnode)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `bootstrap` _[ControlNodeBootstrap](#controlnodebootstrap)_ |  |  |  |
| `management` _[ControlNodeManagement](#controlnodemanagement)_ |  |  |  |
| `external` _[ControlNodeExternal](#controlnodeexternal)_ |  |  |  |
| `dummy` _[ControlNodeDummy](#controlnodedummy)_ |  |  |  |


#### ControlNodeStatus







_Appears in:_
- [ControlNode](#controlnode)



#### ControlUser







_Appears in:_
- [ControlConfig](#controlconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `password` _string_ |  |  |  |
| `authorizedKeys` _string array_ |  |  |  |


#### FabConfig







_Appears in:_
- [FabricatorSpec](#fabricatorspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `control` _[ControlConfig](#controlconfig)_ |  |  |  |
| `registry` _[RegistryConfig](#registryconfig)_ |  |  |  |
| `fabric` _[FabricConfig](#fabricconfig)_ |  |  |  |
| `gateway` _[GatewayConfig](#gatewayconfig)_ |  |  |  |


#### FabNode



FabNode is the Schema for the nodes API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `fabricator.githedgehog.com/v1beta1` | | |
| `kind` _string_ | `FabNode` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[FabNodeSpec](#fabnodespec)_ |  |  |  |
| `status` _[FabNodeStatus](#fabnodestatus)_ |  |  |  |


#### FabNodeRole

_Underlying type:_ _string_





_Appears in:_
- [FabNodeSpec](#fabnodespec)

| Field | Description |
| --- | --- |
| `gateway` |  |


#### FabNodeSpec



FabNodeSpec defines the desired state of FabNode.



_Appears in:_
- [FabNode](#fabnode)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `roles` _[FabNodeRole](#fabnoderole) array_ |  |  |  |
| `bootstrap` _[ControlNodeBootstrap](#controlnodebootstrap)_ |  |  |  |
| `management` _[ControlNodeManagement](#controlnodemanagement)_ |  |  |  |
| `dummy` _[ControlNodeDummy](#controlnodedummy)_ |  |  |  |


#### FabNodeStatus



FabNodeStatus defines the observed state of Node.



_Appears in:_
- [FabNode](#fabnode)



#### FabOverrides







_Appears in:_
- [FabricatorSpec](#fabricatorspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `versions` _[Versions](#versions)_ |  |  |  |


#### FabricConfig







_Appears in:_
- [FabConfig](#fabconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _[FabricMode](#fabricmode)_ |  |  |  |
| `managementDHCPStart` _[Addr](#addr)_ |  |  |  |
| `managementDHCPEnd` _[Addr](#addr)_ |  |  |  |
| `spineASN` _integer_ |  |  |  |
| `leafASNStart` _integer_ |  |  |  |
| `leafASNEnd` _integer_ |  |  |  |
| `protocolSubnet` _[Prefix](#prefix)_ |  |  |  |
| `vtepSubnet` _[Prefix](#prefix)_ |  |  |  |
| `fabricSubnet` _[Prefix](#prefix)_ |  |  |  |
| `baseVPCCommunity` _string_ |  |  |  |
| `vpcIRBVLANs` _VLANRange array_ |  |  |  |
| `vpcWorkaroundVLANs` _VLANRange array_ |  |  |  |
| `vpcWorkaroundSubnet` _[Prefix](#prefix)_ |  |  |  |
| `eslagMACBase` _string_ |  |  |  |
| `eslagESIPrefix` _string_ |  |  |  |
| `mclagSessionSubnet` _[Prefix](#prefix)_ |  |  |  |
| `defaultSwitchUsers` _object (keys:string, values:[SwitchUser](#switchuser))_ |  |  |  |
| `defaultAlloyConfig` _[AlloyConfig](#alloyconfig)_ |  |  |  |
| `includeONIE` _boolean_ |  |  |  |


#### FabricVersions







_Appears in:_
- [Versions](#versions)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `api` _[Version](#version)_ |  |  |  |
| `controller` _[Version](#version)_ |  |  |  |
| `dhcpd` _[Version](#version)_ |  |  |  |
| `boot` _[Version](#version)_ |  |  |  |
| `agent` _[Version](#version)_ |  |  |  |
| `ctl` _[Version](#version)_ |  |  |  |
| `alloy` _[Version](#version)_ |  |  |  |
| `proxyChart` _[Version](#version)_ |  |  |  |
| `proxy` _[Version](#version)_ |  |  |  |
| `nos` _object (keys:string, values:[Version](#version))_ |  |  |  |
| `onie` _object (keys:string, values:[Version](#version))_ |  |  |  |


#### Fabricator



Fabricator defines configuration for the Fabricator controller





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `fabricator.githedgehog.com/v1beta1` | | |
| `kind` _string_ | `Fabricator` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[FabricatorSpec](#fabricatorspec)_ |  |  |  |
| `status` _[FabricatorStatus](#fabricatorstatus)_ |  |  |  |


#### FabricatorSpec







_Appears in:_
- [Fabricator](#fabricator)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `config` _[FabConfig](#fabconfig)_ |  |  |  |
| `overrides` _[FabOverrides](#faboverrides)_ |  |  |  |


#### FabricatorStatus







_Appears in:_
- [Fabricator](#fabricator)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `isBootstrap` _boolean_ |  |  |  |
| `isInstall` _boolean_ |  |  |  |
| `versions` _[Versions](#versions)_ |  |  |  |
| `lastAttemptTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#time-v1-meta)_ | Time of the last attempt to apply configuration |  |  |
| `lastAttemptGen` _integer_ | Generation of the last attempt to apply configuration |  |  |
| `lastAppliedTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#time-v1-meta)_ | Time of the last successful configuration application |  |  |
| `lastAppliedGen` _integer_ | Generation of the last successful configuration application |  |  |
| `lastAppliedController` _string_ | Controller version that applied the last successful configuration |  |  |
| `lastStatusCheck` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#time-v1-meta)_ | Time of the last status check |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.31/#condition-v1-meta) array_ | Conditions of the fabricator, includes readiness marker for use with kubectl wait |  |  |
| `components` _[ComponentsStatus](#componentsstatus)_ |  |  |  |


#### FabricatorVersions







_Appears in:_
- [Versions](#versions)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `api` _[Version](#version)_ |  |  |  |
| `controller` _[Version](#version)_ |  |  |  |
| `controlISORoot` _[Version](#version)_ |  |  |  |
| `ctl` _[Version](#version)_ |  |  |  |
| `nodeConfig` _[Version](#version)_ |  |  |  |
| `pause` _[Version](#version)_ |  |  |  |
| `flatcar` _[Version](#version)_ |  |  |  |


#### GatewayConfig







_Appears in:_
- [FabConfig](#fabconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enable` _boolean_ |  |  |  |
| `asn` _integer_ |  |  |  |


#### GatewayVersions







_Appears in:_
- [Versions](#versions)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `api` _[Version](#version)_ |  |  |  |
| `controller` _[Version](#version)_ |  |  |  |
| `agent` _[Version](#version)_ |  |  |  |
| `dataplane` _[Version](#version)_ |  |  |  |
| `frr` _[Version](#version)_ |  |  |  |


#### PlatformVersions







_Appears in:_
- [Versions](#versions)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `k3s` _[Version](#version)_ |  |  |  |
| `zot` _[Version](#version)_ |  |  |  |
| `certManager` _[Version](#version)_ |  |  |  |
| `k9s` _[Version](#version)_ |  |  |  |
| `toolbox` _[Version](#version)_ |  |  |  |
| `reloader` _[Version](#version)_ |  |  |  |
| `ntp` _[Version](#version)_ |  |  |  |
| `ntpChart` _[Version](#version)_ |  |  |  |


#### RegistryConfig







_Appears in:_
- [FabConfig](#fabconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `mode` _[RegistryMode](#registrymode)_ |  |  |  |
| `upstream` _[ControlConfigRegistryUpstream](#controlconfigregistryupstream)_ |  |  |  |


#### RegistryMode

_Underlying type:_ _string_





_Appears in:_
- [RegistryConfig](#registryconfig)

| Field | Description |
| --- | --- |
| `airgap` |  |
| `upstream` |  |


#### SwitchUser







_Appears in:_
- [FabricConfig](#fabricconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `password` _string_ |  |  |  |
| `role` _string_ |  |  |  |
| `authorizedKeys` _string array_ |  |  |  |


#### VLABVersions







_Appears in:_
- [Versions](#versions)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `onie` _[Version](#version)_ |  |  |  |
| `flatcar` _[Version](#version)_ |  |  |  |


#### Versions







_Appears in:_
- [FabOverrides](#faboverrides)
- [FabricatorStatus](#fabricatorstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `platform` _[PlatformVersions](#platformversions)_ |  |  |  |
| `fabricator` _[FabricatorVersions](#fabricatorversions)_ |  |  |  |
| `fabric` _[FabricVersions](#fabricversions)_ |  |  |  |
| `gateway` _[GatewayVersions](#gatewayversions)_ |  |  |  |
| `vlab` _[VLABVersions](#vlabversions)_ |  |  |  |


