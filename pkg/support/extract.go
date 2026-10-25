// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package support

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	kmetav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/duration"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	kyaml "sigs.k8s.io/yaml"
)

const (
	extractDirPerm  = 0o755
	extractFilePerm = 0o600
)

// ExtractSummary is what an extracted support dump has, written into its metadata file
type ExtractSummary struct {
	Objects    int            `json:"objects"`            // Collected objects
	Kinds      map[string]int `json:"kinds,omitempty"`    // Objects per kind of our own API groups, as their directories are named
	Containers int            `json:"containers"`         // Containers with logs
	LogFiles   int            `json:"logFiles"`           // Log files, the current and the previous ones
	Gateways   int            `json:"gateways"`           // Gateways with insights
	Commands   int            `json:"commands"`           // Insight commands across the gateways
	Errors     int            `json:"errors,omitempty"`   // Insight commands that failed
	Unparsed   int            `json:"unparsed,omitempty"` // Objects that couldn't be parsed, extracted as is
}

type extractedDump struct {
	Dump    `json:",inline"`
	Summary ExtractSummary `json:"summary"`
}

type ExtractOpts struct {
	OutDir      string // Directory to extract into, defaults to the dump path without its extension, must not exist yet
	StripColors bool   // Strip the ANSI color codes from the pod logs and gateway insights, so they read and grep as text
	ASCII       bool   // Replace the box drawing and other decorations in the pod logs and gateway insights with ASCII
}

// Extract unpacks the support dump at path into a tree of files under opts.OutDir:
//
//   - support.yaml: the dump metadata, i.e. its version, name, creator and creation time, and a summary of what it has
//   - resources/<domain>/<version>[.<group>].<Kind>/[<namespace>/]<name>.yaml: every collected object, grouped by the
//     domain of its API group, e.g. resources/githedgehog.com/v1beta1.vpc.githedgehog.com.VPC/default/vpc-1.yaml
//   - podlogs/<namespace>/<pod>/<container>.{current,previous}.log: the current and the previous logs of every container
//   - insights/<gateway>/<command>.{stdout,stderr,error}.txt: the output of every command run on the gateways
func Extract(path string, opts ExtractOpts) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("opening dump: %w", err)
	}
	defer f.Close()

	dump := &Dump{}
	if err := Unmarshal(f, dump); err != nil {
		return fmt.Errorf("reading dump: %w", err)
	}

	createdAt := dump.CreatedAt.UTC().Format(time.RFC3339)
	if !dump.CreatedAt.IsZero() {
		createdAt += " (" + duration.HumanDuration(time.Since(dump.CreatedAt.Time)) + " ago)"
	}
	slog.Info("Support dump", "name", dump.Name, "version", dump.Version, "createdAt", createdAt,
		"host", dump.CreatedBy.Hostname, "user", dump.CreatedBy.Username, "ctlVersion", dump.CreatedBy.CtlVersion)

	outDir := opts.OutDir
	if outDir == "" {
		outDir = strings.TrimSuffix(path, FileExt)
		if outDir == path {
			outDir += ".extracted"
		}
	}
	if _, err := os.Stat(outDir); err == nil {
		return fmt.Errorf("output directory %q already exists", outDir) //nolint:err113
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("checking output directory: %w", err)
	}

	summary := ExtractSummary{Kinds: map[string]int{}}

	if err := extractResources(dump.Resources, filepath.Join(outDir, "resources"), &summary); err != nil {
		return err
	}
	kinds := slices.Collect(maps.Keys(summary.Kinds))
	slices.SortFunc(kinds, func(a, b string) int {
		return cmp.Or(cmp.Compare(summary.Kinds[b], summary.Kinds[a]), strings.Compare(a, b))
	})
	slog.Info("Extracted resources", "objects", summary.Objects, "ownKinds", kindsSummary(kinds, summary.Kinds))
	if summary.Unparsed > 0 {
		slog.Warn("Some objects couldn't be parsed and are extracted as is", "count", summary.Unparsed)
	}

	if err := extractPodLogs(dump.PodLogs, filepath.Join(outDir, "podlogs"), opts, &summary); err != nil {
		return err
	}
	slog.Info("Extracted pod logs", "namespaces", len(dump.PodLogs), "containers", summary.Containers, "files", summary.LogFiles)

	if err := extractInsights(dump.GatewayInsights, filepath.Join(outDir, "insights"), opts, &summary); err != nil {
		return err
	}
	slog.Info("Extracted gateway insights", "gateways", summary.Gateways, "commands", summary.Commands, "failed", summary.Errors)

	meta := extractedDump{Dump: *dump, Summary: summary}
	meta.Resources = ""
	meta.PodLogs = nil
	meta.GatewayInsights = nil
	data, err := kyaml.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshalling dump metadata: %w", err)
	}
	if err := writeExtracted(filepath.Join(outDir, "support.yaml"), data); err != nil {
		return err
	}

	slog.Info("Support dump extracted", "path", outDir)

	return nil
}

// kindsSummary lists the kinds by their names without the group version, which are unique within our own API groups
func kindsSummary(sorted []string, counts map[string]int) string {
	res := make([]string, 0, len(sorted))
	for _, kind := range sorted {
		name := kind[strings.LastIndex(kind, ".")+1:]
		res = append(res, fmt.Sprintf("%s=%d", name, counts[kind]))
	}

	return strings.Join(res, " ")
}

type extractedObject struct {
	kmetav1.TypeMeta `json:",inline"`
	Metadata         kmetav1.ObjectMeta `json:"metadata"`
}

func extractResources(resources, dir string, summary *ExtractSummary) error {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(strings.NewReader(resources)))
	for {
		doc, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading resources: %w", err)
		}
		if strings.TrimSpace(string(doc)) == "" {
			continue
		}

		obj := &extractedObject{}
		err = kyaml.Unmarshal(doc, obj)
		gv, gvErr := schema.ParseGroupVersion(obj.APIVersion)
		if err != nil || gvErr != nil || gv.Version == "" || obj.Kind == "" || obj.Metadata.Name == "" {
			summary.Unparsed++
			if err := writeExtracted(filepath.Join(dir, "unparsed", fmt.Sprintf("%d.yaml", summary.Unparsed)), doc); err != nil {
				return err
			}

			continue
		}

		// the group version named the same as the APIServices, e.g. v1beta1.vpc.githedgehog.com or just v1 for the
		// core group, followed by the kind, e.g. v1beta1.vpc.githedgehog.com.VPC
		kind := gv.Version
		if gv.Group != "" {
			kind += "." + gv.Group
		}
		kind += "." + obj.Kind

		domain := topDomain(gv.Group)
		path := filepath.Join(dir, safeFileName(domain), safeFileName(kind))
		if obj.Metadata.Namespace != "" {
			path = filepath.Join(path, safeFileName(obj.Metadata.Namespace))
		}
		if err := writeExtracted(filepath.Join(path, safeFileName(obj.Metadata.Name)+".yaml"), doc); err != nil {
			return err
		}

		summary.Objects++
		if domain == "githedgehog.com" {
			summary.Kinds[kind]++
		}
	}

	return nil
}

// topDomain returns the domain the API group belongs to, i.e. its last two labels, e.g. githedgehog.com for
// vpc.githedgehog.com, while the core group and the built-in ones without a domain, e.g. apps, are k8s.io's
func topDomain(group string) string {
	labels := strings.Split(group, ".")
	if len(labels) < 2 {
		return "k8s.io"
	}

	return strings.Join(labels[len(labels)-2:], ".")
}

// ansiCodes matches the ANSI escape sequences setting the colors and the text style, e.g. "\x1b[2m" or "\x1b[0m"
var ansiCodes = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// asciiRune replaces the non-ASCII characters found in the pod logs and gateway insights, i.e. the box drawing used
// for the separators and tables by the gateway components and the micro sign in the Go durations, e.g. 12.5µs, which
// is the same as 12.5us; anything else is kept as is
func asciiRune(r rune) rune {
	switch {
	case strings.ContainsRune("─━┄┅┈┉╌╍═", r):
		return '-'
	case strings.ContainsRune("│┃┆┇┊┋╎╏║", r):
		return '|'
	case r >= '─' && r <= '╿': // corners and junctions of the rest of the box drawing block
		return '+'
	case r == '■':
		return '*'
	case r == '✔':
		return 'v'
	case r == 'µ' || r == 'μ':
		return 'u'
	default:
		return r
	}
}

func extractedText(content string, opts ExtractOpts) []byte {
	if opts.StripColors {
		content = ansiCodes.ReplaceAllString(content, "")
	}
	if opts.ASCII {
		content = strings.Map(asciiRune, content)
	}

	return []byte(content)
}

func extractPodLogs(podLogs map[string]map[string]PodLogs, dir string, opts ExtractOpts, summary *ExtractSummary) error {
	for ns, pods := range podLogs {
		for pod, containers := range pods {
			for container, logs := range containers {
				path := filepath.Join(dir, safeFileName(ns), safeFileName(pod), safeFileName(container))
				for suffix, content := range map[string]string{".current.log": logs.Current, ".previous.log": logs.Previous} {
					if content == "" {
						continue
					}
					if err := writeExtracted(path+suffix, extractedText(content, opts)); err != nil {
						return err
					}
					summary.LogFiles++
				}
				summary.Containers++
			}
		}
	}

	return nil
}

func extractInsights(insights map[string]map[string]ExecOutputs, dir string, opts ExtractOpts, summary *ExtractSummary) error {
	for gw, cmds := range insights {
		for cmd, out := range cmds {
			path := filepath.Join(dir, safeFileName(gw), safeFileName(cmd))
			for suffix, content := range map[string]string{".stdout.txt": out.Stdout, ".stderr.txt": out.Stderr, ".error.txt": out.Error} {
				if content == "" {
					continue
				}
				if err := writeExtracted(path+suffix, extractedText(content, opts)); err != nil {
					return err
				}
			}
			summary.Commands++
			if out.Error != "" {
				summary.Errors++
			}
		}
		summary.Gateways++
	}

	return nil
}

var unsafeFileNameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// safeFileName turns a name into one that's fine as a single path element, e.g. "frr: show bgp summary" into
// "frr-show-bgp-summary" or "system:node" into "system-node", while the DNS-like object names stay as is
func safeFileName(name string) string {
	name = strings.Trim(unsafeFileNameChars.ReplaceAllString(name, "-"), "-.")
	if name == "" {
		return "_"
	}

	return name
}

func writeExtracted(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), extractDirPerm); err != nil {
		return fmt.Errorf("creating directory for %q: %w", path, err)
	}
	if err := os.WriteFile(path, data, extractFilePerm); err != nil {
		return fmt.Errorf("writing %q: %w", path, err)
	}

	return nil
}
