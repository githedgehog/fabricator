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

package switchprofile

import (
	"go.githedgehog.com/fabric/api/meta"
	wiringapi "go.githedgehog.com/fabric/api/wiring/v1beta1"
	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var CelesticaDS2000 = wiringapi.SwitchProfile{
	ObjectMeta: kmetav1.ObjectMeta{
		Name: "celestica-ds2000",
	},
	Spec: wiringapi.SwitchProfileSpec{
		DisplayName:   "Celestica DS2000",
		OtherNames:    []string{"Celestica Questone 2a"},
		SwitchSilicon: SiliconBroadcomTD3_X5,
		Features: wiringapi.SwitchProfileFeatures{
			Subinterfaces: true,
			ACLs:          true,
			L2VNI:         true,
			L3VNI:         true,
			RoCE:          false,
			MCLAG:         true,
			ESLAG:         true,
			ECMPRoCEQPN:   false,
		},
		NOSType:  meta.NOSTypeSONiCBCMBase,
		Platform: "x86_64-cel_ds2000-r0",
		Config:   wiringapi.SwitchProfileConfig{},
		Ports: map[string]wiringapi.SwitchProfilePort{
			"M1":    {NOSName: "Management0", Management: true, OniePortName: "eth0"},
			"E1/1":  {NOSName: "Ethernet0", Label: "1", Profile: "SFP28-25G"},
			"E1/2":  {NOSName: "Ethernet1", Label: "2", Profile: "SFP28-25G"},
			"E1/3":  {NOSName: "Ethernet2", Label: "3", Profile: "SFP28-25G"},
			"E1/4":  {NOSName: "Ethernet3", Label: "4", Profile: "SFP28-25G"},
			"E1/5":  {NOSName: "Ethernet4", Label: "5", Profile: "SFP28-25G"},
			"E1/6":  {NOSName: "Ethernet5", Label: "6", Profile: "SFP28-25G"},
			"E1/7":  {NOSName: "Ethernet6", Label: "7", Profile: "SFP28-25G"},
			"E1/8":  {NOSName: "Ethernet7", Label: "8", Profile: "SFP28-25G"},
			"E1/9":  {NOSName: "Ethernet8", Label: "9", Profile: "SFP28-25G"},
			"E1/10": {NOSName: "Ethernet9", Label: "10", Profile: "SFP28-25G"},
			"E1/11": {NOSName: "Ethernet10", Label: "11", Profile: "SFP28-25G"},
			"E1/12": {NOSName: "Ethernet11", Label: "12", Profile: "SFP28-25G"},
			"E1/13": {NOSName: "Ethernet12", Label: "13", Profile: "SFP28-25G"},
			"E1/14": {NOSName: "Ethernet13", Label: "14", Profile: "SFP28-25G"},
			"E1/15": {NOSName: "Ethernet14", Label: "15", Profile: "SFP28-25G"},
			"E1/16": {NOSName: "Ethernet15", Label: "16", Profile: "SFP28-25G"},
			"E1/17": {NOSName: "Ethernet16", Label: "17", Profile: "SFP28-25G"},
			"E1/18": {NOSName: "Ethernet17", Label: "18", Profile: "SFP28-25G"},
			"E1/19": {NOSName: "Ethernet18", Label: "19", Profile: "SFP28-25G"},
			"E1/20": {NOSName: "Ethernet19", Label: "20", Profile: "SFP28-25G"},
			"E1/21": {NOSName: "Ethernet20", Label: "21", Profile: "SFP28-25G"},
			"E1/22": {NOSName: "Ethernet21", Label: "22", Profile: "SFP28-25G"},
			"E1/23": {NOSName: "Ethernet22", Label: "23", Profile: "SFP28-25G"},
			"E1/24": {NOSName: "Ethernet23", Label: "24", Profile: "SFP28-25G"},
			"E1/25": {NOSName: "Ethernet24", Label: "25", Profile: "SFP28-25G"},
			"E1/26": {NOSName: "Ethernet25", Label: "26", Profile: "SFP28-25G"},
			"E1/27": {NOSName: "Ethernet26", Label: "27", Profile: "SFP28-25G"},
			"E1/28": {NOSName: "Ethernet27", Label: "28", Profile: "SFP28-25G"},
			"E1/29": {NOSName: "Ethernet28", Label: "29", Profile: "SFP28-25G"},
			"E1/30": {NOSName: "Ethernet29", Label: "30", Profile: "SFP28-25G"},
			"E1/31": {NOSName: "Ethernet30", Label: "31", Profile: "SFP28-25G"},
			"E1/32": {NOSName: "Ethernet31", Label: "32", Profile: "SFP28-25G"},
			"E1/33": {NOSName: "Ethernet32", Label: "33", Profile: "SFP28-25G"},
			"E1/34": {NOSName: "Ethernet33", Label: "34", Profile: "SFP28-25G"},
			"E1/35": {NOSName: "Ethernet34", Label: "35", Profile: "SFP28-25G"},
			"E1/36": {NOSName: "Ethernet35", Label: "36", Profile: "SFP28-25G"},
			"E1/37": {NOSName: "Ethernet36", Label: "37", Profile: "SFP28-25G"},
			"E1/38": {NOSName: "Ethernet37", Label: "38", Profile: "SFP28-25G"},
			"E1/39": {NOSName: "Ethernet38", Label: "39", Profile: "SFP28-25G"},
			"E1/40": {NOSName: "Ethernet39", Label: "40", Profile: "SFP28-25G"},
			"E1/41": {NOSName: "Ethernet40", Label: "41", Profile: "SFP28-25G"},
			"E1/42": {NOSName: "Ethernet41", Label: "42", Profile: "SFP28-25G"},
			"E1/43": {NOSName: "Ethernet42", Label: "43", Profile: "SFP28-25G"},
			"E1/44": {NOSName: "Ethernet43", Label: "44", Profile: "SFP28-25G"},
			"E1/45": {NOSName: "Ethernet44", Label: "45", Profile: "SFP28-25G"},
			"E1/46": {NOSName: "Ethernet45", Label: "46", Profile: "SFP28-25G"},
			"E1/47": {NOSName: "Ethernet46", Label: "47", Profile: "SFP28-25G"},
			"E1/48": {NOSName: "Ethernet47", Label: "48", Profile: "SFP28-25G"},
			"E1/49": {NOSName: "1/49", BaseNOSName: "Ethernet48", Label: "49", Profile: "QSFP28-100G"},
			"E1/50": {NOSName: "Ethernet52", Label: "50", Profile: "QSFP28-100G" + wiringapi.NonBreakoutPortExceptionSuffix},
			"E1/51": {NOSName: "Ethernet56", Label: "51", Profile: "QSFP28-100G" + wiringapi.NonBreakoutPortExceptionSuffix},
			"E1/52": {NOSName: "Ethernet60", Label: "52", Profile: "QSFP28-100G" + wiringapi.NonBreakoutPortExceptionSuffix},
			"E1/53": {NOSName: "Ethernet64", Label: "53", Profile: "QSFP28-100G" + wiringapi.NonBreakoutPortExceptionSuffix},
			"E1/54": {NOSName: "Ethernet68", Label: "54", Profile: "QSFP28-100G" + wiringapi.NonBreakoutPortExceptionSuffix},
			"E1/55": {NOSName: "Ethernet72", Label: "55", Profile: "QSFP28-100G" + wiringapi.NonBreakoutPortExceptionSuffix},
			"E1/56": {NOSName: "1/56", BaseNOSName: "Ethernet76", Label: "56", Profile: "QSFP28-100G"},
		},
		PortProfiles: map[string]wiringapi.SwitchProfilePortProfile{
			"SFP28-25G": {
				Speed: &wiringapi.SwitchProfilePortProfileSpeed{
					Default:   "25G",
					Supported: []string{"10G", "25G"},
				},
			},
			"QSFP28-100G" + wiringapi.NonBreakoutPortExceptionSuffix: {
				Speed: &wiringapi.SwitchProfilePortProfileSpeed{
					Default:   "100G",
					Supported: []string{"40G", "100G"},
				},
			},
			"QSFP28-100G": {
				Breakout: &wiringapi.SwitchProfilePortProfileBreakout{
					Default: "1x100G",
					Supported: map[string]wiringapi.SwitchProfilePortProfileBreakoutMode{
						"1x40G":  {Offsets: []string{"0"}},
						"1x100G": {Offsets: []string{"0"}},
						"4x25G":  {Offsets: []string{"0", "1", "2", "3"}},
						"4x10G":  {Offsets: []string{"0", "1", "2", "3"}},
					},
				},
			},
		},
	},
}
