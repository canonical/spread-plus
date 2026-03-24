package spread_test

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"

	"github.com/canonical/spread-plus/spread"

	. "gopkg.in/check.v1"
	"gopkg.in/yaml.v2"
)

func Test(t *testing.T) { TestingT(t) }

type FilterSuite struct{}

var _ = Suite(&FilterSuite{})

func (s *FilterSuite) TestFilter(c *C) {
	job := &spread.Job{Name: "backend:image:suite/test:variant"}

	pass := []string{
		"backend",
		"backend:",
		"image",
		":image:",
		"suite/test",
		"suit...est",
		"suite/",
		"/test",
		":variant",
		"...",
		"im...",
		"...ge",
	}

	block := []string{
		"nothing",
		"noth...",
		"...hing",
		":backend",
		"suite",
		"test",
	}

	for _, s := range pass {
		f, err := spread.NewFilter([]string{s})
		c.Assert(err, IsNil)
		c.Assert(f.Pass(job), Equals, true, Commentf("Filter: %q", s))
	}

	for _, s := range block {
		f, err := spread.NewFilter([]string{s})
		c.Assert(err, IsNil)
		c.Assert(f.Pass(job), Equals, false, Commentf("Filter: %q", s))
	}
}

type projectSuite struct{}

var _ = Suite(&projectSuite{})

func (s *projectSuite) TestLoad(c *C) {
	spreadYaml := []byte(`project: mock-prj
path: /some/path
backends:
 google:
  key: some-key
  plan: global-plan
  systems:
   - system-1:
   - system-2:
      plan: plan-for-2
   - system-3:
suites:
 tests/:
  summary: mock tests
`)
	tmpdir := c.MkDir()
	err := ioutil.WriteFile(filepath.Join(tmpdir, "spread.yaml"), spreadYaml, 0644)
	c.Assert(err, IsNil)
	err = os.MkdirAll(filepath.Join(tmpdir, "tests"), 0755)
	c.Assert(err, IsNil)

	prj, err := spread.Load(tmpdir)
	c.Assert(err, IsNil)
	backend := prj.Backends["google"]
	c.Check(backend.Name, Equals, "google")
	c.Check(backend.Systems["system-1"].Plan, Equals, "global-plan")
	c.Check(backend.Systems["system-2"].Plan, Equals, "plan-for-2")
	c.Check(backend.Systems["system-3"].Plan, Equals, "global-plan")
}

func (s *projectSuite) TestOptionalInt(c *C) {
	optInts := struct {
		Priority spread.OptionalInt `yaml:"priority"`
		NotSet   spread.OptionalInt `yaml:"not-set"`
	}{}
	inp := []byte("priority: 100")

	err := yaml.Unmarshal(inp, &optInts)
	c.Assert(err, IsNil)
	c.Check(optInts.Priority.IsSet, Equals, true)
	c.Check(optInts.Priority.Value, Equals, int64(100))
	c.Check(optInts.Priority.String(), Equals, "100")

	c.Check(optInts.NotSet.IsSet, Equals, false)
	c.Check(optInts.NotSet.Value, Equals, int64(0))
	c.Check(optInts.NotSet.String(), Equals, "0")
}

func createSuiteWithSystems(project *spread.Project, suiteSys []string, taskSys []string) {
	project.Suites = map[string]*spread.Suite{"suite/": {
		Systems: suiteSys,
		Tasks:   map[string]*spread.Task{"task": {Systems: taskSys, Samples: 1, Suite: "suite/", Name: "suite/task"}}},
	}
}

func (s *projectSuite) TestSupportedSystemsSuiteMoreRestrictiveThanTask(c *C) {
	project := spread.Project{
		RemotePath: "/remote/path",
		Backends: map[string]*spread.Backend{
			"lxd": {
				Name: "lxd",
				Systems: spread.SystemsMap{
					"ubuntu-20.04": &spread.System{Name: "ubuntu-20.04"},
					"ubuntu-22.04": &spread.System{Name: "ubuntu-22.04"},
				},
			}}}

	// If a suite explicitly lists supported systems, only those systems are in the job list
	// even if the suite's tasks support more systems
	createSuiteWithSystems(&project, []string{"ubuntu-20.04", "arch"}, []string{"ub*"})
	jobs, err := project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "lxd:ubuntu-20.04:suite/task")

	// If a suite explicitly lists supported systems, only those systems are in the job list
	// even if the suite's tasks support more systems
	createSuiteWithSystems(&project, []string{"ubuntu-24*"}, []string{"ub*"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, ErrorMatches, `cannot find any tasks`)

	// If a suite excludes systems, those systems do not appear in the job list
	// even if the suite's tasks support more systems
	createSuiteWithSystems(&project, []string{"-ubuntu-20.04"}, []string{"ub*"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "lxd:ubuntu-22.04:suite/task")

	// if a suite excludes all systems, yet a task adds an excluded system, then that system appears in the jobs list
	createSuiteWithSystems(&project, []string{"-ubuntu-*"}, []string{"+ubuntu-22.04"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "lxd:ubuntu-22.04:suite/task")

	// If a suite excludes all systems, then none appear in the job list
	// even if the suite's tasks support more systems
	createSuiteWithSystems(&project, []string{"-ubuntu-*"}, []string{"ubuntu-20.04", "ubuntu-22.04"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, ErrorMatches, `cannot find any tasks`)

	// If a suite does not declare systems, then all task and backend-supported systems appear on the job list
	createSuiteWithSystems(&project, []string{}, []string{})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 2)

	createSuiteWithSystems(&project, []string{"-ubuntu-22*"}, []string{})
	project.Suites["suite/"].Variants = []string{"foo", "bar"}
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 2)
}

func (s *projectSuite) TestSupportedSystemsSuiteLessRestrictiveThanTask(c *C) {
	project := spread.Project{
		RemotePath: "/remote/path",
		Backends: map[string]*spread.Backend{
			"lxd": {
				Name: "lxd",
				Systems: spread.SystemsMap{
					"ubuntu-20.04": &spread.System{Name: "ubuntu-20.04"},
					"ubuntu-22.04": &spread.System{Name: "ubuntu-22.04"},
				},
			}}}

	// If a task only supports a subset of the suite's systems, only those appear in the job list
	createSuiteWithSystems(&project, []string{"ubuntu*"}, []string{"ubuntu-20*"})
	jobs, err := project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "lxd:ubuntu-20.04:suite/task")

	// If a suite excludes systems, and a task excludes others, none of those systems appear on the job list
	createSuiteWithSystems(&project, []string{"-ubuntu-20.04"}, []string{"-ubuntu-22.04"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, ErrorMatches, `cannot find any tasks`)

	// if a suite adds a system, yet a task excludes it, it is not included in the job list
	createSuiteWithSystems(&project, []string{"+ubuntu-22.04"}, []string{"-ubuntu-22.04"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "lxd:ubuntu-20.04:suite/task")

	// If a suite allows all systems, but a task excludes some, those are not included in the job list
	createSuiteWithSystems(&project, []string{"*"}, []string{"-ubuntu-20.04"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "lxd:ubuntu-22.04:suite/task")

	// If a suite does not declare systems, then all task and backend-supported systems appear on the job list
	createSuiteWithSystems(&project, []string{}, []string{"ubuntu-20.04", "arch-linux-64"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "lxd:ubuntu-20.04:suite/task")
}

func createSuiteWithBackends(project *spread.Project, suiteBkends []string, taskBkends []string) {
	project.Suites = map[string]*spread.Suite{"suite/": {
		Backends: suiteBkends,
		Tasks:    map[string]*spread.Task{"task": {Backends: taskBkends, Samples: 1, Suite: "suite/", Name: "suite/task"}}},
	}
}

func (s *projectSuite) TestSupportedBackendsSuitesMoreRestrictiveThanTask(c *C) {
	project := spread.Project{
		RemotePath: "/remote/path",
		Backends: map[string]*spread.Backend{
			"lxd": {
				Name:    "lxd",
				Systems: spread.SystemsMap{"ubuntu-20.04": &spread.System{Name: "ubuntu-20.04"}},
			},
			"qemu": {
				Name:    "qemu",
				Systems: spread.SystemsMap{"ubuntu-22.04": &spread.System{Name: "ubuntu-22.04"}},
			},
		}}

	// If a suite explicitly lists supported backends, only those systems are in the job list
	createSuiteWithBackends(&project, []string{"lx*"}, []string{"lxd", "qemu", "openstack"})
	jobs, err := project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "lxd:ubuntu-20.04:suite/task")

	// If a suite excludes backends, those backends do not appear in the job list
	createSuiteWithBackends(&project, []string{"-lxd"}, []string{})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "qemu:ubuntu-22.04:suite/task")

	// if a suite excludes all backends, yet a task adds an excluded backend, then that backend appears in the jobs list
	createSuiteWithBackends(&project, []string{"-lxd", "-qe*"}, []string{"+qe*"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "qemu:ubuntu-22.04:suite/task")

	// If a suite excludes all backends, then none appear in the job list
	createSuiteWithBackends(&project, []string{"-lxd", "-qe*"}, []string{"lxd", "qemu"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, ErrorMatches, `cannot find any tasks`)
}

func (s *projectSuite) TestSupportedBackendsSuitesLessRestrictiveThanTask(c *C) {
	project := spread.Project{
		RemotePath: "/remote/path",
		Backends: map[string]*spread.Backend{
			"lxd": {
				Name:    "lxd",
				Systems: spread.SystemsMap{"ubuntu-20.04": &spread.System{Name: "ubuntu-20.04"}},
			},
			"qemu": {
				Name:    "qemu",
				Systems: spread.SystemsMap{"ubuntu-22.04": &spread.System{Name: "ubuntu-22.04"}},
			},
		}}

	// If a suite explicitly lists supported backends, only those systems are in the job list
	createSuiteWithBackends(&project, []string{"lx*", "*mu"}, []string{"lxd"})
	jobs, err := project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "lxd:ubuntu-20.04:suite/task")

	// If a suite excludes backends and a task excludes others, those backends do not appear in the job list
	createSuiteWithBackends(&project, []string{"-lxd"}, []string{"-qemu"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, ErrorMatches, `cannot find any tasks`)

	// if a suite adds a backend yet a task excludes it, it doesn't appear on the list
	createSuiteWithBackends(&project, []string{"+qe*"}, []string{"-qe*", "-openstack"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "lxd:ubuntu-20.04:suite/task")

	// If a suite allows all systems, but a task excludes some, those are not included in the job list
	createSuiteWithBackends(&project, []string{"*"}, []string{"-lxd"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 1)
	c.Assert(jobs[0].Name, Equals, "qemu:ubuntu-22.04:suite/task")

	// If a suite does not declare backends, then all task and backend-supported systems appear on the job list
	createSuiteWithBackends(&project, []string{}, []string{"lxd", "qemu"})
	jobs, err = project.Jobs(&spread.Options{})
	c.Assert(err, IsNil)
	c.Assert(len(jobs), Equals, 2)
}
