package central

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"time"

	"github.com/k1LoW/octocov/config"
	"github.com/k1LoW/octocov/gh"
	"github.com/k1LoW/octocov/pkg/badge"
	"github.com/k1LoW/octocov/report"
)

//go:embed index.md.tmpl
var indexTmpl []byte

type Central struct {
	config         *config.Config
	reports        []*report.Report
	generatedPaths []string
}

func New(c *config.Config) *Central {
	return &Central{
		config:  c,
		reports: []*report.Report{},
	}
}

func (c *Central) Generate(ctx context.Context) error {
	// collect reports
	if err := c.collectReports(); err != nil {
		return err
	}

	// generate badges
	if err := c.generateBadges(); err != nil {
		return err
	}

	// render index
	p := c.config.Central.Root
	fi, err := os.Stat(c.config.Central.Root)
	if err == nil && fi.IsDir() {
		p = filepath.Join(c.config.Central.Root, "README.md")
	}
	i, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644) // #nosec
	if err != nil {
		return err
	}
	if err := c.renderIndex(i); err != nil {
		return err
	}
	c.generatedPaths = append(c.generatedPaths, p)

	// git push
	if c.config.CentralPushConfigReady() {
		_, _ = fmt.Fprintln(os.Stderr, "Commit and push central report")
		if err := gh.PushUsingLocalGit(ctx, c.config.GitRoot, c.generatedPaths, "Update by octocov"); err != nil {
			return err
		}
	}

	return nil
}

func (c *Central) collectReports() error {
	rsMap := map[string]*report.Report{}

	// collect reports
	if err := filepath.Walk(c.config.Central.Reports, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() || !strings.HasSuffix(fi.Name(), ".json") {
			return nil
		}
		r := report.New()
		b, err := ioutil.ReadFile(filepath.Clean(path))
		if err != nil {
			return nil
		}
		if err := json.Unmarshal(b, r); err != nil {
			return nil
		}
		current, ok := rsMap[r.Repository]
		if !ok {
			_, _ = fmt.Fprintf(os.Stderr, "Collect report of %s\n", r.Repository)
			rsMap[r.Repository] = r
			return nil
		}
		if current.Timestamp.UnixNano() < r.Timestamp.UnixNano() {
			rsMap[r.Repository] = r
		}
		return nil
	}); err != nil {
		return err
	}

	for _, r := range rsMap {
		c.reports = append(c.reports, r)
	}
	sort.Slice(c.reports, func(i, j int) bool { return c.reports[i].Repository < c.reports[j].Repository })
	return nil
}

func (c *Central) generateBadges() error {
	for _, r := range c.reports {
		cp := r.CoveragePercent()
		err := os.MkdirAll(filepath.Join(c.config.Central.Badges, r.Repository), 0755) // #nosec
		if err != nil {
			return err
		}
		bp := filepath.Join(c.config.Central.Badges, r.Repository, "coverage.svg")
		out, err := os.OpenFile(bp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644) // #nosec
		if err != nil {
			return err
		}
		b := badge.New("coverage", fmt.Sprintf("%.1f%%", cp))
		b.MessageColor = c.config.CoverageColor(cp)
		if err := b.Render(out); err != nil {
			return err
		}
		c.generatedPaths = append(c.generatedPaths, bp)

		// Code to Test Ratio
		if r.CodeToTestRatio != nil {
			tr := r.CodeToTestRatioRatio()
			err := os.MkdirAll(filepath.Join(c.config.Central.Badges, r.Repository), 0755) // #nosec
			if err != nil {
				return err
			}
			bp := filepath.Join(c.config.Central.Badges, r.Repository, "ratio.svg")
			out, err = os.OpenFile(bp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644) // #nosec
			if err != nil {
				return err
			}
			b := badge.New("code to test ratio", fmt.Sprintf("1:%.1f", tr))
			b.MessageColor = c.config.CodeToTestRatioColor(tr)
			if err := b.Render(out); err != nil {
				return err
			}
			c.generatedPaths = append(c.generatedPaths, bp)
		}

		// Test Execution Time
		if r.TestExecutionTime != nil {
			d := time.Duration(*r.TestExecutionTime)
			err := os.MkdirAll(filepath.Join(c.config.Central.Badges, r.Repository), 0755) // #nosec
			if err != nil {
				return err
			}
			bp := filepath.Join(c.config.Central.Badges, r.Repository, "time.svg")
			out, err = os.OpenFile(bp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644) // #nosec
			if err != nil {
				return err
			}
			b := badge.New("test execution time", d.String())
			b.MessageColor = c.config.TestExecutionTimeColor(d)
			if err := b.Render(out); err != nil {
				return err
			}
			c.generatedPaths = append(c.generatedPaths, bp)
		}
	}
	return nil
}

func (c *Central) renderIndex(wr io.Writer) error {
	tmpl := template.Must(template.New("index").Funcs(funcs()).Parse(string(indexTmpl)))
	host := os.Getenv("GITHUB_SERVER_URL")
	if host == "" {
		host = gh.DefaultGithubServerURL
	}

	ctx := context.Background()
	gh, err := gh.New()
	if err != nil {
		return err
	}
	owner, repo, err := c.config.OwnerRepo()
	if err != nil {
		return err
	}
	rawRootURL, err := gh.GetRawRootURL(ctx, owner, repo)
	if err != nil {
		return err
	}

	// Get project root dir
	proot := c.config.Getwd()

	croot := c.config.Central.Root
	if strings.HasSuffix(croot, ".md") {
		croot = filepath.Dir(c.config.Central.Root)
	}

	badgesLinkRel, err := filepath.Rel(croot, c.config.Central.Badges)
	if err != nil {
		return err
	}

	badgesURLRel, err := filepath.Rel(proot, c.config.Central.Badges)
	if err != nil {
		return err
	}

	d := map[string]interface{}{
		"Host":          host,
		"Reports":       c.reports,
		"BadgesLinkRel": badgesLinkRel,
		"BadgesURLRel":  badgesURLRel,
		"RawRootURL":    rawRootURL,
	}
	if err := tmpl.Execute(wr, d); err != nil {
		return err
	}

	return nil
}

func funcs() map[string]interface{} {
	return template.FuncMap{
		"coverage": func(r *report.Report) string {
			return fmt.Sprintf("%.1f%%", r.CoveragePercent())
		},
		"ratio": func(r *report.Report) string {
			if r.CodeToTestRatio == nil {
				return "-"
			}
			return fmt.Sprintf("1:%.1f", r.CodeToTestRatioRatio())
		},
		"time": func(r *report.Report) string {
			if r.TestExecutionTime == nil {
				return "-"
			}
			return time.Duration(*r.TestExecutionTime).String()
		},
	}
}
