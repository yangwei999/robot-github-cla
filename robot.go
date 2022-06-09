package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"regexp"
	"strings"

	sdk "github.com/google/go-github/v36/github"
	"github.com/opensourceways/community-robot-lib/config"
	gc "github.com/opensourceways/community-robot-lib/githubclient"
	framework "github.com/opensourceways/community-robot-lib/robot-github-framework"
	"github.com/opensourceways/community-robot-lib/utils"
	"github.com/sirupsen/logrus"
)

const (
	botName        = "cla"
	maxLengthOfSHA = 8
)

var checkCLARe = regexp.MustCompile(`(?mi)^/check-cla\s*$`)

type iClient interface {
	AddPRLabel(pr gc.PRInfo, label string) error
	RemovePRLabel(pr gc.PRInfo, label string) error
	CreatePRComment(pr gc.PRInfo, comment string) error
	DeletePRComment(org, repo string, ID int64) error
	GetPRCommits(pr gc.PRInfo) ([]*sdk.RepositoryCommit, error)
	GetPRComments(pr gc.PRInfo) ([]*sdk.IssueComment, error)
}

func newRobot(cli iClient) *robot {
	return &robot{cli: cli}
}

type robot struct {
	cli iClient
}

func (bot *robot) NewConfig() config.Config {
	return &configuration{}
}

func (bot *robot) getConfig(cfg config.Config, org, repo string) (*botConfig, error) {
	c, ok := cfg.(*configuration)
	if !ok {
		return nil, fmt.Errorf("can't convert to configuration")
	}

	if bc := c.configFor(org, repo); bc != nil {
		return bc, nil
	}

	return nil, fmt.Errorf("no config for this repo:%s/%s", org, repo)
}

func (bot *robot) RegisterEventHandler(f framework.HandlerRegister) {
	f.RegisterPullRequestHandler(bot.handlePREvent)
	f.RegisterIssueCommentHandler(bot.handleNoteEvent)
}

func (bot *robot) handlePREvent(e *sdk.PullRequestEvent, c config.Config, log *logrus.Entry) error {
	if e.GetPullRequest().GetState() != "open" {
		return nil
	}

	v := e.GetAction()
	if !gc.IsPROpened(v) && !gc.IsPRSourceBranchChanged(v) {
		return nil
	}

	info := gc.GenIssuePRInfo(e)

	org, repo := info.GetOrgRepo()
	cfg, err := bot.getConfig(c, org, repo)
	if err != nil {
		return err
	}

	_, err = bot.handle(info, cfg, log)

	return err
}

func (bot *robot) handleNoteEvent(e *sdk.IssueCommentEvent, c config.Config, log *logrus.Entry) error {
	if !gc.IsCommentCreated(e) || !gc.IsCommentOnPullRequest(e) {
		return nil
	}

	// Only consider "/check-cla" comments.
	if !checkCLARe.MatchString(e.GetComment().GetBody()) {
		return nil
	}

	info := gc.GenIssuePRInfo(e)

	org, repo := info.GetOrgRepo()
	cfg, err := bot.getConfig(c, org, repo)
	if err != nil {
		return err
	}

	if b, err := bot.handle(info, cfg, log); err != nil || !b {
		return err
	}

	return bot.cli.CreatePRComment(
		gc.PRInfo{
			Org:    org,
			Repo:   repo,
			Number: info.GetNumber(),
		},
		alreadySigned(info.GetAuthor()),
	)
}

func (bot *robot) handle(info gc.IssuePRInfo, cfg *botConfig, log *logrus.Entry) (yes bool, err error) {
	org, repo := info.GetOrgRepo()
	pr := gc.PRInfo{
		Org:    org,
		Repo:   repo,
		Number: info.GetNumber(),
	}

	unsigned, err := bot.getUnsignedCommits(pr, cfg)
	if err != nil {
		return
	}

	labels := info.GetLabels()
	hasCLAYes := labels.Has(cfg.CLALabelYes)
	hasCLANo := labels.Has(cfg.CLALabelNo)

	deleteSignGuide(pr, bot.cli)

	if len(unsigned) == 0 {
		if hasCLANo {
			if err = bot.cli.RemovePRLabel(pr, cfg.CLALabelNo); err != nil {
				err = fmt.Errorf(
					"Could not remove %s label, err: %s",
					cfg.CLALabelNo, err.Error(),
				)

				return
			}
		}

		if !hasCLAYes {
			yes = true

			if err = bot.cli.AddPRLabel(pr, cfg.CLALabelYes); err != nil {
				err = fmt.Errorf(
					"Could not add %s label, err: %s",
					cfg.CLALabelYes, err.Error(),
				)
			}
		}

		return
	}

	if hasCLAYes {
		if err = bot.cli.RemovePRLabel(pr, cfg.CLALabelYes); err != nil {
			err = fmt.Errorf(
				"Could not remove %s label, err: %s",
				cfg.CLALabelYes, err.Error(),
			)

			return
		}
	}

	if !hasCLANo {
		if err := bot.cli.AddPRLabel(pr, cfg.CLALabelNo); err != nil {
			log.WithError(err).Warningf("Could not add %s label.", cfg.CLALabelNo)
		}
	}

	err = bot.cli.CreatePRComment(
		pr, signGuide(cfg.SignURL, generateUnSignComment(unsigned), cfg.FAQURL),
	)

	return
}

func (bot *robot) getUnsignedCommits(pr gc.PRInfo, cfg *botConfig) (map[string]string, error) {
	commits, err := bot.cli.GetPRCommits(pr)
	if err != nil {
		return nil, err
	}

	if len(commits) == 0 {
		return nil, fmt.Errorf("commits is empty, cla cannot be checked")
	}

	unsigned := make(map[string]string)
	update := func(c *sdk.RepositoryCommit) {
		unsigned[c.GetSHA()] = c.GetCommit().GetMessage()
	}

	result := map[string]bool{}

	for i := range commits {
		c := commits[i]
		email := strings.Trim(getAuthorOfCommit(c, cfg), " ")

		if !utils.IsValidEmail(email) {
			update(c)

			continue
		}

		if v, ok := result[email]; ok {
			if !v {
				update(c)
			}

			continue
		}

		b, err := isSigned(email, cfg.CheckURL)
		if err != nil {
			return nil, err
		}

		result[email] = b
		if !b {
			update(c)
		}
	}

	if _, ok := unsigned[""]; ok {
		return nil, fmt.Errorf("invalid commit exists")
	}

	return unsigned, nil
}

func getAuthorOfCommit(c *sdk.RepositoryCommit, cfg *botConfig) string {
	if c == nil {
		return ""
	}

	if cfg.CheckByCommitter {
		v := c.GetCommit().GetCommitter()

		if !cfg.LitePRCommitter.isLitePR(v.GetEmail(), v.GetName()) {
			return v.GetEmail()
		}
	}

	return c.GetCommit().GetAuthor().GetEmail()
}

func isSigned(email, url string) (bool, error) {
	endpoint := fmt.Sprintf("%s?email=%s", url, email)

	resp, err := http.Get(endpoint)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	rb, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, fmt.Errorf("response has status %q and body %q", resp.Status, string(rb))
	}

	type signingInfo struct {
		Signed bool `json:"signed"`
	}
	var v struct {
		Data signingInfo `json:"data"`
	}

	if err := json.Unmarshal(rb, &v); err != nil {
		return false, fmt.Errorf("unmarshal failed: %s", err.Error())
	}

	return v.Data.Signed, nil
}

func deleteSignGuide(pr gc.PRInfo, c iClient) {
	v, err := c.GetPRComments(pr)
	if err != nil {
		return
	}

	prefix := signGuideTitle()
	prefixOld := "Thanks for your pull request. Before we can look at your pull request, you'll need to sign a Contributor License Agreement (CLA)."
	f := func(s string) bool {
		return strings.HasPrefix(s, prefix) || strings.HasPrefix(s, prefixOld)
	}

	for i := range v {
		if item := v[i]; f(item.GetBody()) {
			_ = c.DeletePRComment(pr.Org, pr.Repo, item.GetID())
		}
	}
}

func signGuideTitle() string {
	return "Thanks for your pull request.\n\nThe authors of the following commits have not signed the Contributor License Agreement (CLA):"
}

func signGuide(signURL, cInfo, faq string) string {
	s := `%s

%s

Please check the [**FAQs**](%s) first.
You can click [**here**](%s) to sign the CLA. After signing the CLA, you must comment "/check-cla" to check the CLA status again.`

	return fmt.Sprintf(s, signGuideTitle(), cInfo, faq, signURL)
}

func alreadySigned(user string) string {
	s := `***@%s***, thanks for your pull request. All authors of the commits have signed the CLA. :wave: `
	return fmt.Sprintf(s, user)
}

func generateUnSignComment(commits map[string]string) string {
	if len(commits) == 0 {
		return ""
	}

	cs := make([]string, 0, len(commits))
	for sha, msg := range commits {
		if len(sha) > maxLengthOfSHA {
			sha = sha[:maxLengthOfSHA]
		}

		cs = append(cs, fmt.Sprintf("**%s** | %s", sha, msg))
	}

	return strings.Join(cs, "\n")
}
