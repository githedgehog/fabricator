package cnc

import (
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
