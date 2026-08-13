// Copyright 2026 Hedgehog
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/xml"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type junitCase struct {
	Name    string `xml:"name,attr"`
	Failure *struct {
		Message string `xml:"message,attr"`
	} `xml:"failure"`
	Skipped *struct {
		Message string `xml:"message,attr"`
	} `xml:"skipped"`
}

type junitSuite struct {
	Name      string      `xml:"name,attr"`
	Tests     int         `xml:"tests,attr"`
	Failures  int         `xml:"failures,attr"`
	Skipped   int         `xml:"skipped,attr"`
	TestCases []junitCase `xml:"testcase"`
}

type junitReport struct {
	Suites []junitSuite `xml:"testsuite"`
}

func main() {
	artifactsDir := flag.String("artifacts", "artifacts", "directory to search for release-test.xml files")
	tag := flag.String("tag", "", "release tag this report is evidence for")
	runURL := flag.String("run-url", "", "URL of the CI run that produced this evidence")
	outFile := flag.String("out", "release-test-report.md", "path to write the markdown report")
	evidenceDir := flag.String("evidence-dir", "evidence", "directory to copy uniquely-named xunit files into")
	flag.Parse()

	if *tag == "" {
		log.Fatal("release-evidence: -tag is required")
	}

	if err := os.MkdirAll(*evidenceDir, 0o755); err != nil {
		log.Fatalf("release-evidence: creating evidence dir: %v", err)
	}

	var paths []string
	if err := filepath.WalkDir(*artifactsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "release-test.xml" {
			paths = append(paths, path)
		}

		return nil
	}); err != nil {
		log.Fatalf("release-evidence: walking %s: %v", *artifactsDir, err)
	}
	sort.Strings(paths)

	var sections []string
	for _, path := range paths {
		slug := strings.TrimSuffix(filepath.Base(filepath.Dir(path)), "--test-results")

		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatalf("release-evidence: reading %s: %v", path, err)
		}

		var report junitReport
		if err := xml.Unmarshal(data, &report); err != nil {
			log.Fatalf("release-evidence: parsing %s: %v", path, err)
		}

		var recap, tests []string
		for _, suite := range report.Suites {
			passed := suite.Tests - suite.Skipped - suite.Failures
			recap = append(recap, fmt.Sprintf("%-30s tests=%-4d passed=%-4d skipped=%-4d failed=%-4d", suite.Name, suite.Tests, passed, suite.Skipped, suite.Failures))
			for _, tc := range suite.TestCases {
				switch {
				case tc.Failure != nil:
					line := "  FAIL  " + tc.Name
					if tc.Failure.Message != "" {
						line += " (" + tc.Failure.Message + ")"
					}
					tests = append(tests, line)
				case tc.Skipped != nil:
					line := "  SKIP  " + tc.Name
					if tc.Skipped.Message != "" {
						line += " (" + tc.Skipped.Message + ")"
					}
					tests = append(tests, line)
				default:
					tests = append(tests, "  PASS  "+tc.Name)
				}
			}
		}

		body := strings.Join(recap, "\n") + "\n\ntests:\n" + strings.Join(tests, "\n")
		sections = append(sections, fmt.Sprintf("## %s\n<details>\n<summary>%s Test Results</summary>\n\n```\n%s\n```\n\n</details>", slug, *tag, body))

		dst := filepath.Join(*evidenceDir, slug+"--release-test.xml")
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			log.Fatalf("release-evidence: writing %s: %v", dst, err)
		}
	}

	report := fmt.Sprintf("# Release test evidence for %s\n\nRun: %s\n\n%s\n", *tag, *runURL, strings.Join(sections, "\n\n"))
	if err := os.WriteFile(*outFile, []byte(report), 0o644); err != nil {
		log.Fatalf("release-evidence: writing %s: %v", *outFile, err)
	}
	fmt.Print(report)
}
