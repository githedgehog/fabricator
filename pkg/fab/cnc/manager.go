package cnc

import (
	"compress/gzip"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/mholt/archiver/v4"
	"github.com/pkg/errors"
	"github.com/urfave/cli/v2"
	"go.githedgehog.com/fabric/pkg/wiring"
	"go.githedgehog.com/fabric/pkg/wiring/sample"
	"go.githedgehog.com/fabricator/pkg/fab/cnc/bin"
	fabwiring "go.githedgehog.com/fabricator/pkg/fab/wiring"
	"golang.org/x/exp/slices"
	"sigs.k8s.io/yaml"
)

type Preset string

type Bundle struct {
	Name        string
	IsInstaller bool
}

type Stage uint8

type Component interface {
	Name() string
	IsEnabled(preset Preset) bool
	Flags() []cli.Flag

	// set defaults and validate saveable config
	// called on each init/load, should set values the makes sense for user to change
	// e.g. if we need to set TLS SAN to some value for system to work, it shouldn't be set here
	// e.g. if we need some things to be available for user to change, it should be set here
	// e.g. if we want other components to be able to use some values of that component config - it should be here
	// e.g. generate TLS certificates
	// if we want to make sure that same value is used on every build - it should be set here
	Hydrate(preset Preset) error

	// TODO rename run -> build, install -> run?
	Build(basedir string, preset Preset, get GetComponent, wiring *wiring.Data, run AddBuildOp, install AddRunOp) error
}

type (
	GetComponent func(string) Component
	AddBuildOp   func(bundle Bundle, stage Stage, name string, op BuildOp)
	AddRunOp     func(bundle Bundle, stage Stage, name string, op RunOp)
)

type BuildOp interface {
	Hydrate() error
	Build(basedir string) error
	RunOps() []RunOp
}

type RunOp interface {
	Hydrate() error
	Summary() string
	Run(basedir string) error
}

type Manager struct {
	basedir    string
	preset     Preset
	wiring     *wiring.Data
	presets    []Preset
	bundles    []Bundle
	maxStage   Stage
	components []Component

	addedBuildOps map[string]any
	addedRunOps   map[string]any
}

func New(presets []Preset, bundles []Bundle, maxStage Stage, components []Component) *Manager {
	mngr := &Manager{
		presets:    presets,
		bundles:    bundles,
		maxStage:   maxStage,
		components: components,
	}

	return mngr
}

func (mngr *Manager) Flags() []cli.Flag {
	res := []cli.Flag{}
	for _, comp := range mngr.components {
		res = append(res, comp.Flags()...)
	}

	return res
}

func (mngr *Manager) prepare() error {
	if mngr.basedir == "" {
		return errors.New("basedir is empty")
	}
	if mngr.preset == "" {
		return errors.New("preset is empty")
	}
	if mngr.wiring == nil {
		return errors.New("wiring is empty")
	}

	for _, comp := range mngr.components {
		if !comp.IsEnabled(mngr.preset) {
			continue
		}

		err := comp.Hydrate(mngr.preset)
		if err != nil {
			return errors.Wrapf(err, "error hydrating component %s", comp.Name())
		}
	}

	return nil
}

func (mngr *Manager) Init(basedir string, fromConfig string, preset Preset, wiringPath string, wiringGenType string, wiringGenPreset string, hydrate bool) error {
	if _, err := os.Stat(basedir); err == nil {
		if !os.IsNotExist(err) {
			return errors.Errorf("basedir %s already exists, please, remove it first", basedir)
		}
	}

	mngr.basedir = basedir

	if wiringPath != "" && wiringGenType != "" {
		return errors.New("wiring path and wiring gen are mutually exclusive")
	}

	if wiringPath != "" {
		slog.Info("Loading wiring", "from", wiringPath)
		wiring, err := wiring.LoadDataFrom(wiringPath)
		if err != nil {
			return errors.Wrapf(err, "error loading wiring from %s", wiringPath)
		}
		mngr.wiring = wiring
	}

	if wiringGenType == "" {
		wiringGenType = "collapsedcore"
	}
	if wiringGenPreset == "" {
		wiringGenPreset = string(preset)
	}

	ok := false
	for _, preset := range sample.PresetsAll {
		if sample.Preset(wiringGenPreset) == preset {
			ok = true
			break
		}
	}
	if !ok {
		return errors.Errorf("unknown wiring preset: %s", wiringGenPreset)
	}

	if wiringPath == "" && wiringGenType != "" {
		if wiringGenType != "collapsedcore" {
			return errors.Errorf("unknown wiring sample: %s", wiringGenType)
		}
		slog.Info("Generating wiring", "type", wiringGenType, "preset", wiringGenPreset)
		data, err := sample.CollapsedCore(sample.Preset(wiringGenPreset))
		if err != nil {
			return errors.Wrapf(err, "error generating wiring sample %s", wiringGenType)
		}
		mngr.wiring = data
	}

	if wiringPath == "" && wiringGenType == "" {
		return errors.New("wiring path or wiring gen must be specified")
	}

	if fromConfig != "" {
		slog.Info("Loading existing config", "from", fromConfig)
		err := mngr.loadConfig(fromConfig)
		if err != nil {
			return errors.Wrapf(err, "error existing loading config")
		}

		if mngr.preset != "" && preset != mngr.preset {
			return errors.Errorf("loaded config preset mismatch: %s != %s", preset, mngr.preset)
		}
	}

	mngr.preset = preset

	if !slices.Contains(mngr.presets, preset) {
		return fmt.Errorf("unknown preset: %s", preset)
	}

	// TODO rework wiring generation & handling

	if err := fabwiring.IsHydrated(mngr.wiring); err != nil {
		err = errors.Wrapf(err, "error validating wiring")

		if hydrate {
			slog.Warn("Wiring is not hydrated, hydrating", "err", err)

			// TODO make configurable
			if err := fabwiring.Hydrate(mngr.wiring, fabwiring.HydrateConfig{
				Subnet:       "172.30.0.0/16",
				SpineASN:     65100,
				LeafASNStart: 65101,
			}); err != nil {
				return errors.Wrapf(err, "error hydrating wiring")
			}
		} else {
			return err
		}
	}

	if err := mngr.prepare(); err != nil {
		return errors.Wrapf(err, "error preparing")
	}

	slog.Info("Initialized", "preset", mngr.preset,
		"config", filepath.Join(mngr.basedir, "config.yaml"),
		"wiring", filepath.Join(mngr.basedir, "wiring.yaml"))

	return nil
}

type ManagerSaver struct {
	Preset Preset         `json:"preset,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}

func (mngr *Manager) Save() error {
	err := os.MkdirAll(mngr.basedir, 0o755)
	if err != nil {
		return errors.Wrapf(err, "error creating basedir %s", mngr.basedir)
	}

	data, err := mngr.configData()
	if err != nil {
		return errors.Wrapf(err, "error getting config data")
	}

	err = os.WriteFile(filepath.Join(mngr.basedir, "config.yaml"), data, 0o644)
	if err != nil {
		return errors.Wrapf(err, "error writing config")
	}

	err = mngr.wiring.SaveTo(filepath.Join(mngr.basedir, "wiring.yaml"))
	if err != nil {
		return errors.Wrapf(err, "error saving wiring")
	}

	return nil
}

func (mngr *Manager) configData() ([]byte, error) {
	saver := &ManagerSaver{
		Preset: mngr.preset,
		Config: map[string]any{},
	}

	for _, comp := range mngr.components {
		if !comp.IsEnabled(mngr.preset) {
			continue
		}

		saver.Config[comp.Name()] = comp
	}

	data, err := yaml.Marshal(saver)
	if err != nil {
		return nil, errors.Wrapf(err, "error marshaling config")
	}

	return data, nil
}

func (mngr *Manager) loadConfig(fromConfig string) error {
	data, err := os.ReadFile(fromConfig)
	if err != nil {
		return errors.Wrapf(err, "error reading config")
	}

	saver := &ManagerSaver{}
	err = yaml.Unmarshal(data, saver)
	if err != nil {
		return errors.Wrapf(err, "error unmarshaling config")
	}

	mngr.preset = saver.Preset

	for idx, comp := range mngr.components {
		if !comp.IsEnabled(mngr.preset) {
			continue
		}

		if parsed, exist := saver.Config[comp.Name()]; exist {
			data, err := yaml.Marshal(parsed)
			if err != nil {
				return errors.Wrapf(err, "error marshaling config for component %s", comp.Name())
			}
			err = yaml.Unmarshal(data, &mngr.components[idx])
			if err != nil {
				return errors.Wrapf(err, "error unmarshaling config for component %s", comp.Name())
			}
		}
	}

	return nil
}

func (mngr *Manager) Load(basedir string) error {
	mngr.basedir = basedir

	err := mngr.loadConfig(filepath.Join(basedir, "config.yaml"))
	if err != nil {
		return errors.Wrapf(err, "error loading config")
	}

	wiringData, err := wiring.LoadDataFrom(filepath.Join(basedir, "wiring.yaml"))
	if err != nil {
		return errors.Wrapf(err, "error loading wiring")
	}
	mngr.wiring = wiringData

	err = mngr.prepare()
	if err != nil {
		return errors.Wrapf(err, "error preparing")
	}

	return nil
}

func (mngr *Manager) Build(pack bool) error {
	start := time.Now()

	actions := map[Bundle][][]recipeContext{}
	for _, bundle := range mngr.bundles {
		actions[bundle] = make([][]recipeContext, mngr.maxStage)

		basedir := filepath.Join(mngr.basedir, bundle.Name)
		err := os.MkdirAll(basedir, 0o755)
		if err != nil {
			return errors.Wrapf(err, "error creating bundle dir %s", basedir)
		}

		if bundle.IsInstaller {
			err = bin.WriteRunBin(basedir)
			if err != nil {
				return errors.Wrapf(err, "error writing run bin")
			}
		}
	}

	for _, comp := range mngr.components {
		if !comp.IsEnabled(mngr.preset) {
			continue
		}

		slog.Info("Building", "component", comp.Name())

		adder := &opAdder{mngr: mngr}
		err := comp.Build(mngr.basedir, mngr.preset, mngr.getComponent, mngr.wiring, adder.addBuildOp, adder.addRunOp)
		if err != nil {
			return errors.Wrapf(err, "error building component %s", comp.Name())
		}
		if adder.err != nil {
			return errors.Wrapf(adder.err, "error building component %s (adder)", comp.Name())
		}

		for _, runOp := range adder.actions {
			err = runOp.op.Hydrate()
			if err != nil {
				return errors.Wrapf(err, "error hydrating run op %s", runOp.name)
			}

			actions[runOp.bundle][int(runOp.stage)] = append(actions[runOp.bundle][int(runOp.stage)], runOp)
		}

		slog.Debug("Finished", "component", comp.Name())
	}

	for _, bundle := range mngr.bundles {
		if !bundle.IsInstaller {
			continue
		}

		slog.Info("Creating recipe", "bundle", bundle.Name)

		recipe := &Recipe{}

		for stage := 0; stage < int(mngr.maxStage); stage++ {
			for _, action := range actions[bundle][stage] {
				slog.Info("Planned", "bundle", bundle.Name, "name", action.name, "op", action.op.Summary())
				recipe.Actions = append(recipe.Actions, RecipeAction{
					Name: action.name,
					Op:   action.op,
				})
			}
		}

		err := recipe.Save(filepath.Join(mngr.basedir, bundle.Name))
		if err != nil {
			return errors.Wrapf(err, "error saving recipe for bundle %s", bundle.Name)
		}

		err = recipe.Load(filepath.Join(mngr.basedir, bundle.Name))
		if err != nil {
			return errors.Wrapf(err, "error loading recipe for bundle %s", bundle.Name)
		}

		slog.Info("Recipe created", "bundle", bundle.Name, "actions", len(recipe.Actions))
	}

	slog.Info("Building done", "took", time.Since(start))

	if pack {
		return errors.Wrapf(mngr.Pack(), "error packing bundles")
	}

	return nil
}

func (mngr *Manager) Pack() error {
	start := time.Now()

	for _, bundle := range mngr.bundles {
		if !bundle.IsInstaller {
			continue
		}

		target := bundle.Name + ".tgz"

		slog.Info("Packing", "bundle", bundle.Name, "target", target)

		files, err := archiver.FilesFromDisk(nil, map[string]string{
			filepath.Join(mngr.basedir, bundle.Name): bundle.Name,
		})
		if err != nil {
			return errors.Wrapf(err, "error getting files for bundle %s", bundle.Name)
		}

		out, err := os.Create(filepath.Join(mngr.basedir, target))
		if err != nil {
			return errors.Wrapf(err, "error creating target %s", target)
		}
		defer out.Close()

		format := archiver.CompressedArchive{
			Compression: archiver.Gz{
				Multithreaded:    true,
				CompressionLevel: gzip.BestSpeed,
			},
			Archival: archiver.Tar{},
		}

		err = format.Archive(context.Background(), out, files)
		if err != nil {
			return errors.Wrapf(err, "error archiving bundle %s", bundle.Name)
		}
	}

	slog.Info("Packing done", "took", time.Since(start))

	return nil
}

func (mngr *Manager) getComponent(name string) Component {
	for _, comp := range mngr.components {
		if !comp.IsEnabled(mngr.preset) {
			continue
		}

		if comp.Name() == name {
			return comp
		}
	}

	return nil
}

type opAdder struct {
	mngr    *Manager
	err     error
	actions []recipeContext
}

type recipeContext struct {
	bundle Bundle
	stage  Stage
	name   string
	op     RunOp
}

var (
	goodNameRegexp = `^[A-Za-z0-9-_]+$`
	isGoodName     = regexp.MustCompile(goodNameRegexp).MatchString
)

func (adder *opAdder) validate(bundle Bundle, stage Stage, name string, addedOps map[string]any) bool {
	if !slices.Contains(adder.mngr.bundles, bundle) {
		adder.err = errors.Errorf("unknown bundle: %s", bundle.Name)
		return false
	}

	if stage >= adder.mngr.maxStage {
		adder.err = errors.Errorf("unknown stage: %d", stage)
		return false
	}

	if name == "" {
		adder.err = errors.New("name is empty")
		return false
	}
	if !isGoodName(name) {
		adder.err = errors.Errorf("invalid name '%s', should be %s", name, goodNameRegexp)
		return false
	}
	if len(name) < 3 || len(name) > 64 {
		adder.err = errors.Errorf("invalid name '%s', should be 3-64 chars", name)
		return false
	}

	if _, exist := addedOps[name]; exist {
		adder.err = errors.Errorf("duplicate added op name: %s", name)
		return false
	}

	return true
}

func (adder *opAdder) addBuildOp(bundle Bundle, stage Stage, name string, op BuildOp) {
	if adder.err != nil {
		return
	}
	if !adder.validate(bundle, stage, name, adder.mngr.addedBuildOps) {
		return
	}

	slog.Debug("Adding build op", "bundle", bundle.Name, "stage", stage, "name", name)

	err := op.Hydrate()
	if err != nil {
		adder.err = errors.Wrapf(err, "error hydrating build op %s", name)
		return
	}

	err = op.Build(filepath.Join(adder.mngr.basedir, bundle.Name))
	if err != nil {
		adder.err = errors.Wrapf(err, "error building op %s", name)
		return
	}

	runOps := op.RunOps()
	if len(runOps) > 0 && !bundle.IsInstaller {
		adder.err = errors.Errorf("build op %s has run ops but bundle %s is not installer", name, bundle.Name)
		return
	}

	for _, runOp := range runOps {
		adder.actions = append(adder.actions, recipeContext{
			bundle: bundle,
			stage:  stage,
			name:   name,
			op:     runOp,
		})
	}
}

func (adder *opAdder) addRunOp(bundle Bundle, stage Stage, name string, op RunOp) {
	if adder.err != nil {
		return
	}
	if !adder.validate(bundle, stage, name, adder.mngr.addedRunOps) {
		return
	}

	slog.Debug("Adding run op", "bundle", bundle.Name, "stage", stage, "name", name)

	if !bundle.IsInstaller {
		adder.err = errors.Errorf("build op %s has run ops but bundle %s is not installer", name, bundle.Name)
		return
	}

	adder.actions = append(adder.actions, recipeContext{
		bundle: bundle,
		stage:  stage,
		name:   name,
		op:     op,
	})
}

func (mngr *Manager) Dump() error {
	slog.Info("Dumping hydrated config")

	mngr.wiring = nil

	data, err := mngr.configData()
	if err != nil {
		return errors.Wrapf(err, "error getting config data")
	}

	fmt.Println()
	fmt.Println(string(data))

	return nil
}

func (mngr *Manager) Wiring() *wiring.Data {
	return mngr.wiring
}

func (mngr *Manager) Preset() Preset {
	return mngr.preset
}
