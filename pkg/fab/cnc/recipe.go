package cnc

import (
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/exp/slices"
	"sigs.k8s.io/yaml"
)

type Recipe struct {
	Actions []RecipeAction
}

type RecipeAction struct {
	Name string `json:"name,omitempty"`
	Op   RunOp  `json:"op,omitempty"`
}

type RecipeSaver struct {
	Actions []RecipeSaverItem `json:"recipe,omitempty"`
}

type RecipeSaverItem struct {
	Name string `json:"name,omitempty"`
	Type string `json:"action,omitempty"`
	Op   any    `json:"params,omitempty"`
}

func getShortTypeName(a any) string {
	if t := reflect.TypeOf(a); t.Kind() == reflect.Ptr {
		return t.Elem().Name()
	} else {
		return t.Name()
	}
}

func (r *Recipe) Save(basedir string) error {
	saver := &RecipeSaver{}

	for _, action := range r.Actions {
		known := false
		for _, knownOp := range RunOpsList {
			if getShortTypeName(action.Op) == getShortTypeName(knownOp) {
				known = true
				break
			}
		}

		if !known {
			return errors.Errorf("unknown op: %s", getShortTypeName(action.Op))
		}

		saver.Actions = append(saver.Actions, RecipeSaverItem{
			Name: action.Name,
			Type: getShortTypeName(action.Op),
			Op:   action.Op,
		})
	}

	data, err := yaml.Marshal(saver)
	if err != nil {
		return errors.Wrap(err, "error marshaling recipe")
	}

	err = os.WriteFile(filepath.Join(basedir, "recipe.yaml"), data, 0o644)
	if err != nil {
		return errors.Wrap(err, "error writing recipe")
	}

	return nil
}

func (r *Recipe) Load(basedir string) error {
	data, err := os.ReadFile(filepath.Join(basedir, "recipe.yaml"))
	if err != nil {
		return errors.Wrap(err, "error reading recipe")
	}

	saver := &RecipeSaver{}
	err = yaml.UnmarshalStrict(data, saver)
	if err != nil {
		return errors.Wrap(err, "error unmarshaling recipe")
	}

	r.Actions = []RecipeAction{}

	for _, item := range saver.Actions {
		var op RunOp

		for _, knownOp := range RunOpsList {
			if getShortTypeName(knownOp) == item.Type {
				op = reflect.New(reflect.TypeOf(knownOp).Elem()).Interface().(RunOp)
				break
			}
		}

		data, err := yaml.Marshal(item.Op)
		if err != nil {
			return errors.Wrap(err, "error marshaling op")
		}

		err = yaml.UnmarshalStrict(data, op)
		if err != nil {
			return errors.Wrap(err, "error unmarshaling op")
		}

		r.Actions = append(r.Actions, RecipeAction{
			Name: item.Name,
			Op:   op,
		})
	}

	return nil
}

func RunRecipe(basedir string, steps []string, dryRun bool) error {
	if dryRun {
		slog.Warn("Dry run, not actually running anything")
	}

	slog.Info("Running recipe", "basedir", basedir, "steps", strings.Join(steps, " "), "dryRun", dryRun)

	runStart := time.Now()

	recipe := &Recipe{}
	err := recipe.Load(basedir)
	if err != nil {
		return errors.Wrapf(err, "error loading recipe from %s", basedir)
	}

	slog.Debug("Loaded recipe", "actions", len(recipe.Actions))

	for _, action := range recipe.Actions {
		opStart := time.Now()
		if len(steps) == 0 || len(steps) == 1 && steps[0] == "all" || len(steps) > 0 && slices.Contains(steps, action.Name) {
			slog.Info("Running", "name", action.Name, "op", action.Op.Summary())
			if !dryRun {
				err = action.Op.Run(basedir)
				if err != nil {
					return errors.Wrapf(err, "error running action %s", action.Name)
				}
			}
		} else {
			slog.Debug("Skipping", "name", action.Name, "op", action.Op.Summary())
		}
		slog.Debug("Done", "name", action.Name, "op", action.Op.Summary(), "took", time.Since(opStart))
	}

	slog.Info("Done", "took", time.Since(runStart))

	return nil
}
