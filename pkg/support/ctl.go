// Copyright 2025 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dustin/go-humanize"
	"go.githedgehog.com/fabric/pkg/hhfctl/inspect"
	"go.githedgehog.com/fabric/pkg/util/kubeutil"
	fabapi "go.githedgehog.com/fabricator/api/fabricator/v1beta1"
	"go.githedgehog.com/fabricator/pkg/fab"
	kclient "sigs.k8s.io/controller-runtime/pkg/client"
	kyaml "sigs.k8s.io/yaml"
)

type DumpHelpers struct {
	path string
	d    *Dump
}

func LoadSupportDump(workDir, name string) (*DumpHelpers, error) {
	if name == "" {
		slog.Debug("No dump file name provided, looking for the latest one")

		lastName := ""
		entries, err := os.ReadDir(workDir)
		if err != nil {
			return nil, fmt.Errorf("reading work dir: %w", err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			if filepath.Ext(entry.Name()) == FileExt {
				lastName = entry.Name()
			}
		}

		name = lastName
	}
	if name == "" {
		return nil, fmt.Errorf("no dump file found in %s", workDir)
	}

	path := filepath.Join(workDir, name)
	slog.Info("Using support dump", "path", path)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading dump file: %w", err)
	}

	d, err := Unmarshal(data)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling dump file: %w", err)
	}

	return &DumpHelpers{path: path, d: d}, nil
}

func (h *DumpHelpers) loadResources() (kclient.Reader, error) {
	scheme, err := kubeutil.NewScheme(schemeBuilders...)
	if err != nil {
		return nil, fmt.Errorf("creating scheme: %w", err)
	}

	client, err := loadObjects(scheme, bytes.NewReader(h.d.Resources))
	if err != nil {
		return nil, fmt.Errorf("loading kube resources: %w", err)
	}

	return client, nil
}

func (h *DumpHelpers) Info(_ context.Context) error {
	slog.Info("Created",
		"hostname", h.d.Hostname,
		"time", humanize.Time(h.d.Time.Time),
		"hhfab", h.d.HHFabVersion,
	)
	if h.d.OSRelease != "" {
		for line := range strings.Lines(h.d.OSRelease) {
			parts := strings.SplitN(line, "=", 2)

			val := strings.TrimSpace(parts[1])
			val = strings.Trim(val, "\"")
			slog.Info("/etc/os-release", parts[0], val)
		}
	}

	return nil
}

func (h *DumpHelpers) Versions(ctx context.Context) error {
	kube, err := h.loadResources()
	if err != nil {
		return err
	}

	f, _, _, err := fab.GetFabAndNodes(ctx, kube, fab.GetFabAndNodesOpts{
		AllowNotHydrated: true, // TODO
	})
	if err != nil {
		return fmt.Errorf("getting fab and nodes: %w", err)
	}

	data, err := kyaml.Marshal(f.Status.Versions)
	if err != nil {
		return fmt.Errorf("marshalling versions: %w", err)
	}

	fmt.Println(string(data))

	return nil
}

func (h *DumpHelpers) Status(ctx context.Context) error {
	kube, err := h.loadResources()
	if err != nil {
		return err
	}

	f, _, _, err := fab.GetFabAndNodes(ctx, kube, fab.GetFabAndNodesOpts{
		AllowNotHydrated: true, // TODO
	})
	if err != nil {
		return fmt.Errorf("getting fab and nodes: %w", err)
	}

	f.Status.Versions = fabapi.Versions{} // TODO: remove versions from status?
	data, err := kyaml.Marshal(f.Status)
	if err != nil {
		return fmt.Errorf("marshalling status: %w", err)
	}

	fmt.Println(string(data))

	return nil
}

func (h *DumpHelpers) Config(ctx context.Context) error {
	kube, err := h.loadResources()
	if err != nil {
		return err
	}

	// TODO deduplicate with ConfigExport
	f, controls, nodes, err := fab.GetFabAndNodes(ctx, kube)
	if err != nil {
		return fmt.Errorf("getting fabricator and control nodes: %w", err)
	}

	slices.SortFunc(controls, func(a, b fabapi.ControlNode) int {
		return cmp.Compare(a.Name, b.Name)
	})

	slices.SortFunc(nodes, func(a, b fabapi.FabNode) int {
		return cmp.Compare(a.Name, b.Name)
	})

	out := os.Stdout

	if err := kubeutil.PrintObject(&f, out, false); err != nil {
		return fmt.Errorf("printing fabricator: %w", err)
	}

	for _, c := range controls {
		_, err := fmt.Fprintf(out, "---\n")
		if err != nil {
			return fmt.Errorf("writing separator: %w", err)
		}

		if err := kubeutil.PrintObject(&c, out, false); err != nil {
			return fmt.Errorf("printing control node: %w", err)
		}
	}

	for _, n := range nodes {
		_, err := fmt.Fprintf(out, "---\n")
		if err != nil {
			return fmt.Errorf("writing separator: %w", err)
		}

		if err := kubeutil.PrintObject(&n, out, false); err != nil {
			return fmt.Errorf("printing node: %w", err)
		}
	}

	return nil
}

func InspectRun[TIn inspect.In, TOut inspect.Out](ctx context.Context, h *DumpHelpers, useNow bool, f inspect.Func[TIn, TOut], args inspect.Args, in TIn, w io.Writer) error {
	kube, err := h.loadResources()
	if err != nil {
		return err
	}

	outType := inspect.OutputTypeText
	if args.Output != inspect.OutputTypeUndefined {
		outType = args.Output
	}

	if !slices.Contains(inspect.OutputTypes, outType) {
		return fmt.Errorf("invalid output type: %s", outType)
	}

	out, err := f(ctx, kube, in)
	if err != nil {
		return fmt.Errorf("inspecting function: %w", err)
	}

	if useNow {
		slog.Info("Using current time for inspects")
	} else {
		slog.Info("Using time from the dump file as a 'current' time")
	}

	now := h.d.Time.Time
	if useNow || now.IsZero() {
		now = time.Now()
	}

	return inspect.Render(now, args.Output, w, out)
}

func (h *DumpHelpers) PodLogs(ctx context.Context, qNs, qPod, qCont string) error {
	isList := qNs == "" || qPod == ""

	for _, ns := range slices.Sorted(maps.Keys(h.d.PodLogs)) {
		nsLogs := h.d.PodLogs[ns]

		if qNs != "" && ns != qNs {
			continue
		}

		for _, pod := range slices.Sorted(maps.Keys(nsLogs)) {
			podLogs := nsLogs[pod]

			if qPod != "" && pod != qPod {
				continue
			}

			for _, cont := range slices.Sorted(maps.Keys(podLogs)) {
				contLogs := podLogs[cont]

				if qCont != "" && cont != qCont {
					continue
				}

				slog.Info("Logs", "namespace", ns, "pod", pod, "container", cont)

				if !isList {
					fmt.Println(string(contLogs.Current))

					return nil
				}
			}
		}
	}

	if !isList {
		return fmt.Errorf("no logs found for %s/%s/%s", qNs, qPod, qCont)
	}

	return nil
}
