package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/PRIEYAN/ButterDB/internal/platform/config"
)

type jsonFinding struct {
	Severity string `json:"severity"`
	Key      string `json:"key"`
	Message  string `json:"message"`
}

type jsonReport struct {
	OK       bool          `json:"ok"`
	Findings []jsonFinding `json:"findings"`
}

func (a *application) runCheckConfig(args []string) int {
	fs, path := a.newFlagSet("check-config")
	asJSON := fs.Bool("json", false, "print the report as one JSON object")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	cfg, findings, _ := a.loadConfig(*path)
	if !hasError(findings) {
		findings = append(findings, configRules(cfg)...)
		findings = append(findings, newHostChecker(a).run(cfg)...)
	}
	errorCount, warningCount := countFindings(findings)
	if *asJSON {
		if err := writeJSONReport(a.stdout, findings, errorCount == 0); err != nil {
			a.fail(err)
			return exitFailure
		}
	} else {
		writeTextReport(a.stdout, findings, errorCount, warningCount)
	}
	if errorCount > 0 {
		return exitUsage
	}
	return exitOK
}

func hasError(findings []config.Finding) bool {
	errorCount, _ := countFindings(findings)
	return errorCount > 0
}

func countFindings(findings []config.Finding) (errorCount, warningCount int) {
	for _, f := range findings {
		switch f.Severity {
		case config.SeverityError:
			errorCount++
		case config.SeverityWarning:
			warningCount++
		}
	}
	return errorCount, warningCount
}

func (a *application) printErrorFindings(findings []config.Finding) {
	for _, f := range findings {
		if f.Severity == config.SeverityError {
			fmt.Fprintf(a.stderr, "%s: %s: %s\n", programName, terminalSafe(f.Key), terminalSafe(f.Message))
		}
	}
}

func writeTextReport(w io.Writer, findings []config.Finding, errorCount, warningCount int) {
	width := 0
	for _, f := range findings {
		width = max(width, len(f.Key))
	}
	for _, f := range findings {
		fmt.Fprintf(w, "%-7s %-*s  %s\n", f.Severity, width, terminalSafe(f.Key), terminalSafe(f.Message))
	}
	fmt.Fprintf(w, "check-config: %s, %s\n", plural(errorCount, "error"), plural(warningCount, "warning"))
}

func plural(count int, noun string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, noun)
	}
	return fmt.Sprintf("%d %ss", count, noun)
}

func writeJSONReport(w io.Writer, findings []config.Finding, ok bool) error {
	report := jsonReport{OK: ok, Findings: make([]jsonFinding, 0, len(findings))}
	for _, f := range findings {
		report.Findings = append(report.Findings, jsonFinding{Severity: string(f.Severity), Key: f.Key, Message: f.Message})
	}
	return json.NewEncoder(w).Encode(report)
}
