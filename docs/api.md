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



! WARNING: Make sure to update the IsReady/IsGatewayReady methods if you add or remove components



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
| `controlProxy` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `controlAlloy` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `gatewayAlloy` _[ComponentStatus](#componentstatus)_ |  |  |  |
| `gatewayDataplane` _object (keys:string, values:[ComponentStatus](#componentstatus))_ |  |  |  |
| `gatewayFRR` _object (keys:string, values:[ComponentStatus](#componentstatus))_ |  |  |  |


#### ControlConfig







_Appears in:_
- [FabConfig](#fabconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `managementSubnet` _Prefix_ |  |  |  |
| `controlVIP` _Prefix_ |  |  |  |
| `managementSubnetAnyDevice` _boolean_ |  |  |  |
| `managementSubnetStatic` _object (keys:string, values:string)_ |  |  |  |
| `tlsSAN` _string array_ |  |  |  |
| `joinToken` _string_ |  |  |  |
| `kubeClusterSubnet` _Prefix_ |  |  |  |
| `kubeServiceSubnet` _Prefix_ |  |  |  |
| `kubeClusterDNS` _Addr_ |  |  |  |
| `dummySubnet` _Prefix_ |  |  |  |
| `defaultUser` _[ControlUser](#controluser)_ |  |  |  |
| `ntpServers` _string array_ |  |  |  |
| `observability` _[ControlObservability](#controlobservability)_ |  |  |  |


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
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
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
| `ip` _Prefix_ |  |  |  |


#### ControlNodeExternal







_Appears in:_
- [ControlNodeSpec](#controlnodespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ip` _PrefixOrDHCP_ |  |  |  |
| `gateway` _Addr_ |  |  |  |
| `dns` _Addr array_ |  |  |  |
| `interface` _string_ |  |  |  |


#### ControlNodeManagement







_Appears in:_
- [ControlNodeSpec](#controlnodespec)
- [FabNodeSpec](#fabnodespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ip` _Prefix_ |  |  |  |
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



#### ControlObservability







_Appears in:_
- [ControlConfig](#controlconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `kubePodLogs` _boolean_ |  |  |  |
| `kubeEvents` _boolean_ |  |  |  |


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
| `observability` _[ObservabilityConfig](#observabilityconfig)_ |  |  |  |


#### FabNode



FabNode is the Schema for the nodes API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `fabricator.githedgehog.com/v1beta1` | | |
| `kind` _string_ | `FabNode` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
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
| `mode` _FabricMode_ |  |  |  |
| `managementDHCPStart` _Addr_ |  |  |  |
| `managementDHCPEnd` _Addr_ |  |  |  |
| `spineASN` _integer_ |  |  |  |
| `leafASNStart` _integer_ |  |  |  |
| `leafASNEnd` _integer_ |  |  |  |
| `protocolSubnet` _Prefix_ |  |  |  |
| `vtepSubnet` _Prefix_ |  |  |  |
| `fabricSubnet` _Prefix_ |  |  |  |
| `proxyExternalSubnet` _Prefix_ |  |  |  |
| `baseVPCCommunity` _string_ |  |  |  |
| `vpcIRBVLANs` _VLANRange array_ |  |  |  |
| `loopbackWorkaroundDisable` _boolean_ |  |  |  |
| `vpcWorkaroundVLANs` _VLANRange array_ |  |  |  |
| `vpcWorkaroundSubnet` _Prefix_ |  |  |  |
| `th5WorkaroundVLANs` _VLANRange array_ |  |  |  |
| `eslagMACBase` _string_ |  |  |  |
| `eslagESIPrefix` _string_ |  |  |  |
| `mclagSessionSubnet` _Prefix_ |  |  |  |
| `defaultSwitchUsers` _object (keys:string, values:[SwitchUser](#switchuser))_ |  |  |  |
| `defaultAlloyConfig` _AlloyConfig_ |  |  |  |
| `excludeNOSInstallers` _boolean_ |  |  |  |
| `includeONIE` _boolean_ |  |  |  |
| `includeBCM` _boolean_ |  |  |  |
| `includeCLSP` _boolean_ |  |  |  |
| `includeCumulus` _boolean_ |  |  |  |
| `disableBFD` _boolean_ |  |  |  |
| `observability` _Observability_ |  |  |  |


#### FabricVersions







_Appears in:_
- [Versions](#versions)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `api` _Version_ |  |  |  |
| `controller` _Version_ |  |  |  |
| `dhcpd` _Version_ |  |  |  |
| `boot` _Version_ |  |  |  |
| `agent` _Version_ |  |  |  |
| `ctl` _Version_ |  |  |  |
| `nos` _object (keys:NOSType, values:Version)_ |  |  |  |
| `onie` _object (keys:string, values:Version)_ |  |  |  |


#### Fabricator



Fabricator defines configuration for the Fabricator controller





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `fabricator.githedgehog.com/v1beta1` | | |
| `kind` _string_ | `Fabricator` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
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
| `lastAttemptTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | Time of the last attempt to apply configuration |  |  |
| `lastAttemptGen` _integer_ | Generation of the last attempt to apply configuration |  |  |
| `lastAppliedTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | Time of the last successful configuration application |  |  |
| `lastAppliedGen` _integer_ | Generation of the last successful configuration application |  |  |
| `lastAppliedController` _string_ | Controller version that applied the last successful configuration |  |  |
| `lastStatusCheck` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | Time of the last status check |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions of the fabricator, includes readiness marker for use with kubectl wait |  |  |
| `components` _[ComponentsStatus](#componentsstatus)_ |  |  |  |
| `release` _string_ |  |  |  |
| `releaseChannel` _string_ |  |  |  |


#### FabricatorVersions







_Appears in:_
- [Versions](#versions)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `api` _Version_ |  |  |  |
| `controller` _Version_ |  |  |  |
| `controlISORoot` _Version_ |  |  |  |
| `ctl` _Version_ |  |  |  |
| `nodeConfig` _Version_ |  |  |  |
| `pause` _Version_ |  |  |  |
| `flatcar` _Version_ |  |  |  |


#### GatewayConfig







_Appears in:_
- [FabConfig](#fabconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `enable` _boolean_ |  |  |  |
| `asn` _integer_ |  |  |  |
| `mac` _string_ |  |  |  |
| `observability` _[GatewayObservability](#gatewayobservability)_ |  |  |  |
| `communities` _object (keys:string, values:string)_ |  |  |  |


#### GatewayObservability







_Appears in:_
- [GatewayConfig](#gatewayconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `dataplane` _[GatewayObservabilityDataplane](#gatewayobservabilitydataplane)_ |  |  |  |
| `frr` _[GatewayObservabilityFRR](#gatewayobservabilityfrr)_ |  |  |  |
| `unix` _[GatewayObservabilityUnix](#gatewayobservabilityunix)_ |  |  |  |


#### GatewayObservabilityDataplane







_Appears in:_
- [GatewayObservability](#gatewayobservability)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `metrics` _boolean_ |  |  |  |
| `metricsInterval` _integer_ |  |  |  |
| `metricsRelabel` _ScrapeRelabelRule array_ |  |  |  |


#### GatewayObservabilityFRR







_Appears in:_
- [GatewayObservability](#gatewayobservability)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `metrics` _boolean_ |  |  |  |
| `metricsInterval` _integer_ |  |  |  |
| `metricsRelabel` _ScrapeRelabelRule array_ |  |  |  |


#### GatewayObservabilityUnix







_Appears in:_
- [GatewayObservability](#gatewayobservability)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `metrics` _boolean_ |  |  |  |
| `metricsInterval` _integer_ |  |  |  |
| `metricsRelabel` _ScrapeRelabelRule array_ |  |  |  |
| `metricsCollectors` _string array_ |  |  |  |


#### GatewayVersions







_Appears in:_
- [Versions](#versions)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `api` _Version_ |  |  |  |
| `controller` _Version_ |  |  |  |
| `agent` _Version_ |  |  |  |
| `dataplane` _Version_ |  |  |  |
| `frr` _Version_ |  |  |  |


#### ObservabilityConfig







_Appears in:_
- [FabConfig](#fabconfig)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `defaults` _[ObservabilityDefaults](#observabilitydefaults)_ |  |  |  |
| `labels` _object (keys:string, values:string)_ |  |  |  |
| `targets` _Targets_ |  |  |  |


#### ObservabilityDefaults

_Underlying type:_ _string_





_Appears in:_
- [ObservabilityConfig](#observabilityconfig)

| Field | Description |
| --- | --- |
| `` |  |
| `none` |  |
| `minimal` |  |


#### PlatformVersions







_Appears in:_
- [Versions](#versions)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `k3s` _Version_ |  |  |  |
| `zot` _Version_ |  |  |  |
| `zotChart` _Version_ |  |  |  |
| `certManager` _Version_ |  |  |  |
| `k9s` _Version_ |  |  |  |
| `toolbox` _Version_ |  |  |  |
| `reloader` _Version_ |  |  |  |
| `reloaderChart` _Version_ |  |  |  |
| `ntp` _Version_ |  |  |  |
| `ntpChart` _Version_ |  |  |  |
| `alloy` _Version_ |  |  |  |
| `controlProxy` _Version_ |  |  |  |
| `controlProxyChart` _Version_ |  |  |  |
| `bashCompletion` _Version_ |  |  |  |
| `hostBGPContainer` _Version_ |  |  |  |


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
| `onie` _Version_ |  |  |  |
| `flatcar` _Version_ |  |  |  |


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


