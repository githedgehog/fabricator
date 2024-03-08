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

package cnc

import (
	"strings"

	"github.com/pkg/errors"
)

type Ref struct {
	Repo string `json:"repo,omitempty"`
	Name string `json:"name,omitempty"`
	Tag  string `json:"tag,omitempty"`
}

func (ref Ref) StrictValidate() error {
	if ref.Repo == "" {
		return errors.New("repo is empty")
	}
	if ref.Name == "" {
		return errors.New("name is empty")
	}
	if ref.Tag == "" {
		return errors.New("tag is empty")
	}

	return nil
}

func (ref Ref) Fallback(refs ...Ref) Ref {
	for _, fallback := range refs {
		if ref.Repo == "" {
			ref.Repo = fallback.Repo
		}
		if ref.Name == "" {
			ref.Name = fallback.Name
		}
		if ref.Tag == "" {
			ref.Tag = fallback.Tag
		}
	}

	return ref
}

func (ref Ref) RepoName() string {
	return ref.Repo + "/" + ref.Name
}

// TODO maybe rename to smth? and make it work for empty parts
func (ref Ref) String() string {
	return ref.Repo + "/" + ref.Name + ":" + ref.Tag
}

func (ref Ref) IsLocalhost() bool {
	return strings.HasPrefix(ref.Repo, "127.0.0.1:")
}
