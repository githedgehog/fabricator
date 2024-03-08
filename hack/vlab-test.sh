#!/bin/bash
# Copyright 2023 Hedgehog
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


set -ex

function control {
    bin/hhfab vlab ssh --vm control-1 PATH=/opt/bin "$@"
}

sleep 10 # give agent ctrl a chance to process everything
control kubectl get agent -o wide
control kubectl wait --for=condition=Applied --timeout=3600s agent/switch-1 agent/switch-2
control kubectl get agent -o wide

control kubectl fabric vpc create --name vpc-1 --subnet 10.90.1.1/24
control kubectl fabric vpc create --name vpc-2 --subnet 10.90.2.1/24

control kubectl fabric vpc attach --vpc vpc-1 --conn server-1--mclag--switch-1--switch-2
control kubectl fabric vpc attach --vpc vpc-2 --conn server-2--mclag--switch-1--switch-2

control kubectl fabric vpc peer --vpc vpc-1 --vpc vpc-2

sleep 10 # give agent ctrl a chance to process everything
control kubectl wait --for=condition=Applied --timeout=3600s agent/switch-1 agent/switch-2
control kubectl get agent -o wide
