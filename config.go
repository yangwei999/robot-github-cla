package main

import (
	"errors"

	"github.com/huaweicloud/golangsdk"
	"github.com/opensourceways/community-robot-lib/config"
)

type configuration struct {
	ConfigItems []botConfig `json:"config_items,omitempty"`
}

func (c *configuration) configFor(org, repo string) *botConfig {
	if c == nil {
		return nil
	}

	items := c.ConfigItems
	v := make([]config.IRepoFilter, len(items))
	for i := range items {
		v[i] = &items[i]
	}

	if i := config.Find(org, repo, v); i >= 0 {
		return &items[i]
	}

	return nil
}

func (c *configuration) Validate() error {
	if c == nil {
		return nil
	}

	items := c.ConfigItems
	for i := range items {
		if err := items[i].validate(); err != nil {
			return err
		}
	}

	return nil
}

func (c *configuration) SetDefault() {
	if c == nil {
		return
	}

	Items := c.ConfigItems
	for i := range Items {
		Items[i].setDefault()
	}
}

type botConfig struct {
	config.RepoFilter

	// CLALabelYes is the cla label name for org/repos indicating
	// the cla has been signed
	CLALabelYes string `json:"cla_label_yes" required:"true"`

	// CLALabelNo is the cla label name for org/repos indicating
	// the cla has not been signed
	CLALabelNo string `json:"cla_label_no" required:"true"`

	// CheckURL is the url used to check whether the contributor has signed cla
	// The url has the format as https://**/{{org}}:{{repo}}?email={{email}}
	CheckURL string `json:"check_url" required:"true"`

	// SignURL is the url used to sign the cla
	SignURL string `json:"sign_url" required:"true"`

	// CheckByCommitter is one of ways to check CLA. There are two ways to check cla.
	// One is checking CLA by the email of committer, and Second is by the email of author.
	// Default is by email of author.
	CheckByCommitter bool `json:"check_by_committer"`

	// LitePRCommitter is the config for lite pr commiter.
	// It must be set when `check_by_committer` is true.
	LitePRCommitter litePRCommiter `json:"lite_pr_committer"`

	// FAQURL is the url of faq which is corresponding to the way of checking CLA
	FAQURL string `json:"faq_url" required:"true"`
}

func (c *botConfig) setDefault() {
}

func (c *botConfig) validate() error {
	if _, err := golangsdk.BuildRequestBody(c, ""); err != nil {
		return err
	}

	if c.CheckByCommitter {
		if err := c.LitePRCommitter.validate(); err != nil {
			return err
		}
	}

	return c.RepoFilter.Validate()
}

type litePRCommiter struct {
	// Email is the one of committer in a commit when a PR is lite
	Email string `json:"email" required:"true"`

	// Name is the one of committer in a commit when a PR is lite
	Name string `json:"name" required:"true"`
}

func (l litePRCommiter) validate() error {
	if l.Email == "" {
		return errors.New("missing email")
	}

	if l.Name == "" {
		return errors.New("missing name")
	}

	return nil
}

func (l litePRCommiter) isLitePR(email, name string) bool {
	return email == l.Email || name == l.Name
}
