package bootstrap

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	statusRunning = "running"
	statusOK      = "ok"
	statusFailed  = "failed"
)

type Reporter struct {
	w    io.Writer
	JSON bool
	next int
}

type Phase struct {
	Number  int    `json:"number"`
	Total   int    `json:"total"`
	Name    string `json:"phase"`
	Status  string `json:"status"`
	Detail  string `json:"detail,omitempty"`
	Elapsed string `json:"elapsed,omitempty"`
	Event   string `json:"event,omitempty"`
}

func NewReporter(w io.Writer, jsonMode bool) *Reporter { return &Reporter{w: w, JSON: jsonMode} }

// Planned reports a dry run without invoking any provisioning functions.
func (r *Reporter) Planned(names []string) error {
	for i, name := range names {
		if err := r.emit(Phase{Number: i + 1, Total: len(names), Name: name, Status: "planned", Event: "phase"}); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reporter) Run(total int, name string, fn func() (string, error)) error {
	r.next++
	started := time.Now()
	if err := r.emit(Phase{Number: r.next, Total: total, Name: name, Status: statusRunning, Event: "phase"}); err != nil {
		return err
	}

	detail, err := fn()
	status := statusOK
	if err != nil {
		status = statusFailed
		detail = err.Error()
	}
	if emitErr := r.emit(Phase{Number: r.next, Total: total, Name: name, Status: status, Detail: detail, Elapsed: time.Since(started).Round(time.Second).String(), Event: "phase"}); err == nil {
		err = emitErr
	}
	return err
}

// Detail reports a substep inside the current phase.
func (r *Reporter) Detail(name, detail string) error {
	return r.emit(Phase{Name: name, Status: "detail", Detail: detail, Event: "detail"})
}

// Output reports output returned by a remote bootstrap script.
func (r *Reporter) Output(name, stream, output string) error {
	if strings.TrimSpace(output) == "" {
		return nil
	}
	return r.emit(Phase{Name: name, Status: stream, Detail: output, Event: "output"})
}

func (r *Reporter) emit(p Phase) error {
	if r.JSON {
		b, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(r.w, string(b))
		return err
	}
	if p.Event == "detail" {
		_, err := fmt.Fprintf(r.w, "           → %-24s %s\n", p.Name, strings.ReplaceAll(p.Detail, "\n", "\n             "))
		return err
	}
	if p.Event == "output" {
		prefix := "stdout"
		if p.Status == "stderr" {
			prefix = "stderr"
		}
		if _, err := fmt.Fprintf(r.w, "           %s:\n", prefix); err != nil {
			return err
		}
		_, err := fmt.Fprintf(r.w, "             %s\n", strings.ReplaceAll(strings.TrimSuffix(p.Detail, "\n"), "\n", "\n             "))
		return err
	}
	if p.Status == statusRunning {
		_, err := fmt.Fprintf(r.w, "[%02d/%02d] %s ...\n", p.Number, p.Total, p.Name)
		return err
	}
	mark := "✓"
	if p.Status == statusFailed {
		mark = "✗"
	}
	_, err := fmt.Fprintf(r.w, "[%02d/%02d] %s %s", p.Number, p.Total, mark, p.Name)
	if p.Detail != "" {
		_, _ = fmt.Fprintf(r.w, " — %s", p.Detail)
	}
	if p.Elapsed != "" {
		_, _ = fmt.Fprintf(r.w, " [%s]", p.Elapsed)
	}
	_, err = fmt.Fprintln(r.w)
	return err
}
