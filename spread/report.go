package spread

import (
	"strings"
	"time"
)

const (
	TIME_FORMAT = "2006-01-02T15:04:05.000"
)

type Report struct {
	ExecutionItems   []*Item `json:"items,attr"`
	ExecutionResults Results `json:"results,attr"`
}

type Item struct {
	Start    string `json:"start,attr"`
	End      string `json:"end,attr"`
	Verb     string `json:"verb,attr"`
	Backend  string `json:"backend,attr"`
	System   string `json:"system,attr"`
	Level    string `json:"level,attr"`
	Name     string `json:"name,attr"`
	Variant  string `json:"variant,attr"`
	Instance string `json:"instance,attr"`
	Success  bool   `json:"success,attr"`
	Aborted  bool   `json:"aborted,attr"`
	Skipped  bool   `json:"skipped,attr"`
}

type Results struct {
	TaskPassed           int `json:"task-passed,attr"`
	TaskFailed           int `json:"task-failed,attr"`
	TaskAborted          int `json:"task-aborted,attr"`
	TaskSkipped          int `json:"task-skipped,attr"`
	TaskPrepareFailed    int `json:"task-prepare-failed,attr"`
	TaskRestoreFailed    int `json:"task-restore-failed,attr"`
	SuitePrepareFailed   int `json:"suite-prepare-failed,attr"`
	SuiteRestoreFailed   int `json:"suite-restore-failed,attr"`
	BackendPrepareFailed int `json:"backend-prepare-failed,attr"`
	BackendRestoreFailed int `json:"backend-restore-failed,attr"`
	ProjectPrepareFailed int `json:"project-prepare-failed,attr"`
	ProjectRestoreFailed int `json:"project-restore-failed,attr"`
}

func NewReport() *Report {
	return &Report{
		ExecutionItems:   []*Item{},
		ExecutionResults: Results{},
	}
}

func (r *Report) addItem(verb string, backend string, system string, context string, suite string, task string, variant string, instance string) *Item {
	level := "task"
	name := task
	if strings.HasSuffix(context, "/") {
		level = "suite"
		name = suite
		variant = ""
	} else if strings.HasSuffix(context, system) {
		level = "project"
		name = ""
		variant = ""
	}
	item := &Item{
		Start:    time.Now().Format(TIME_FORMAT),
		End:      "",
		Verb:     verb,
		Backend:  backend,
		System:   system,
		Level:    level,
		Name:     name,
		Variant:  variant,
		Instance: instance,
		Success:  true,
		Aborted:  false,
		Skipped:  false,
	}
	r.ExecutionItems = append(r.ExecutionItems, item)
	return item
}

func (r *Report) addSkippedTask(backend string, system string, task string, variant string) *Item {
	item := &Item{
		Start:    "",
		End:      "",
		Verb:     "",
		Backend:  backend,
		System:   system,
		Name:     task,
		Variant:  variant,
		Instance: "",
		Success:  false,
		Aborted:  false,
		Skipped:  true,
	}
	r.ExecutionItems = append(r.ExecutionItems, item)
	return item
}

func (r *Report) addAbortedTask(backend string, system string, task string, variant string) *Item {
	item := &Item{
		Start:    "",
		End:      "",
		Verb:     "",
		Backend:  backend,
		System:   system,
		Name:     task,
		Variant:  variant,
		Instance: "",
		Success:  false,
		Aborted:  true,
		Skipped:  false,
	}
	r.ExecutionItems = append(r.ExecutionItems, item)
	return item
}

func (r *Report) addTaskResults(passed int, failed int, aborted int, skipped int, prepareFailed int, restoreFailed int) {
	r.ExecutionResults.TaskPassed = passed
	r.ExecutionResults.TaskFailed = failed
	r.ExecutionResults.TaskAborted = aborted
	r.ExecutionResults.TaskSkipped = skipped
	r.ExecutionResults.TaskPrepareFailed = prepareFailed
	r.ExecutionResults.TaskRestoreFailed = restoreFailed
}

func (r *Report) addSuiteResults(prepareFailed int, restoreFailed int) {
	r.ExecutionResults.SuitePrepareFailed = prepareFailed
	r.ExecutionResults.SuiteRestoreFailed = restoreFailed
}

func (r *Report) addBackendResults(prepareFailed int, restoreFailed int) {
	r.ExecutionResults.BackendPrepareFailed = prepareFailed
	r.ExecutionResults.BackendRestoreFailed = restoreFailed
}

func (r *Report) addProjectResults(prepareFailed int, restoreFailed int) {
	r.ExecutionResults.ProjectPrepareFailed = prepareFailed
	r.ExecutionResults.ProjectRestoreFailed = restoreFailed
}

func (i *Item) addStatus(success bool) {
	i.End = time.Now().Format(TIME_FORMAT)
	i.Success = success
}
