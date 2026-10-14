package bootstrap

import (
	"encoding/json"
	"fmt"
	"io"
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
	Number int    `json:"number"`
	Total  int    `json:"total"`
	Name   string `json:"phase"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func NewReporter(w io.Writer, jsonMode bool) *Reporter { return &Reporter{w: w, JSON: jsonMode} }

func (r *Reporter) Run(total int, name string, fn func() (string, error)) error {
	r.next++
	if err := r.emit(Phase{Number: r.next, Total: total, Name: name, Status: statusRunning}); err != nil {
		return err
	}

	detail, err := fn()
	status := statusOK
	if err != nil {
		status = statusFailed
		detail = err.Error()
	}
	if emitErr := r.emit(Phase{Number: r.next, Total: total, Name: name, Status: status, Detail: detail}); err == nil {
		err = emitErr
	}
	return err
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
	if p.Status == statusRunning {
		_, err := fmt.Fprintf(r.w, "[%d/%d] %s ... running\n", p.Number, p.Total, p.Name)
		return err
	}
	_, err := fmt.Fprintf(r.w, "[%d/%d] %s ... %s", p.Number, p.Total, p.Name, p.Status)
	if p.Detail != "" {
		_, _ = fmt.Fprintf(r.w, " (%s)", p.Detail)
	}
	_, err = fmt.Fprintln(r.w)
	return err
}
