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

// Package install is the binary that is run when the core user is logged into the live flatcar image.
package install

// ConfigFile contains info needed for the flacar install to proceed.
const ConfigFile = "flatcar-install.yaml"

// Config wraps the contents of a ConfigFile in a type.
type Config struct {
	Hostname       string   `json:"hostname,omitempty"`
	Username       string   `json:"username,omitempty"`
	PasswordHash   string   `json:"passwordHash,omitempty"`
	AuthorizedKeys []string `json:"authorizedKeys,omitempty"`
}
