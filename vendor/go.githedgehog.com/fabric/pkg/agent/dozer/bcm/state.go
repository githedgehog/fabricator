// Copyright 2023 Hedgehog
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bcm

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/openconfig/gnmic/api"
	"github.com/pkg/errors"
	"go.githedgehog.com/fabric-bcm-ygot/pkg/oc"
	agentapi "go.githedgehog.com/fabric/api/agent/v1beta1"
	"go.githedgehog.com/fabric/pkg/agent/switchstate"
	"go.githedgehog.com/fabric/pkg/ctrl/switchprofile"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	fanIgnore         = ""
	psuIgnore         = "None"
	temperatureIgnore = "N/A"
)

func (p *BroadcomProcessor) UpdateSwitchState(ctx context.Context, agent *agentapi.Agent, reg *switchstate.Registry) error {
	start := time.Now()

	swState := &agentapi.SwitchState{
		Interfaces:   map[string]agentapi.SwitchStateInterface{},
		Breakouts:    map[string]agentapi.SwitchStateBreakout{},
		Transceivers: map[string]agentapi.SwitchStateTransceiver{},
		BGPNeighbors: map[string]map[string]agentapi.SwitchStateBGPNeighbor{},
		Platform: agentapi.SwitchStatePlatform{
			Fans:         map[string]agentapi.SwitchStatePlatformFan{},
			PSUs:         map[string]agentapi.SwitchStatePlatformPSU{},
			Temperatures: map[string]agentapi.SwitchStatePlatformTemperature{},
		},
		Firmware: map[string]string{},
	}

	if agent.Spec.SwitchProfile == nil {
		return errors.Errorf("switch profile not found")
	}

	portMap, err := agent.Spec.SwitchProfile.GetNOS2APIPortsFor(&agent.Spec.Switch)
	if err != nil {
		return errors.Wrapf(err, "failed to get port mapping")
	}

	roce, err := p.GetRoCE(ctx)
	if err != nil {
		return errors.Wrapf(err, "failed to get roce")
	}
	swState.RoCE = roce

	if err := p.updateInterfaceMetrics(ctx, reg, swState, agent, portMap); err != nil {
		return errors.Wrapf(err, "failed to update interface metrics")
	}

	if err := p.updateInterfaceQueuesMetrics(ctx, reg, swState, agent, portMap); err != nil {
		return errors.Wrapf(err, "failed to update interface queues metrics")
	}

	if err := p.updateTransceiverMetrics(ctx, reg, swState, portMap); err != nil {
		return errors.Wrapf(err, "failed to update transceiver metrics")
	}

	if err := p.updateCMISMetrics(ctx, agent, swState, portMap); err != nil {
		return errors.Wrapf(err, "failed to update cmis metrics")
	}

	if err := p.updateBreakoutMetrics(ctx, reg, swState, agent, portMap); err != nil {
		return errors.Wrapf(err, "failed to update breakout metrics")
	}

	if err := p.updateLLDPNeighbors(ctx, swState, portMap); err != nil {
		return errors.Wrapf(err, "failed to update lldp neighbors")
	}

	if err := p.updateBGPNeighborMetrics(ctx, reg, swState); err != nil {
		return errors.Wrapf(err, "failed to update bgp neighbor metrics")
	}

	if err := p.updatePlatformMetrics(ctx, agent, reg, swState); err != nil {
		return errors.Wrapf(err, "failed to update platform metrics")
	}

	if err := p.updateComponentMetrics(ctx, reg, swState, portMap); err != nil {
		return errors.Wrapf(err, "failed to update component metrics")
	}

	if err := p.updateCRMMetrics(ctx, reg, swState); err != nil {
		return errors.Wrapf(err, "failed to update crm metrics")
	}

	reg.SaveSwitchState(swState)

	slog.Debug("Switch state updated", "took", time.Since(start))

	return nil
}

func (p *BroadcomProcessor) updateInterfaceMetrics(ctx context.Context, reg *switchstate.Registry, swState *agentapi.SwitchState, ag *agentapi.Agent, portMap map[string]string) error {
	ifaces := &oc.OpenconfigInterfaces_Interfaces{}
	err := p.client.Get(ctx, "/interfaces/interface", ifaces)
	if err != nil {
		return errors.Wrapf(err, "failed to get interfaces")
	}

	for ifaceNameRaw, iface := range ifaces.Interface {
		if !isManagement(ifaceNameRaw) && !isPhysical(ifaceNameRaw) && !isPortChannel(ifaceNameRaw) {
			continue
		}

		if strings.Contains(ifaceNameRaw, ".") || strings.Contains(ifaceNameRaw, "|") {
			continue
		}

		if iface.State == nil {
			continue
		}

		ifaceName := ifaceNameRaw
		if isManagement(ifaceNameRaw) || isPhysical(ifaceNameRaw) {
			exists := false
			ifaceName, exists = portMap[ifaceNameRaw]
			if !exists && !(ag.Spec.Switch.Profile == switchprofile.VS.Name && switchprofile.VSIsIgnoredNOSPort(ifaceNameRaw)) {
				slog.Warn("Port mapping not found, ignoring for metrics", "interface", ifaceNameRaw)

				continue
			}
		}

		st := iface.State

		adminStatus, err := mapAdminStatus(st.AdminStatus)
		if err != nil {
			return errors.Wrapf(err, "failed to map admin status")
		}
		adminStatusID, err := adminStatus.ID()
		if err != nil {
			return errors.Wrapf(err, "failed to get admin status ID")
		}

		operStatus, err := mapOperStatus(st.OperStatus)
		if err != nil {
			return errors.Wrapf(err, "failed to map oper status")
		}
		operStatusID, err := operStatus.ID()
		if err != nil {
			return errors.Wrapf(err, "failed to get oper status ID")
		}

		reg.InterfaceMetrics.Enabled.WithLabelValues(ifaceName).Set(boolToFloat64(st.Enabled))
		reg.InterfaceMetrics.AdminStatus.WithLabelValues(ifaceName).Set(float64(adminStatusID))
		reg.InterfaceMetrics.OperStatus.WithLabelValues(ifaceName).Set(float64(operStatusID))

		if st.RateInterval != nil {
			reg.InterfaceMetrics.RateInterval.WithLabelValues(ifaceName).Set(float64(*st.RateInterval))
		}

		ifState := agentapi.SwitchStateInterface{}
		if st.Enabled != nil {
			ifState.Enabled = *st.Enabled
		}
		ifState.AdminStatus = adminStatus
		ifState.OperStatus = operStatus
		if st.MacAddress != nil {
			ifState.MAC = *st.MacAddress
		}
		if st.LastChange != nil {
			reg.InterfaceMetrics.LastChange.WithLabelValues(ifaceName).Set(float64(*st.LastChange))
			if *st.LastChange != 0 {
				ifState.LastChange = kmetav1.Time{Time: time.Unix(int64(*st.LastChange), 0)} //nolint:gosec
			}
		}

		if ifState.Enabled && st.Counters != nil {
			ifState.Counters = &agentapi.SwitchStateInterfaceCounters{}

			reg.InterfaceCounters.InBitsPerSecond.WithLabelValues(ifaceName).Set(unptrFloat64(st.Counters.InBitsPerSecond))
			ifState.Counters.InBitsPerSecond = unptrFloat64(st.Counters.InBitsPerSecond)

			if st.Counters.InBroadcastPkts != nil {
				reg.InterfaceCounters.InBroadcastPkts.WithLabelValues(ifaceName).Set(float64(*st.Counters.InBroadcastPkts))
			}

			if st.Counters.InDiscards != nil {
				reg.InterfaceCounters.InDiscards.WithLabelValues(ifaceName).Set(float64(*st.Counters.InDiscards))
				ifState.Counters.InDiscards = *st.Counters.InDiscards
			}

			if st.Counters.InErrors != nil {
				reg.InterfaceCounters.InErrors.WithLabelValues(ifaceName).Set(float64(*st.Counters.InErrors))
				ifState.Counters.InErrors = *st.Counters.InErrors
			}

			if st.Counters.InMulticastPkts != nil {
				reg.InterfaceCounters.InMulticastPkts.WithLabelValues(ifaceName).Set(float64(*st.Counters.InMulticastPkts))
			}

			if st.Counters.InOctets != nil {
				reg.InterfaceCounters.InOctets.WithLabelValues(ifaceName).Set(float64(*st.Counters.InOctets))
				reg.InterfaceCounters.InBits.WithLabelValues(ifaceName).Set(float64(*st.Counters.InOctets * 8))
				ifState.Counters.InBits = *st.Counters.InOctets * 8
			}

			reg.InterfaceCounters.InOctetsPerSecond.WithLabelValues(ifaceName).Set(unptrFloat64(st.Counters.InOctetsPerSecond))

			if st.Counters.InPkts != nil {
				reg.InterfaceCounters.InPkts.WithLabelValues(ifaceName).Set(float64(*st.Counters.InPkts))
			}

			reg.InterfaceCounters.InPktsPerSecond.WithLabelValues(ifaceName).Set(unptrFloat64(st.Counters.InPktsPerSecond))
			ifState.Counters.InPktsPerSecond = unptrFloat64(st.Counters.InPktsPerSecond)

			if st.Counters.InUnicastPkts != nil {
				reg.InterfaceCounters.InUnicastPkts.WithLabelValues(ifaceName).Set(float64(*st.Counters.InUnicastPkts))
			}

			if st.Counters.InUtilization != nil {
				reg.InterfaceCounters.InUtilization.WithLabelValues(ifaceName).Set(float64(*st.Counters.InUtilization))
				ifState.Counters.InUtilization = *st.Counters.InUtilization
			}

			if st.Counters.LastClear != nil {
				reg.InterfaceCounters.LastClear.WithLabelValues(ifaceName).Set(float64(*st.Counters.LastClear))
				if *st.Counters.LastClear != 0 {
					ifState.Counters.LastClear = kmetav1.Time{Time: time.Unix(int64(*st.Counters.LastClear), 0)} //nolint:gosec
				}
			}

			reg.InterfaceCounters.OutBitsPerSecond.WithLabelValues(ifaceName).Set(unptrFloat64(st.Counters.OutBitsPerSecond))
			ifState.Counters.OutBitsPerSecond = unptrFloat64(st.Counters.OutBitsPerSecond)

			if st.Counters.OutBroadcastPkts != nil {
				reg.InterfaceCounters.OutBroadcastPkts.WithLabelValues(ifaceName).Set(float64(*st.Counters.OutBroadcastPkts))
			}

			if st.Counters.OutDiscards != nil {
				reg.InterfaceCounters.OutDiscards.WithLabelValues(ifaceName).Set(float64(*st.Counters.OutDiscards))
				ifState.Counters.OutDiscards = *st.Counters.OutDiscards
			}

			if st.Counters.OutErrors != nil {
				reg.InterfaceCounters.OutErrors.WithLabelValues(ifaceName).Set(float64(*st.Counters.OutErrors))
				ifState.Counters.OutErrors = *st.Counters.OutErrors
			}

			if st.Counters.OutMulticastPkts != nil {
				reg.InterfaceCounters.OutMulticastPkts.WithLabelValues(ifaceName).Set(float64(*st.Counters.OutMulticastPkts))
			}

			if st.Counters.OutOctets != nil {
				reg.InterfaceCounters.OutOctets.WithLabelValues(ifaceName).Set(float64(*st.Counters.OutOctets))
				reg.InterfaceCounters.OutBits.WithLabelValues(ifaceName).Set(float64(*st.Counters.OutOctets * 8))
				ifState.Counters.OutBits = *st.Counters.OutOctets * 8
			}

			reg.InterfaceCounters.OutOctetsPerSecond.WithLabelValues(ifaceName).Set(unptrFloat64(st.Counters.OutOctetsPerSecond))

			if st.Counters.OutPkts != nil {
				reg.InterfaceCounters.OutPkts.WithLabelValues(ifaceName).Set(float64(*st.Counters.OutPkts))
			}

			reg.InterfaceCounters.OutPktsPerSecond.WithLabelValues(ifaceName).Set(unptrFloat64(st.Counters.OutPktsPerSecond))
			ifState.Counters.OutPktsPerSecond = unptrFloat64(st.Counters.OutPktsPerSecond)

			if st.Counters.OutUnicastPkts != nil {
				reg.InterfaceCounters.OutUnicastPkts.WithLabelValues(ifaceName).Set(float64(*st.Counters.OutUnicastPkts))
			}

			if st.Counters.OutUtilization != nil {
				reg.InterfaceCounters.OutUtilization.WithLabelValues(ifaceName).Set(float64(*st.Counters.OutUtilization))
				ifState.Counters.OutUtilization = *st.Counters.OutUtilization
			}
		}

		if iface.Ethernet != nil && iface.Ethernet.State != nil {
			ethSt := iface.Ethernet.State

			speed := UnmarshalPortSpeed(ethSt.PortSpeed)
			if speed != nil {
				ifState.Speed = *speed
			}

			if ethSt.AutoNegotiate != nil {
				ifState.AutoNegotiate = *ethSt.AutoNegotiate
			}

			switch ethSt.OperFec {
			case oc.OpenconfigPlatformTypes_FEC_MODE_TYPE_UNSET:
				ifState.FEC = ""
			case oc.OpenconfigPlatformTypes_FEC_MODE_TYPE_FEC_AUTO:
				ifState.FEC = "auto"
			case oc.OpenconfigPlatformTypes_FEC_MODE_TYPE_FEC_DEFAULT:
				ifState.FEC = "default"
			case oc.OpenconfigPlatformTypes_FEC_MODE_TYPE_FEC_DISABLED:
				ifState.FEC = "disabled"
			case oc.OpenconfigPlatformTypes_FEC_MODE_TYPE_FEC_ENABLED:
				ifState.FEC = "enabled"
			case oc.OpenconfigPlatformTypes_FEC_MODE_TYPE_FEC_FC:
				ifState.FEC = "fc"
			case oc.OpenconfigPlatformTypes_FEC_MODE_TYPE_FEC_RS:
				ifState.FEC = "rs"
			case oc.OpenconfigPlatformTypes_FEC_MODE_TYPE_FEC_RS528:
				ifState.FEC = "rs528"
			case oc.OpenconfigPlatformTypes_FEC_MODE_TYPE_FEC_RS544:
				ifState.FEC = "rs544"
			case oc.OpenconfigPlatformTypes_FEC_MODE_TYPE_FEC_RS544_2XN:
				ifState.FEC = "rs544_2xN"
			}
		}

		swState.Interfaces[ifaceName] = ifState
	}

	return nil
}

func (p *BroadcomProcessor) updateInterfaceQueuesMetrics(ctx context.Context, reg *switchstate.Registry, swState *agentapi.SwitchState, ag *agentapi.Agent, portMap map[string]string) error {
	ifaces := &oc.OpenconfigQos_Qos_Interfaces{}
	err := p.client.Get(ctx, "/qos/interfaces/interface", ifaces)
	if err != nil {
		return errors.Wrapf(err, "failed to get qos interfaces")
	}

	for ifaceNameRaw, iface := range ifaces.Interface {
		if !isPhysical(ifaceNameRaw) && !isCPU(ifaceNameRaw) {
			continue
		}

		if strings.Contains(ifaceNameRaw, ".") || strings.Contains(ifaceNameRaw, "|") {
			continue
		}

		if iface.Output == nil || iface.Output.Queues == nil || len(iface.Output.Queues.Queue) == 0 {
			continue
		}

		ifaceName := ifaceNameRaw
		if isPhysical(ifaceNameRaw) {
			exists := false
			ifaceName, exists = portMap[ifaceNameRaw]
			if !exists && !(ag.Spec.Switch.Profile == switchprofile.VS.Name && switchprofile.VSIsIgnoredNOSPort(ifaceNameRaw)) {
				slog.Warn("Port mapping not found, ignoring for queue metrics", "interface", ifaceNameRaw)

				continue
			}
		} else if isCPU(ifaceNameRaw) {
			ifaceName = "CPU"
		}

		ifaceSt, found := swState.Interfaces[ifaceName]
		if isCPU(ifaceNameRaw) {
			ifaceSt = agentapi.SwitchStateInterface{
				Enabled: true,
			}
		} else if !found {
			slog.Warn("Interface not found in switch state, skipping queue metrics", "interface", ifaceNameRaw)

			continue
		}

		if !ifaceSt.Enabled {
			continue
		}

		queues := map[string]agentapi.SwitchStateInterfaceCountersQueue{}
		for _, queue := range iface.Output.Queues.Queue {
			if queue.State == nil || queue.State.Name == nil || queue.State.TrafficType == nil {
				continue
			}

			parts := strings.SplitN(*queue.State.Name, ":", 2)
			if len(parts) != 2 {
				slog.Warn("Queue name does not have expected format", "queueName", *queue.State.Name, "interface", ifaceNameRaw)

				continue
			}
			qName := *queue.State.TrafficType + parts[1]
			if _, exists := queues[qName]; exists {
				slog.Warn("Queue with the same name already exists, skipping", "queueName", qName, "interface", ifaceNameRaw)

				continue
			}

			qCounters := agentapi.SwitchStateInterfaceCountersQueue{}
			nonZero := false
			if queue.State.DroppedOctets != nil {
				val := *queue.State.DroppedOctets
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueueDroppedBits.WithLabelValues(ifaceName, qName).Set(float64(val * 8))
				reg.InterfaceCounters.QueueDroppedOctets.WithLabelValues(ifaceName, qName).Set(float64(val))
				qCounters.DroppedBits = val * 8
			}

			if queue.State.DroppedPkts != nil {
				val := *queue.State.DroppedPkts
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueueDroppedPkts.WithLabelValues(ifaceName, qName).Set(float64(val))
				qCounters.DroppedPkts = val
			}

			if queue.State.EcnMarkedOctets != nil {
				val := *queue.State.EcnMarkedOctets
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueueECNMarkedBits.WithLabelValues(ifaceName, qName).Set(float64(val * 8))
				reg.InterfaceCounters.QueueECNMarkedOctets.WithLabelValues(ifaceName, qName).Set(float64(val))
				qCounters.ECNMarkedBits = val * 8
			}

			if queue.State.EcnMarkedPkts != nil {
				val := *queue.State.EcnMarkedPkts
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueueECNMarkedPkts.WithLabelValues(ifaceName, qName).Set(float64(val))
				qCounters.ECNMarkedPkts = val
			}

			if queue.State.PeriodicWatermark != nil {
				val := *queue.State.PeriodicWatermark
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueuePeriodicWatermark.WithLabelValues(ifaceName, qName).Set(float64(val))
			}

			if queue.State.PersistentWatermark != nil {
				val := *queue.State.PersistentWatermark
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueuePersistentWatermark.WithLabelValues(ifaceName, qName).Set(float64(val))
			}

			if queue.State.TransmitBitsPerSecond != nil {
				val := *queue.State.TransmitBitsPerSecond
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueueTransmitBitsPerSecond.WithLabelValues(ifaceName, qName).Set(float64(val))
				qCounters.TransmitBitsPerSecond = val
			}

			if queue.State.TransmitOctets != nil {
				val := *queue.State.TransmitOctets
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueueTransmitOctets.WithLabelValues(ifaceName, qName).Set(float64(val))
				reg.InterfaceCounters.QueueTransmitBits.WithLabelValues(ifaceName, qName).Set(float64(val * 8))
				qCounters.TransmitBits = val * 8
			}

			if queue.State.TransmitOctetsPerSecond != nil {
				val := *queue.State.TransmitOctetsPerSecond
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueueTransmitOctetsPerSecond.WithLabelValues(ifaceName, qName).Set(float64(val))
			}

			if queue.State.TransmitPkts != nil {
				val := *queue.State.TransmitPkts
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueueTransmitPkts.WithLabelValues(ifaceName, qName).Set(float64(val))
				qCounters.TransmitPkts = val
			}

			if queue.State.TransmitPktsPerSecond != nil {
				val := *queue.State.TransmitPktsPerSecond
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueueTransmitPktsPerSecond.WithLabelValues(ifaceName, qName).Set(float64(val))
				qCounters.TransmitPktsPerSecond = val
			}

			if queue.State.Watermark != nil {
				val := *queue.State.Watermark
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueueWatermark.WithLabelValues(ifaceName, qName).Set(float64(val))
			}

			if queue.State.WredDroppedPkts != nil {
				val := *queue.State.WredDroppedPkts
				nonZero = nonZero || val != 0
				reg.InterfaceCounters.QueueWREDDroppedPkts.WithLabelValues(ifaceName, qName).Set(float64(val))
				qCounters.WREDDroppedPkts = val
			}

			if nonZero {
				queues[qName] = qCounters
			}
		}

		if len(queues) == 0 {
			continue
		}

		if ifaceSt.Counters == nil {
			ifaceSt.Counters = &agentapi.SwitchStateInterfaceCounters{}
		}
		ifaceSt.Counters.Queues = queues
		swState.Interfaces[ifaceName] = ifaceSt
	}

	return nil
}

func (p *BroadcomProcessor) updateTransceiverMetrics(ctx context.Context, reg *switchstate.Registry, swState *agentapi.SwitchState, portMap map[string]string) error {
	dev := &oc.Device{}
	if err := p.client.Get(ctx, "/transceiver-dom", dev); err != nil {
		return errors.Wrapf(err, "failed to get transceiver-dom")
	}

	if dev.TransceiverDom == nil {
		return nil
	}

	for transceiverNameRaw, transceiver := range dev.TransceiverDom.TransceiverDomInfo {
		if !strings.HasPrefix(transceiverNameRaw, "Ethernet") {
			continue
		}

		if transceiver.State == nil {
			continue
		}

		transceiverName, exists := portMap[transceiverNameRaw]
		if !exists {
			slog.Warn("Port mapping not found, ignoring for metrics", "transceiver", transceiverNameRaw)

			continue
		}

		transceiverName, exists = normBreakoutName(transceiverName)
		if !exists {
			continue
		}

		ocSt := transceiver.State
		st := swState.Transceivers[transceiverName]

		if ocSt.CableClass != nil {
			st.CableClass = *ocSt.CableClass
		}

		if ocSt.Temperature != nil {
			reg.TransceiverMetrics.Temperature.WithLabelValues(transceiverName).Set(*ocSt.Temperature)
			st.Temperature = *ocSt.Temperature
		}

		if ocSt.Voltage != nil {
			reg.TransceiverMetrics.Voltage.WithLabelValues(transceiverName).Set(*ocSt.Voltage)
			st.Voltage = *ocSt.Voltage
		}

		swState.Transceivers[transceiverName] = st
	}

	return nil
}

func (p *BroadcomProcessor) updateCMISMetrics(ctx context.Context, ag *agentapi.Agent, swState *agentapi.SwitchState, portMap map[string]string) error {
	if ag.Spec.IsVS() {
		return nil
	}

	cmis := &oc.OpenconfigPlatformDiagnostics_TransceiverCmis{}
	if err := p.client.Get(ctx, "/transceiver-cmis/transceiver-cmis-info", cmis); err != nil {
		return errors.Wrapf(err, "failed to get transceiver-cmis")
	}

	if cmis.TransceiverCmisInfo == nil {
		return nil
	}

	for transceiverNameRaw, transceiver := range cmis.TransceiverCmisInfo {
		if !strings.HasPrefix(transceiverNameRaw, "Ethernet") {
			continue
		}

		if transceiver.State == nil {
			continue
		}

		transceiverName, exists := portMap[transceiverNameRaw]
		if !exists {
			slog.Warn("Port mapping not found, ignoring for metrics (cmis)", "transceiver", transceiverNameRaw)

			continue
		}

		transceiverName, exists = normBreakoutName(transceiverName)
		if !exists {
			continue
		}

		ocSt := transceiver.State
		st := swState.Transceivers[transceiverName]

		if ocSt.Status != nil {
			st.CMISStatus = *ocSt.Status
		}

		if ocSt.Revision != nil {
			st.CMISRev = *ocSt.Revision
		}

		if ocSt.Appsel != nil {
			st.CMISApp = *ocSt.Appsel
		}

		swState.Transceivers[transceiverName] = st
	}

	return nil
}

func (p *BroadcomProcessor) updateLLDPNeighbors(ctx context.Context, swState *agentapi.SwitchState, portMap map[string]string) error {
	lldp := &oc.OpenconfigLldp_Lldp{}
	err := p.client.Get(ctx, "/lldp/interfaces", lldp)
	if err != nil {
		return errors.Wrapf(err, "failed to get lldp interfaces")
	}

	if lldp.Interfaces == nil {
		return nil
	}

	for ifaceName, iface := range lldp.Interfaces.Interface {
		if iface.Neighbors == nil {
			continue
		}

		neighbours := []agentapi.SwitchStateLLDPNeighbor{}

		for neighbourName, neighbour := range iface.Neighbors.Neighbor {
			if neighbour.State == nil {
				continue
			}

			nSt := neighbour.State
			st := agentapi.SwitchStateLLDPNeighbor{
				Name: neighbourName,
			}

			if nSt.ChassisId != nil {
				st.ChassisID = *nSt.ChassisId
			}

			if nSt.SystemName != nil {
				st.SystemName = *nSt.SystemName
			}

			if nSt.SystemDescription != nil {
				st.SystemDescription = *nSt.SystemDescription
			}

			if nSt.PortId != nil {
				st.PortID = *nSt.PortId
			}

			if nSt.PortDescription != nil {
				st.PortDescription = *nSt.PortDescription
			}

			if neighbour.Med != nil {
				nMed := neighbour.Med

				if nMed.State != nil && nMed.State.Inventory != nil {
					nInt := nMed.State.Inventory

					if nInt.Manufacturer != nil {
						st.Manufacturer = *nInt.Manufacturer
					}

					if nInt.Model != nil {
						st.Model = *nInt.Model
					}

					if nInt.SerialNumber != nil {
						st.SerialNumber = *nInt.SerialNumber
					}
				}
			}

			neighbours = append(neighbours, st)
		}

		ifaceNameTr, exists := portMap[ifaceName]
		if !exists {
			slog.Warn("Port mapping not found, ignoring for metrics", "lldpInterface", ifaceName)

			continue
		}

		intSt := swState.Interfaces[ifaceNameTr]
		intSt.LLDPNeighbors = neighbours
		swState.Interfaces[ifaceNameTr] = intSt
	}

	return nil
}

func (p *BroadcomProcessor) updateBGPNeighborMetrics(ctx context.Context, reg *switchstate.Registry, swState *agentapi.SwitchState) error {
	sonicVRFs := &oc.SonicVrf_SonicVrf_VRF{}
	if err := p.client.Get(ctx, "/sonic-vrf/VRF/VRF_LIST", sonicVRFs, api.DataTypeCONFIG()); err != nil {
		return errors.Wrapf(err, "failed to get vrfs list")
	}

	for vrfName := range sonicVRFs.VRF_LIST {
		if vrfName != VRFDefault && !strings.HasPrefix(vrfName, "VrfI") && !strings.HasPrefix(vrfName, "VrfE") {
			continue
		}

		neighs := &oc.OpenconfigNetworkInstance_NetworkInstances_NetworkInstance_Protocols_Protocol_Bgp_Neighbors{}
		path := fmt.Sprintf("/network-instances/network-instance[name=%s]/protocols/protocol[identifier=BGP][name=bgp]/bgp/neighbors/neighbor", vrfName)
		if err := p.client.Get(ctx, path, neighs); err != nil {
			if !strings.Contains(err.Error(), "rpc error: code = NotFound") { // TODO rework client to handle it
				return errors.Wrapf(err, "failed to get bgp neighbors for vrf %s", vrfName)
			}
		}

		vrfSt := map[string]agentapi.SwitchStateBGPNeighbor{}
		for neighborAddress, neighbor := range neighs.Neighbor {
			if neighbor.State == nil {
				continue
			}

			ocSt := neighbor.State
			st := agentapi.SwitchStateBGPNeighbor{
				Prefixes: map[string]agentapi.SwitchStateBGPNeighborPrefixes{},
			}

			if ocSt.Enabled != nil {
				reg.BGPNeighborMetrics.Enabled.WithLabelValues(vrfName, neighborAddress).Set(boolToFloat64(ocSt.Enabled))
				st.Enabled = *ocSt.Enabled
			}

			if ocSt.ConnectionsDropped != nil {
				reg.BGPNeighborMetrics.ConnectionsDropped.WithLabelValues(vrfName, neighborAddress).Set(float64(*ocSt.ConnectionsDropped))
				st.ConnectionsDropped = *ocSt.ConnectionsDropped
			}

			if ocSt.EstablishedTransitions != nil {
				reg.BGPNeighborMetrics.EstablishedTransitions.WithLabelValues(vrfName, neighborAddress).Set(float64(*ocSt.EstablishedTransitions))
				st.EstablishedTransitions = *ocSt.EstablishedTransitions
			}

			if ocSt.LastEstablished != nil {
				if *ocSt.LastEstablished != 0 {
					st.LastEstablished = kmetav1.Time{Time: time.Unix(int64(*ocSt.LastEstablished), 0)} //nolint:gosec
				}
			}

			if ocSt.LastRead != nil {
				if *ocSt.LastRead != 0 {
					st.LastRead = kmetav1.Time{Time: time.Unix(int64(*ocSt.LastRead), 0)} //nolint:gosec
				}
			}

			if ocSt.LastResetReason != nil {
				st.LastResetReason = *ocSt.LastResetReason
			}

			if ocSt.LastResetTime != nil {
				if *ocSt.LastResetTime != 0 {
					st.LastResetTime = kmetav1.Time{Time: time.Unix(int64(*ocSt.LastResetTime), 0)} //nolint:gosec
				}
			}

			if ocSt.LastWrite != nil {
				if *ocSt.LastWrite != 0 {
					st.LastWrite = kmetav1.Time{Time: time.Unix(int64(*ocSt.LastWrite), 0)} //nolint:gosec
				}
			}

			if ocSt.LocalAs != nil {
				// TODO parse https://datatracker.ietf.org/doc/html/rfc5396
				if val, ok := ocSt.LocalAs.(oc.UnionUint32); ok {
					st.LocalAS = uint32(val)
				}
			}

			if ocSt.PeerAs != nil {
				// TODO parse https://datatracker.ietf.org/doc/html/rfc5396
				if val, ok := ocSt.PeerAs.(oc.UnionUint32); ok {
					st.PeerAS = uint32(val)
				}
			}

			if ocSt.PeerGroup != nil {
				st.PeerGroup = *ocSt.PeerGroup
			}

			if ocSt.PeerPort != nil {
				st.PeerPort = *ocSt.PeerPort
			}

			peerType, err := mapBGPPeerType(ocSt.PeerType)
			if err != nil {
				return errors.Wrapf(err, "failed to map bgp peer type")
			}
			st.PeerType = peerType

			peerTypeID, err := peerType.ID()
			if err != nil {
				return errors.Wrapf(err, "failed to get peer type ID")
			}
			reg.BGPNeighborMetrics.PeerType.WithLabelValues(vrfName, neighborAddress).Set(float64(peerTypeID))

			if ocSt.RemoteRouterId != nil {
				st.RemoteRouterID = *ocSt.RemoteRouterId
			}

			sessionState, err := mapBGPNeighborSessionState(ocSt.SessionState)
			if err != nil {
				return errors.Wrapf(err, "failed to map bgp neighbor session state")
			}
			st.SessionState = sessionState

			sessionStateID, err := sessionState.ID()
			if err != nil {
				return errors.Wrapf(err, "failed to get session state ID")
			}
			reg.BGPNeighborMetrics.SessionState.WithLabelValues(vrfName, neighborAddress).Set(float64(sessionStateID))

			if ocSt.ShutdownMessage != nil {
				st.ShutdownMessage = *ocSt.ShutdownMessage
			}

			if ocSt.Messages != nil {
				messages := agentapi.BGPMessages{}
				if ocSt.Messages.Received != nil {
					ocR := ocSt.Messages.Received
					messages.Received = agentapi.BGPMessagesCounters{
						Capability:   unptrUint64(ocR.CAPABILITY),
						Keepalive:    unptrUint64(ocR.KEEPALIVE),
						Notification: unptrUint64(ocR.NOTIFICATION),
						Open:         unptrUint64(ocR.OPEN),
						RouteRefresh: unptrUint64(ocR.ROUTE_REFRESH),
						Update:       unptrUint64(ocR.UPDATE),
					}

					reg.BGPNeighborMetrics.Messages.Received.Capability.WithLabelValues(vrfName, neighborAddress).Set(float64(unptrUint64(ocR.CAPABILITY)))
					reg.BGPNeighborMetrics.Messages.Received.Keepalive.WithLabelValues(vrfName, neighborAddress).Set(float64(unptrUint64(ocR.KEEPALIVE)))
					reg.BGPNeighborMetrics.Messages.Received.Notification.WithLabelValues(vrfName, neighborAddress).Set(float64(unptrUint64(ocR.NOTIFICATION)))
					reg.BGPNeighborMetrics.Messages.Received.Open.WithLabelValues(vrfName, neighborAddress).Set(float64(unptrUint64(ocR.OPEN)))
					reg.BGPNeighborMetrics.Messages.Received.RouteRefresh.WithLabelValues(vrfName, neighborAddress).Set(float64(unptrUint64(ocR.ROUTE_REFRESH)))
					reg.BGPNeighborMetrics.Messages.Received.Update.WithLabelValues(vrfName, neighborAddress).Set(float64(unptrUint64(ocR.UPDATE)))
				}

				if ocSt.Messages.Sent != nil {
					ocS := ocSt.Messages.Sent
					messages.Sent = agentapi.BGPMessagesCounters{
						Capability:   unptrUint64(ocS.CAPABILITY),
						Keepalive:    unptrUint64(ocS.KEEPALIVE),
						Notification: unptrUint64(ocS.NOTIFICATION),
						Open:         unptrUint64(ocS.OPEN),
						RouteRefresh: unptrUint64(ocS.ROUTE_REFRESH),
						Update:       unptrUint64(ocS.UPDATE),
					}

					reg.BGPNeighborMetrics.Messages.Sent.Capability.WithLabelValues(vrfName, neighborAddress).Set(float64(unptrUint64(ocS.CAPABILITY)))
					reg.BGPNeighborMetrics.Messages.Sent.Keepalive.WithLabelValues(vrfName, neighborAddress).Set(float64(unptrUint64(ocS.KEEPALIVE)))
					reg.BGPNeighborMetrics.Messages.Sent.Notification.WithLabelValues(vrfName, neighborAddress).Set(float64(unptrUint64(ocS.NOTIFICATION)))
					reg.BGPNeighborMetrics.Messages.Sent.Open.WithLabelValues(vrfName, neighborAddress).Set(float64(unptrUint64(ocS.OPEN)))
					reg.BGPNeighborMetrics.Messages.Sent.RouteRefresh.WithLabelValues(vrfName, neighborAddress).Set(float64(unptrUint64(ocS.ROUTE_REFRESH)))
					reg.BGPNeighborMetrics.Messages.Sent.Update.WithLabelValues(vrfName, neighborAddress).Set(float64(unptrUint64(ocS.UPDATE)))
				}

				st.Messages = messages
			}

			if neighbor.AfiSafis != nil {
				for afiSafiName, afiSafi := range neighbor.AfiSafis.AfiSafi {
					if afiSafi.State == nil || afiSafi.State.Prefixes == nil {
						continue
					}

					afiSafiName := afiSafiName.String()

					st.Prefixes[afiSafiName] = agentapi.SwitchStateBGPNeighborPrefixes{
						Received:          unptrUint32(afiSafi.State.Prefixes.Received),
						ReceivedPrePolicy: unptrUint32(afiSafi.State.Prefixes.ReceivedPrePolicy),
						Sent:              unptrUint32(afiSafi.State.Prefixes.Sent),
					}

					reg.BGPNeighborMetrics.Prefixes.Received.WithLabelValues(vrfName, neighborAddress, afiSafiName).Set(float64(unptrUint32(afiSafi.State.Prefixes.Received)))
					reg.BGPNeighborMetrics.Prefixes.ReceivedPrePolicy.WithLabelValues(vrfName, neighborAddress, afiSafiName).Set(float64(unptrUint32(afiSafi.State.Prefixes.ReceivedPrePolicy)))
					reg.BGPNeighborMetrics.Prefixes.Sent.WithLabelValues(vrfName, neighborAddress, afiSafiName).Set(float64(unptrUint32(afiSafi.State.Prefixes.Sent)))
				}
			}

			vrfSt[neighborAddress] = st
		}

		swState.BGPNeighbors[vrfName] = vrfSt
	}

	return nil
}

func (p *BroadcomProcessor) updatePlatformMetrics(ctx context.Context, agent *agentapi.Agent, reg *switchstate.Registry, swState *agentapi.SwitchState) error {
	dev := &oc.Device{}
	if err := p.client.Get(ctx, "/sonic-platform", dev); err != nil {
		return errors.Wrapf(err, "failed to get sonic-platform")
	}
	if dev.SonicPlatform == nil {
		if agent.Spec.IsVS() {
			return nil
		}

		return errors.Errorf("sonic-platform not found")
	}

	if dev.SonicPlatform.FAN_INFO != nil {
		for fanName, fan := range dev.SonicPlatform.FAN_INFO.FAN_INFO_LIST {
			if fan.Name != nil {
				fanName = *fan.Name
			}

			st := agentapi.SwitchStatePlatformFan{}

			if fan.Speed != nil && *fan.Speed != fanIgnore {
				speed, err := strconv.ParseFloat(*fan.Speed, 64)
				if err != nil {
					slog.Warn("failed to parse fan speed", "fan", fanName, "speed", *fan.Speed)
				} else {
					reg.PlatformMetrics.Fan.Speed.WithLabelValues(fanName).Set(speed)
					st.Speed = speed
				}
			}

			if fan.Presence != nil {
				reg.PlatformMetrics.Fan.Presence.WithLabelValues(fanName).Set(boolToFloat64(fan.Presence))
				st.Presence = *fan.Presence
			}

			if fan.Status != nil {
				reg.PlatformMetrics.Fan.Status.WithLabelValues(fanName).Set(boolToFloat64(fan.Status))
				st.Status = *fan.Status
			}

			if fan.Direction != nil {
				st.Direction = *fan.Direction
			}

			swState.Platform.Fans[fanName] = st
		}
	}

	if dev.SonicPlatform.PSU_INFO != nil {
		for psuName, psu := range dev.SonicPlatform.PSU_INFO.PSU_INFO_LIST {
			if psu.Name != nil {
				psuName = *psu.Name
			}

			st := agentapi.SwitchStatePlatformPSU{}

			if psu.Presence != nil {
				reg.PlatformMetrics.PSU.Presence.WithLabelValues(psuName).Set(boolToFloat64(psu.Presence))
				st.Presence = *psu.Presence
			}

			if psu.Status != nil {
				reg.PlatformMetrics.PSU.Status.WithLabelValues(psuName).Set(boolToFloat64(psu.Status))
				st.Status = *psu.Status
			}

			if psu.InputCurrent != nil && *psu.InputCurrent != psuIgnore {
				val, err := strconv.ParseFloat(*psu.InputCurrent, 64)
				if err != nil {
					slog.Warn("failed to parse psu input current", "psu", psuName, "input_current", *psu.InputCurrent)
				} else {
					reg.PlatformMetrics.PSU.InputCurrent.WithLabelValues(psuName).Set(val)
					st.InputCurrent = val
				}
			}

			if psu.InputPower != nil && *psu.InputPower != psuIgnore {
				val, err := strconv.ParseFloat(*psu.InputPower, 64)
				if err != nil {
					slog.Warn("failed to parse psu input power", "psu", psuName, "input_power", *psu.InputPower)
				} else {
					reg.PlatformMetrics.PSU.InputPower.WithLabelValues(psuName).Set(val)
					st.InputPower = val
				}
			}

			if psu.InputVoltage != nil && *psu.InputVoltage != psuIgnore {
				val, err := strconv.ParseFloat(*psu.InputVoltage, 64)
				if err != nil {
					slog.Warn("failed to parse psu input voltage", "psu", psuName, "input_voltage", *psu.InputVoltage)
				} else {
					reg.PlatformMetrics.PSU.InputVoltage.WithLabelValues(psuName).Set(val)
					st.InputVoltage = val
				}
			}

			if psu.OutputCurrent != nil && *psu.OutputCurrent != psuIgnore {
				val, err := strconv.ParseFloat(*psu.OutputCurrent, 64)
				if err != nil {
					slog.Warn("failed to parse psu output current", "psu", psuName, "output_current", *psu.OutputCurrent)
				} else {
					reg.PlatformMetrics.PSU.OutputCurrent.WithLabelValues(psuName).Set(val)
					st.OutputCurrent = val
				}
			}

			if psu.OutputPower != nil && *psu.OutputPower != psuIgnore {
				val, err := strconv.ParseFloat(*psu.OutputPower, 64)
				if err != nil {
					slog.Warn("failed to parse psu output power", "psu", psuName, "output_power", *psu.OutputPower)
				} else {
					reg.PlatformMetrics.PSU.OutputPower.WithLabelValues(psuName).Set(val)
					st.OutputPower = val
				}
			}

			if psu.OutputVoltage != nil && *psu.OutputVoltage != psuIgnore {
				val, err := strconv.ParseFloat(*psu.OutputVoltage, 64)
				if err != nil {
					slog.Warn("failed to parse psu output voltage", "psu", psuName, "output_voltage", *psu.OutputVoltage)
				} else {
					reg.PlatformMetrics.PSU.OutputVoltage.WithLabelValues(psuName).Set(val)
					st.OutputVoltage = val
				}
			}

			swState.Platform.PSUs[psuName] = st
		}
	}

	if dev.SonicPlatform.TEMPERATURE_INFO != nil {
		for tempName, temp := range dev.SonicPlatform.TEMPERATURE_INFO.TEMPERATURE_INFO_LIST {
			if temp.Name != nil {
				tempName = *temp.Name
			}

			st := agentapi.SwitchStatePlatformTemperature{}

			if temp.Temperature != nil && *temp.Temperature != temperatureIgnore {
				val, err := strconv.ParseFloat(*temp.Temperature, 64)
				if err != nil {
					slog.Warn("failed to parse temperature", "temperature", *temp.Temperature)
				} else {
					reg.PlatformMetrics.Temperature.Temperature.WithLabelValues(tempName).Set(val)
					st.Temperature = val
				}
			}

			if temp.Alarms != nil {
				st.Alarms = *temp.Alarms
			}

			if temp.HighThreshold != nil && *temp.HighThreshold != temperatureIgnore {
				val, err := strconv.ParseFloat(*temp.HighThreshold, 64)
				if err != nil {
					slog.Warn("failed to parse temperature high threshold", "high_threshold", *temp.HighThreshold)
				} else {
					reg.PlatformMetrics.Temperature.HighThreshold.WithLabelValues(tempName).Set(val)
					st.HighThreshold = val
				}
			}

			if temp.LowThreshold != nil && *temp.LowThreshold != temperatureIgnore {
				val, err := strconv.ParseFloat(*temp.LowThreshold, 64)
				if err != nil {
					slog.Warn("failed to parse temperature low threshold", "low_threshold", *temp.LowThreshold)
				} else {
					reg.PlatformMetrics.Temperature.LowThreshold.WithLabelValues(tempName).Set(val)
					st.LowThreshold = val
				}
			}

			if temp.CriticalHighThreshold != nil && *temp.CriticalHighThreshold != temperatureIgnore {
				val, err := strconv.ParseFloat(*temp.CriticalHighThreshold, 64)
				if err != nil {
					slog.Warn("failed to parse temperature critical high threshold", "critical_high_threshold", *temp.CriticalHighThreshold)
				} else {
					reg.PlatformMetrics.Temperature.CriticalHighThreshold.WithLabelValues(tempName).Set(val)
					st.CriticalHighThreshold = val
				}
			}

			if temp.CriticalLowThreshold != nil && *temp.CriticalLowThreshold != temperatureIgnore {
				val, err := strconv.ParseFloat(*temp.CriticalLowThreshold, 64)
				if err != nil {
					slog.Warn("failed to parse temperature critical low threshold", "critical_low_threshold", *temp.CriticalLowThreshold)
				} else {
					reg.PlatformMetrics.Temperature.CriticalLowThreshold.WithLabelValues(tempName).Set(val)
					st.CriticalLowThreshold = val
				}
			}

			swState.Platform.Temperatures[tempName] = st
		}
	}

	return nil
}

func (p *BroadcomProcessor) updateBreakoutMetrics(ctx context.Context, _ *switchstate.Registry, swState *agentapi.SwitchState, ag *agentapi.Agent, portMap map[string]string) error {
	dev := &oc.Device{}
	if err := p.client.Get(ctx, "/sonic-port-breakout", dev); err != nil {
		return errors.Wrapf(err, "failed to get breakouts")
	}
	if dev.SonicPortBreakout == nil || dev.SonicPortBreakout.BREAKOUT_CFG == nil {
		return nil
	}

	for rawBreakoutName, breakout := range dev.SonicPortBreakout.BREAKOUT_CFG.BREAKOUT_CFG_LIST {
		breakoutName, exists := portMap[rawBreakoutName]
		if !exists && !(ag.Spec.Switch.Profile == switchprofile.VS.Name && switchprofile.VSIsIgnoredComponent(breakoutName)) {
			slog.Warn("Breakout mapping not found, ignoring for metrics", "breakout", rawBreakoutName)
		}

		breakoutName, exists = normBreakoutName(breakoutName)
		if !exists {
			continue
		}

		st := swState.Breakouts[breakoutName]

		if breakout.BrkoutMode != nil {
			st.Mode = UnmarshalPortBreakout(*breakout.BrkoutMode)
		}

		if breakout.Status != nil {
			st.Status = *breakout.Status
		}

		swState.Breakouts[breakoutName] = st
	}

	return nil
}

func (p *BroadcomProcessor) updateComponentMetrics(ctx context.Context, reg *switchstate.Registry, swState *agentapi.SwitchState, portMap map[string]string) error {
	dev := &oc.Device{}
	if err := p.client.Get(ctx, "/components", dev); err != nil {
		return errors.Wrapf(err, "failed to get components")
	}
	if dev.Components == nil {
		return errors.Errorf("components not found")
	}

	for componentName, component := range dev.Components.Component {
		if component.State != nil && component.Transceiver != nil && component.Transceiver.State != nil {
			transceiverName, exists := portMap[componentName]
			if !exists {
				slog.Warn("Port mapping not found, ignoring for metrics", "component", componentName)
			} else if transceiverName, exists := normBreakoutName(transceiverName); exists {
				st := swState.Transceivers[transceiverName]
				ocSt := component.Transceiver.State

				operStatus, err := mapComponentOperStatus(component.State.OperStatus)
				if err != nil {
					return errors.Wrapf(err, "failed to map component oper status")
				}

				st.OperStatus = operStatus

				if ocSt.DisplayName != nil {
					st.Description = *ocSt.DisplayName
				}

				if ocSt.FormFactor > 0 {
					st.FormFactor = ocSt.FormFactor.String()
				}

				if ocSt.ConnectorType > 0 {
					st.ConnectorType = ocSt.ConnectorType.String()
				}

				if ocSt.Present > 0 {
					st.Present = ocSt.Present.String()
				}

				if ocSt.CableLength != nil {
					st.CableLength = *ocSt.CableLength
				}

				if ocSt.SerialNo != nil {
					st.SerialNumber = *ocSt.SerialNo
				}

				if ocSt.Vendor != nil {
					st.Vendor = *ocSt.Vendor
				}

				if ocSt.VendorPart != nil {
					st.VendorPart = *ocSt.VendorPart
				}

				if ocSt.VendorOui != nil {
					st.VendorOUI = *ocSt.VendorOui
				}

				if ocSt.VendorRev != nil {
					st.VendorRev = *ocSt.VendorRev
				}

				if ocSt.FirmwareRevision != nil && *ocSt.FirmwareRevision != "0.0" {
					st.Firmware = *ocSt.FirmwareRevision
				}

				if component.Transceiver.PhysicalChannels != nil {
					st.Channels = map[string]agentapi.SwitchStateTransceiverChannel{}
					for channelID, channel := range component.Transceiver.PhysicalChannels.Channel {
						if channel.State == nil {
							continue
						}

						ch := fmt.Sprintf("%d", channelID)
						chSt := agentapi.SwitchStateTransceiverChannel{}

						if channel.State.InputPower != nil {
							reg.TransceiverMetrics.InPower.WithLabelValues(transceiverName, ch).Set(*channel.State.InputPower.Instant)
							chSt.In = normPower(channel.State.InputPower.Instant)
						}

						if channel.State.OutputPower != nil {
							reg.TransceiverMetrics.OutPower.WithLabelValues(transceiverName, ch).Set(*channel.State.OutputPower.Instant)
							chSt.Out = normPower(channel.State.OutputPower.Instant)
						}

						if channel.State.LaserBiasCurrent != nil {
							reg.TransceiverMetrics.Bias.WithLabelValues(transceiverName, ch).Set(*channel.State.LaserBiasCurrent.Instant)
							chSt.Bias = normBias(channel.State.LaserBiasCurrent.Instant)
						}

						st.Channels[ch] = chSt
					}
				}

				swState.Transceivers[transceiverName] = st
			}
		}

		if component.SoftwareModule != nil && component.SoftwareModule.State != nil {
			ocSt := component.SoftwareModule.State
			st := agentapi.SwitchStateNOS{}

			if ocSt.AsicVersion != nil {
				st.AsicVersion = *ocSt.AsicVersion
			}

			if ocSt.BuildCommit != nil {
				st.BuildCommit = *ocSt.BuildCommit
			}

			if ocSt.BuildDate != nil {
				st.BuildDate = *ocSt.BuildDate
			}

			if ocSt.BuiltBy != nil {
				st.BuiltBy = *ocSt.BuiltBy
			}

			if ocSt.ConfigDbVersion != nil {
				st.ConfigDBVersion = *ocSt.ConfigDbVersion
			}

			if ocSt.DistributionVersion != nil {
				st.DistributionVersion = *ocSt.DistributionVersion
			}

			if ocSt.HardwareVersion != nil {
				st.HardwareVersion = *ocSt.HardwareVersion
			}

			if ocSt.HwskuVersion != nil {
				st.HwskuVersion = *ocSt.HwskuVersion
			}

			if ocSt.KernelVersion != nil {
				st.KernelVersion = *ocSt.KernelVersion
			}

			if ocSt.MfgName != nil {
				st.MfgName = *ocSt.MfgName
			}

			if ocSt.PlatformName != nil {
				st.PlatformName = *ocSt.PlatformName
			}

			if ocSt.ProductDescription != nil {
				st.ProductDescription = *ocSt.ProductDescription
			}

			if ocSt.ProductVersion != nil {
				st.ProductVersion = *ocSt.ProductVersion
			}

			if ocSt.SerialNumber != nil {
				st.SerialNumber = *ocSt.SerialNumber
			}

			if ocSt.SoftwareVersion != nil {
				st.SoftwareVersion = *ocSt.SoftwareVersion
			}

			if ocSt.UpTime != nil {
				st.Uptime = *ocSt.UpTime
			}

			swState.NOS = st
		}

		if component.State != nil && strings.HasPrefix(componentName, "FIRMWARE ") {
			name := component.State.Name
			version := component.State.FirmwareVersion
			if name != nil && *name != "" && version != nil && *version != "" {
				swState.Firmware[*name] = *version
			}
		}
	}

	return nil
}

func (p *BroadcomProcessor) updateCRMMetrics(ctx context.Context, reg *switchstate.Registry, swState *agentapi.SwitchState) error {
	sys := &oc.OpenconfigSystem_System{}
	if err := p.client.Get(ctx, "/system/crm", sys); err != nil {
		return errors.Wrapf(err, "failed to get system crm")
	}
	if sys.Crm == nil {
		return errors.Errorf("crm not found")
	}

	if sys.Crm.AclStatistics != nil {
		if sys.Crm.AclStatistics.Egress != nil {
			ocEgress := sys.Crm.AclStatistics.Egress

			if ocEgress.Lag != nil && ocEgress.Lag.State != nil {
				reg.CriticalResources.ACLStats.GroupsAvailable.WithLabelValues("egress", "lag").Set(uint32ptrAsFloat64(ocEgress.Lag.State.GroupsAvailable))
				reg.CriticalResources.ACLStats.GroupsUsed.WithLabelValues("egress", "lag").Set(uint32ptrAsFloat64(ocEgress.Lag.State.GroupsUsed))
				reg.CriticalResources.ACLStats.TablesAvailable.WithLabelValues("egress", "lag").Set(uint32ptrAsFloat64(ocEgress.Lag.State.TablesAvailable))
				reg.CriticalResources.ACLStats.TablesUsed.WithLabelValues("egress", "lag").Set(uint32ptrAsFloat64(ocEgress.Lag.State.TablesUsed))

				swState.CriticalResources.ACLStats.Egress.Lag.GroupsAvailable = unptrUint32(ocEgress.Lag.State.GroupsAvailable)
				swState.CriticalResources.ACLStats.Egress.Lag.GroupsUsed = unptrUint32(ocEgress.Lag.State.GroupsUsed)
				swState.CriticalResources.ACLStats.Egress.Lag.TablesAvailable = unptrUint32(ocEgress.Lag.State.TablesAvailable)
				swState.CriticalResources.ACLStats.Egress.Lag.TablesUsed = unptrUint32(ocEgress.Lag.State.TablesUsed)
			}

			if ocEgress.Port != nil && ocEgress.Port.State != nil {
				reg.CriticalResources.ACLStats.GroupsAvailable.WithLabelValues("egress", "port").Set(uint32ptrAsFloat64(ocEgress.Port.State.GroupsAvailable))
				reg.CriticalResources.ACLStats.GroupsUsed.WithLabelValues("egress", "port").Set(uint32ptrAsFloat64(ocEgress.Port.State.GroupsUsed))
				reg.CriticalResources.ACLStats.TablesAvailable.WithLabelValues("egress", "port").Set(uint32ptrAsFloat64(ocEgress.Port.State.TablesAvailable))
				reg.CriticalResources.ACLStats.TablesUsed.WithLabelValues("egress", "port").Set(uint32ptrAsFloat64(ocEgress.Port.State.TablesUsed))

				swState.CriticalResources.ACLStats.Egress.Port.GroupsAvailable = unptrUint32(ocEgress.Port.State.GroupsAvailable)
				swState.CriticalResources.ACLStats.Egress.Port.GroupsUsed = unptrUint32(ocEgress.Port.State.GroupsUsed)
				swState.CriticalResources.ACLStats.Egress.Port.TablesAvailable = unptrUint32(ocEgress.Port.State.TablesAvailable)
				swState.CriticalResources.ACLStats.Egress.Port.TablesUsed = unptrUint32(ocEgress.Port.State.TablesUsed)
			}

			if ocEgress.Rif != nil && ocEgress.Rif.State != nil {
				reg.CriticalResources.ACLStats.GroupsAvailable.WithLabelValues("egress", "rif").Set(uint32ptrAsFloat64(ocEgress.Rif.State.GroupsAvailable))
				reg.CriticalResources.ACLStats.GroupsUsed.WithLabelValues("egress", "rif").Set(uint32ptrAsFloat64(ocEgress.Rif.State.GroupsUsed))
				reg.CriticalResources.ACLStats.TablesAvailable.WithLabelValues("egress", "rif").Set(uint32ptrAsFloat64(ocEgress.Rif.State.TablesAvailable))
				reg.CriticalResources.ACLStats.TablesUsed.WithLabelValues("egress", "rif").Set(uint32ptrAsFloat64(ocEgress.Rif.State.TablesUsed))

				swState.CriticalResources.ACLStats.Egress.RIF.GroupsAvailable = unptrUint32(ocEgress.Rif.State.GroupsAvailable)
				swState.CriticalResources.ACLStats.Egress.RIF.GroupsUsed = unptrUint32(ocEgress.Rif.State.GroupsUsed)
				swState.CriticalResources.ACLStats.Egress.RIF.TablesAvailable = unptrUint32(ocEgress.Rif.State.TablesAvailable)
				swState.CriticalResources.ACLStats.Egress.RIF.TablesUsed = unptrUint32(ocEgress.Rif.State.TablesUsed)
			}

			if ocEgress.Switch != nil && ocEgress.Switch.State != nil {
				reg.CriticalResources.ACLStats.GroupsAvailable.WithLabelValues("egress", "switch").Set(uint32ptrAsFloat64(ocEgress.Switch.State.GroupsAvailable))
				reg.CriticalResources.ACLStats.GroupsUsed.WithLabelValues("egress", "switch").Set(uint32ptrAsFloat64(ocEgress.Switch.State.GroupsUsed))
				reg.CriticalResources.ACLStats.TablesAvailable.WithLabelValues("egress", "switch").Set(uint32ptrAsFloat64(ocEgress.Switch.State.TablesAvailable))
				reg.CriticalResources.ACLStats.TablesUsed.WithLabelValues("egress", "switch").Set(uint32ptrAsFloat64(ocEgress.Switch.State.TablesUsed))

				swState.CriticalResources.ACLStats.Egress.Switch.GroupsAvailable = unptrUint32(ocEgress.Switch.State.GroupsAvailable)
				swState.CriticalResources.ACLStats.Egress.Switch.GroupsUsed = unptrUint32(ocEgress.Switch.State.GroupsUsed)
				swState.CriticalResources.ACLStats.Egress.Switch.TablesAvailable = unptrUint32(ocEgress.Switch.State.TablesAvailable)
				swState.CriticalResources.ACLStats.Egress.Switch.TablesUsed = unptrUint32(ocEgress.Switch.State.TablesUsed)
			}

			if ocEgress.Vlan != nil && ocEgress.Vlan.State != nil {
				reg.CriticalResources.ACLStats.GroupsAvailable.WithLabelValues("egress", "vlan").Set(uint32ptrAsFloat64(ocEgress.Vlan.State.GroupsAvailable))
				reg.CriticalResources.ACLStats.GroupsUsed.WithLabelValues("egress", "vlan").Set(uint32ptrAsFloat64(ocEgress.Vlan.State.GroupsUsed))
				reg.CriticalResources.ACLStats.TablesAvailable.WithLabelValues("egress", "vlan").Set(uint32ptrAsFloat64(ocEgress.Vlan.State.TablesAvailable))
				reg.CriticalResources.ACLStats.TablesUsed.WithLabelValues("egress", "vlan").Set(uint32ptrAsFloat64(ocEgress.Vlan.State.TablesUsed))

				swState.CriticalResources.ACLStats.Egress.VLAN.GroupsAvailable = unptrUint32(ocEgress.Vlan.State.GroupsAvailable)
				swState.CriticalResources.ACLStats.Egress.VLAN.GroupsUsed = unptrUint32(ocEgress.Vlan.State.GroupsUsed)
				swState.CriticalResources.ACLStats.Egress.VLAN.TablesAvailable = unptrUint32(ocEgress.Vlan.State.TablesAvailable)
				swState.CriticalResources.ACLStats.Egress.VLAN.TablesUsed = unptrUint32(ocEgress.Vlan.State.TablesUsed)
			}
		}

		if sys.Crm.AclStatistics.Ingress != nil {
			ocIngress := sys.Crm.AclStatistics.Ingress

			if ocIngress.Lag != nil && ocIngress.Lag.State != nil {
				reg.CriticalResources.ACLStats.GroupsAvailable.WithLabelValues("ingress", "lag").Set(uint32ptrAsFloat64(ocIngress.Lag.State.GroupsAvailable))
				reg.CriticalResources.ACLStats.GroupsUsed.WithLabelValues("ingress", "lag").Set(uint32ptrAsFloat64(ocIngress.Lag.State.GroupsUsed))
				reg.CriticalResources.ACLStats.TablesAvailable.WithLabelValues("ingress", "lag").Set(uint32ptrAsFloat64(ocIngress.Lag.State.TablesAvailable))
				reg.CriticalResources.ACLStats.TablesUsed.WithLabelValues("ingress", "lag").Set(uint32ptrAsFloat64(ocIngress.Lag.State.TablesUsed))

				swState.CriticalResources.ACLStats.Ingress.Lag.GroupsAvailable = unptrUint32(ocIngress.Lag.State.GroupsAvailable)
				swState.CriticalResources.ACLStats.Ingress.Lag.GroupsUsed = unptrUint32(ocIngress.Lag.State.GroupsUsed)
				swState.CriticalResources.ACLStats.Ingress.Lag.TablesAvailable = unptrUint32(ocIngress.Lag.State.TablesAvailable)
				swState.CriticalResources.ACLStats.Ingress.Lag.TablesUsed = unptrUint32(ocIngress.Lag.State.TablesUsed)
			}

			if ocIngress.Port != nil && ocIngress.Port.State != nil {
				reg.CriticalResources.ACLStats.GroupsAvailable.WithLabelValues("ingress", "port").Set(uint32ptrAsFloat64(ocIngress.Port.State.GroupsAvailable))
				reg.CriticalResources.ACLStats.GroupsUsed.WithLabelValues("ingress", "port").Set(uint32ptrAsFloat64(ocIngress.Port.State.GroupsUsed))
				reg.CriticalResources.ACLStats.TablesAvailable.WithLabelValues("ingress", "port").Set(uint32ptrAsFloat64(ocIngress.Port.State.TablesAvailable))
				reg.CriticalResources.ACLStats.TablesUsed.WithLabelValues("ingress", "port").Set(uint32ptrAsFloat64(ocIngress.Port.State.TablesUsed))

				swState.CriticalResources.ACLStats.Ingress.Port.GroupsAvailable = unptrUint32(ocIngress.Port.State.GroupsAvailable)
				swState.CriticalResources.ACLStats.Ingress.Port.GroupsUsed = unptrUint32(ocIngress.Port.State.GroupsUsed)
				swState.CriticalResources.ACLStats.Ingress.Port.TablesAvailable = unptrUint32(ocIngress.Port.State.TablesAvailable)
				swState.CriticalResources.ACLStats.Ingress.Port.TablesUsed = unptrUint32(ocIngress.Port.State.TablesUsed)
			}

			if ocIngress.Rif != nil && ocIngress.Rif.State != nil {
				reg.CriticalResources.ACLStats.GroupsAvailable.WithLabelValues("ingress", "rif").Set(uint32ptrAsFloat64(ocIngress.Rif.State.GroupsAvailable))
				reg.CriticalResources.ACLStats.GroupsUsed.WithLabelValues("ingress", "rif").Set(uint32ptrAsFloat64(ocIngress.Rif.State.GroupsUsed))
				reg.CriticalResources.ACLStats.TablesAvailable.WithLabelValues("ingress", "rif").Set(uint32ptrAsFloat64(ocIngress.Rif.State.TablesAvailable))
				reg.CriticalResources.ACLStats.TablesUsed.WithLabelValues("ingress", "rif").Set(uint32ptrAsFloat64(ocIngress.Rif.State.TablesUsed))

				swState.CriticalResources.ACLStats.Ingress.RIF.GroupsAvailable = unptrUint32(ocIngress.Rif.State.GroupsAvailable)
				swState.CriticalResources.ACLStats.Ingress.RIF.GroupsUsed = unptrUint32(ocIngress.Rif.State.GroupsUsed)
				swState.CriticalResources.ACLStats.Ingress.RIF.TablesAvailable = unptrUint32(ocIngress.Rif.State.TablesAvailable)
				swState.CriticalResources.ACLStats.Ingress.RIF.TablesUsed = unptrUint32(ocIngress.Rif.State.TablesUsed)
			}

			if ocIngress.Switch != nil && ocIngress.Switch.State != nil {
				reg.CriticalResources.ACLStats.GroupsAvailable.WithLabelValues("ingress", "switch").Set(uint32ptrAsFloat64(ocIngress.Switch.State.GroupsAvailable))
				reg.CriticalResources.ACLStats.GroupsUsed.WithLabelValues("ingress", "switch").Set(uint32ptrAsFloat64(ocIngress.Switch.State.GroupsUsed))
				reg.CriticalResources.ACLStats.TablesAvailable.WithLabelValues("ingress", "switch").Set(uint32ptrAsFloat64(ocIngress.Switch.State.TablesAvailable))
				reg.CriticalResources.ACLStats.TablesUsed.WithLabelValues("ingress", "switch").Set(uint32ptrAsFloat64(ocIngress.Switch.State.TablesUsed))

				swState.CriticalResources.ACLStats.Ingress.Switch.GroupsAvailable = unptrUint32(ocIngress.Switch.State.GroupsAvailable)
				swState.CriticalResources.ACLStats.Ingress.Switch.GroupsUsed = unptrUint32(ocIngress.Switch.State.GroupsUsed)
				swState.CriticalResources.ACLStats.Ingress.Switch.TablesAvailable = unptrUint32(ocIngress.Switch.State.TablesAvailable)
				swState.CriticalResources.ACLStats.Ingress.Switch.TablesUsed = unptrUint32(ocIngress.Switch.State.TablesUsed)
			}

			if ocIngress.Vlan != nil && ocIngress.Vlan.State != nil {
				reg.CriticalResources.ACLStats.GroupsAvailable.WithLabelValues("ingress", "vlan").Set(uint32ptrAsFloat64(ocIngress.Vlan.State.GroupsAvailable))
				reg.CriticalResources.ACLStats.GroupsUsed.WithLabelValues("ingress", "vlan").Set(uint32ptrAsFloat64(ocIngress.Vlan.State.GroupsUsed))
				reg.CriticalResources.ACLStats.TablesAvailable.WithLabelValues("ingress", "vlan").Set(uint32ptrAsFloat64(ocIngress.Vlan.State.TablesAvailable))
				reg.CriticalResources.ACLStats.TablesUsed.WithLabelValues("ingress", "vlan").Set(uint32ptrAsFloat64(ocIngress.Vlan.State.TablesUsed))

				swState.CriticalResources.ACLStats.Ingress.VLAN.GroupsAvailable = unptrUint32(ocIngress.Vlan.State.GroupsAvailable)
				swState.CriticalResources.ACLStats.Ingress.VLAN.GroupsUsed = unptrUint32(ocIngress.Vlan.State.GroupsUsed)
				swState.CriticalResources.ACLStats.Ingress.VLAN.TablesAvailable = unptrUint32(ocIngress.Vlan.State.TablesAvailable)
				swState.CriticalResources.ACLStats.Ingress.VLAN.TablesUsed = unptrUint32(ocIngress.Vlan.State.TablesUsed)
			}
		}
	}

	if sys.Crm.Statistics != nil && sys.Crm.Statistics.State != nil {
		ocStats := sys.Crm.Statistics.State

		reg.CriticalResources.Stats.DnatEntriesAvailable.Set(uint32ptrAsFloat64(ocStats.DnatEntriesAvailable))
		reg.CriticalResources.Stats.DnatEntriesUsed.Set(uint32ptrAsFloat64(ocStats.DnatEntriesUsed))
		reg.CriticalResources.Stats.FdbEntriesAvailable.Set(uint32ptrAsFloat64(ocStats.FdbEntriesAvailable))
		reg.CriticalResources.Stats.FdbEntriesUsed.Set(uint32ptrAsFloat64(ocStats.FdbEntriesUsed))
		reg.CriticalResources.Stats.IpmcEntriesAvailable.Set(uint32ptrAsFloat64(ocStats.IpmcEntriesAvailable))
		reg.CriticalResources.Stats.IpmcEntriesUsed.Set(uint32ptrAsFloat64(ocStats.IpmcEntriesUsed))
		reg.CriticalResources.Stats.Ipv4NeighborsAvailable.Set(uint32ptrAsFloat64(ocStats.Ipv4NeighborsAvailable))
		reg.CriticalResources.Stats.Ipv4NeighborsUsed.Set(uint32ptrAsFloat64(ocStats.Ipv4NeighborsUsed))
		reg.CriticalResources.Stats.Ipv4NexthopsAvailable.Set(uint32ptrAsFloat64(ocStats.Ipv4NexthopsAvailable))
		reg.CriticalResources.Stats.Ipv4NexthopsUsed.Set(uint32ptrAsFloat64(ocStats.Ipv4NexthopsUsed))
		reg.CriticalResources.Stats.Ipv4RoutesAvailable.Set(uint32ptrAsFloat64(ocStats.Ipv4RoutesAvailable))
		reg.CriticalResources.Stats.Ipv4RoutesUsed.Set(uint32ptrAsFloat64(ocStats.Ipv4RoutesUsed))
		reg.CriticalResources.Stats.Ipv6NeighborsAvailable.Set(uint32ptrAsFloat64(ocStats.Ipv6NeighborsAvailable))
		reg.CriticalResources.Stats.Ipv6NeighborsUsed.Set(uint32ptrAsFloat64(ocStats.Ipv6NeighborsUsed))
		reg.CriticalResources.Stats.Ipv6NexthopsAvailable.Set(uint32ptrAsFloat64(ocStats.Ipv6NexthopsAvailable))
		reg.CriticalResources.Stats.Ipv6NexthopsUsed.Set(uint32ptrAsFloat64(ocStats.Ipv6NexthopsUsed))
		reg.CriticalResources.Stats.Ipv6RoutesAvailable.Set(uint32ptrAsFloat64(ocStats.Ipv6RoutesAvailable))
		reg.CriticalResources.Stats.Ipv6RoutesUsed.Set(uint32ptrAsFloat64(ocStats.Ipv6RoutesUsed))
		reg.CriticalResources.Stats.NexthopGroupMembersAvailable.Set(uint32ptrAsFloat64(ocStats.NexthopGroupMembersAvailable))
		reg.CriticalResources.Stats.NexthopGroupMembersUsed.Set(uint32ptrAsFloat64(ocStats.NexthopGroupMembersUsed))
		reg.CriticalResources.Stats.NexthopGroupsAvailable.Set(uint32ptrAsFloat64(ocStats.NexthopGroupsAvailable))
		reg.CriticalResources.Stats.NexthopGroupsUsed.Set(uint32ptrAsFloat64(ocStats.NexthopGroupsUsed))
		reg.CriticalResources.Stats.SnatEntriesAvailable.Set(uint32ptrAsFloat64(ocStats.SnatEntriesAvailable))
		reg.CriticalResources.Stats.SnatEntriesUsed.Set(uint32ptrAsFloat64(ocStats.SnatEntriesUsed))

		swState.CriticalResources.Stats.DnatEntriesAvailable = unptrUint32(ocStats.DnatEntriesAvailable)
		swState.CriticalResources.Stats.DnatEntriesUsed = unptrUint32(ocStats.DnatEntriesUsed)
		swState.CriticalResources.Stats.FdbEntriesAvailable = unptrUint32(ocStats.FdbEntriesAvailable)
		swState.CriticalResources.Stats.FdbEntriesUsed = unptrUint32(ocStats.FdbEntriesUsed)
		swState.CriticalResources.Stats.IpmcEntriesAvailable = unptrUint32(ocStats.IpmcEntriesAvailable)
		swState.CriticalResources.Stats.IpmcEntriesUsed = unptrUint32(ocStats.IpmcEntriesUsed)
		swState.CriticalResources.Stats.Ipv4NeighborsAvailable = unptrUint32(ocStats.Ipv4NeighborsAvailable)
		swState.CriticalResources.Stats.Ipv4NeighborsUsed = unptrUint32(ocStats.Ipv4NeighborsUsed)
		swState.CriticalResources.Stats.Ipv4NexthopsAvailable = unptrUint32(ocStats.Ipv4NexthopsAvailable)
		swState.CriticalResources.Stats.Ipv4NexthopsUsed = unptrUint32(ocStats.Ipv4NexthopsUsed)
		swState.CriticalResources.Stats.Ipv4RoutesAvailable = unptrUint32(ocStats.Ipv4RoutesAvailable)
		swState.CriticalResources.Stats.Ipv4RoutesUsed = unptrUint32(ocStats.Ipv4RoutesUsed)
		swState.CriticalResources.Stats.Ipv6NeighborsAvailable = unptrUint32(ocStats.Ipv6NeighborsAvailable)
		swState.CriticalResources.Stats.Ipv6NeighborsUsed = unptrUint32(ocStats.Ipv6NeighborsUsed)
		swState.CriticalResources.Stats.Ipv6NexthopsAvailable = unptrUint32(ocStats.Ipv6NexthopsAvailable)
		swState.CriticalResources.Stats.Ipv6NexthopsUsed = unptrUint32(ocStats.Ipv6NexthopsUsed)
		swState.CriticalResources.Stats.Ipv6RoutesAvailable = unptrUint32(ocStats.Ipv6RoutesAvailable)
		swState.CriticalResources.Stats.Ipv6RoutesUsed = unptrUint32(ocStats.Ipv6RoutesUsed)
		swState.CriticalResources.Stats.NexthopGroupMembersAvailable = unptrUint32(ocStats.NexthopGroupMembersAvailable)
		swState.CriticalResources.Stats.NexthopGroupMembersUsed = unptrUint32(ocStats.NexthopGroupMembersUsed)
		swState.CriticalResources.Stats.NexthopGroupsAvailable = unptrUint32(ocStats.NexthopGroupsAvailable)
		swState.CriticalResources.Stats.NexthopGroupsUsed = unptrUint32(ocStats.NexthopGroupsUsed)
		swState.CriticalResources.Stats.SnatEntriesAvailable = unptrUint32(ocStats.SnatEntriesAvailable)
		swState.CriticalResources.Stats.SnatEntriesUsed = unptrUint32(ocStats.SnatEntriesUsed)
	}

	return nil
}

func boolToFloat64(b *bool) float64 {
	if b != nil && *b {
		return 1
	}

	return 0
}

func unptrUint64(u *uint64) uint64 {
	if u != nil {
		return *u
	}

	return 0
}

func unptrUint32(u *uint32) uint32 {
	if u != nil {
		return *u
	}

	return 0
}

func unptrFloat64(f *float64) float64 {
	if f != nil {
		return *f
	}

	return 0
}

func uint32ptrAsFloat64(u *uint32) float64 {
	if u != nil {
		return float64(*u)
	}

	return 0
}

func mapAdminStatus(in oc.E_OpenconfigInterfaces_Interfaces_Interface_State_AdminStatus) (agentapi.AdminStatus, error) {
	switch in {
	case oc.OpenconfigInterfaces_Interfaces_Interface_State_AdminStatus_UNSET:
		return agentapi.AdminStatusUnset, nil
	case oc.OpenconfigInterfaces_Interfaces_Interface_State_AdminStatus_UP:
		return agentapi.AdminStatusUp, nil
	case oc.OpenconfigInterfaces_Interfaces_Interface_State_AdminStatus_DOWN:
		return agentapi.AdminStatusDown, nil
	case oc.OpenconfigInterfaces_Interfaces_Interface_State_AdminStatus_TESTING:
		return agentapi.AdminStatusTesting, nil
	default:
		return agentapi.AdminStatusUnset, errors.Errorf("unknown admin status from gnmi: %d", in)
	}
}

func mapOperStatus(in oc.E_OpenconfigInterfaces_Interfaces_Interface_State_OperStatus) (agentapi.OperStatus, error) {
	switch in {
	case oc.OpenconfigInterfaces_Interfaces_Interface_State_OperStatus_UNSET:
		return agentapi.OperStatusUnset, nil
	case oc.OpenconfigInterfaces_Interfaces_Interface_State_OperStatus_UP:
		return agentapi.OperStatusUp, nil
	case oc.OpenconfigInterfaces_Interfaces_Interface_State_OperStatus_DOWN:
		return agentapi.OperStatusDown, nil
	case oc.OpenconfigInterfaces_Interfaces_Interface_State_OperStatus_TESTING:
		return agentapi.OperStatusTesting, nil
	case oc.OpenconfigInterfaces_Interfaces_Interface_State_OperStatus_UNKNOWN:
		return agentapi.OperStatusUnknown, nil
	case oc.OpenconfigInterfaces_Interfaces_Interface_State_OperStatus_DORMANT:
		return agentapi.OperStatusDormant, nil
	case oc.OpenconfigInterfaces_Interfaces_Interface_State_OperStatus_NOT_PRESENT:
		return agentapi.OperStatusNotPresent, nil
	case oc.OpenconfigInterfaces_Interfaces_Interface_State_OperStatus_LOWER_LAYER_DOWN:
		return agentapi.OperStatusLowerLayerDown, nil
	default:
		return agentapi.OperStatusUnset, errors.Errorf("unknown oper status from gnmi: %d", in)
	}
}

func mapBGPPeerType(in oc.E_OpenconfigBgp_PeerType) (agentapi.BGPPeerType, error) {
	switch in {
	case oc.OpenconfigBgp_PeerType_UNSET:
		return agentapi.BGPPeerTypeUnset, nil
	case oc.OpenconfigBgp_PeerType_INTERNAL:
		return agentapi.BGPPeerTypeInternal, nil
	case oc.OpenconfigBgp_PeerType_EXTERNAL:
		return agentapi.BGPPeerTypeExternal, nil
	default:
		return agentapi.BGPPeerTypeInternal, errors.Errorf("unknown bgp peer type from gnmi: %d", in)
	}
}

func mapBGPNeighborSessionState(in oc.E_OpenconfigBgp_Bgp_Neighbors_Neighbor_State_SessionState) (agentapi.BGPNeighborSessionState, error) {
	switch in {
	case oc.OpenconfigBgp_Bgp_Neighbors_Neighbor_State_SessionState_UNSET:
		return agentapi.BGPNeighborSessionStateUnset, nil
	case oc.OpenconfigBgp_Bgp_Neighbors_Neighbor_State_SessionState_IDLE:
		return agentapi.BGPNeighborSessionStateIdle, nil
	case oc.OpenconfigBgp_Bgp_Neighbors_Neighbor_State_SessionState_CONNECT:
		return agentapi.BGPNeighborSessionStateConnect, nil
	case oc.OpenconfigBgp_Bgp_Neighbors_Neighbor_State_SessionState_ACTIVE:
		return agentapi.BGPNeighborSessionStateActive, nil
	case oc.OpenconfigBgp_Bgp_Neighbors_Neighbor_State_SessionState_OPENSENT:
		return agentapi.BGPNeighborSessionStateOpenSent, nil
	case oc.OpenconfigBgp_Bgp_Neighbors_Neighbor_State_SessionState_OPENCONFIRM:
		return agentapi.BGPNeighborSessionStateOpenConfirm, nil
	case oc.OpenconfigBgp_Bgp_Neighbors_Neighbor_State_SessionState_ESTABLISHED:
		return agentapi.BGPNeighborSessionStateEstablished, nil
	default:
		return agentapi.BGPNeighborSessionStateUnset, errors.Errorf("unknown bgp neighbor session state from gnmi: %d", in)
	}
}

func mapComponentOperStatus(in oc.E_OpenconfigPlatformTypes_COMPONENT_OPER_STATUS) (string, error) {
	switch in {
	case oc.OpenconfigPlatformTypes_COMPONENT_OPER_STATUS_UNSET:
		return "", nil
	case oc.OpenconfigPlatformTypes_COMPONENT_OPER_STATUS_ACTIVE:
		return "active", nil
	case oc.OpenconfigPlatformTypes_COMPONENT_OPER_STATUS_INACTIVE:
		return "inactive", nil
	case oc.OpenconfigPlatformTypes_COMPONENT_OPER_STATUS_DISABLED:
		return "disabled", nil
	default:
		return "", errors.Errorf("unknown component oper status from gnmi: %d", in)
	}
}

func normPower(power *float64) *float64 {
	if power == nil {
		return nil
	}

	if math.IsInf(*power, 0) || math.IsNaN(*power) {
		return nil
	}

	return power
}

func normBias(bias *float64) float64 {
	if bias == nil {
		return 0
	}

	if math.IsInf(*bias, 0) || math.IsNaN(*bias) {
		return 0
	}

	return *bias
}

func normBreakoutName(transceiverName string) (string, bool) {
	if strings.Count(transceiverName, "/") == 2 {
		if strings.HasSuffix(transceiverName, "/1") {
			transceiverName = strings.TrimSuffix(transceiverName, "/1")
		} else {
			return "", false
		}
	}

	return transceiverName, true
}
