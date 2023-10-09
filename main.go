package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
    "encoding/csv"
    "strconv"

	units "github.com/docker/go-units"

	"github.com/google/go-github/v50/github"
	"golang.org/x/oauth2"
)

type RepoSummary struct {
	Counts       map[string]int
	Jobs         int
	TotalTime    time.Duration
	LongestBuild time.Duration
	Name         string
}

type EventType string

const (
	JOB_STARTED  EventType = "started"
	JOB_FINISHED EventType = "finished"
)

type JobEvent struct {
	Ts github.Timestamp
	Tp EventType
}

func main() {
	var (
		orgName, userName, token, tokenFile, repoList, reposFile, apiUrl string
		days                                                             int
		punchCard, byRepo                                                bool
	)

	flag.StringVar(&orgName, "org", "", "Organization name")
	flag.StringVar(&orgName, "repo", "", "Repo name")
	flag.StringVar(&userName, "user", "", "User name")
	flag.StringVar(&apiUrl, "apiUrl", "", "Override Github API URL")
	flag.StringVar(&token, "token", "", "GitHub token")
	flag.StringVar(&tokenFile, "token-file", "", "Path to the file containing the GitHub token")
	flag.StringVar(&repoList, "include", "", "List of repos you want stats for eg. 'org/repo1,org/repo2'")
	flag.StringVar(&reposFile, "include-file", "", "Path to file containing the Github repos list or '-' for stdin")

	flag.BoolVar(&byRepo, "by-repo", false, "Show breakdown by repository")

	flag.BoolVar(&punchCard, "punch-card", false, "Show punch card with breakdown of builds per day")
	flag.IntVar(&days, "days", 30, "How many days of data to query from the GitHub API")

	flag.Parse()

	if len(tokenFile) > 0 {
		tokenBytes, err := os.ReadFile(tokenFile)
		if err != nil {
			log.Fatal(err)
		}
		token = strings.TrimSpace(string(tokenBytes))
	}

	auth := oauth2.NewClient(context.Background(), oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: token},
	))

	switch {
	case orgName == "" && userName == "":
		log.Fatal("Organization name or username is required")
	case orgName != "" && userName != "":
		log.Fatal("Only org or username must be specified at the same time")
	}

	client := github.NewClient(auth)
	if len(apiUrl) > 0 {
		var err error
		client, err = github.NewEnterpriseClient(apiUrl, "", auth)
		if err != nil {
			log.Fatal(err)
		}
	}
	created := time.Now().AddDate(0, 0, -days)
	format := "2006-01-02"
	createdQuery := ">=" + created.Format(format)

	var (
		totalRuns    int
		totalJobs    int
		totalPrivate int
		totalPublic  int
		longestBuild time.Duration
		actors       map[string]bool
		conclusion   map[string]int
		buildsPerDay map[string]int
		repoSummary  map[string]*RepoSummary
	)

	repoSummary = make(map[string]*RepoSummary)

	actors = make(map[string]bool)
	buildsPerDay = map[string]int{
		"Monday":    0,
		"Tuesday":   0,
		"Wednesday": 0,
		"Thursday":  0,
		"Friday":    0,
		"Saturday":  0,
		"Sunday":    0,
	}

	conclusion = map[string]int{
		"success":   0,
		"failure":   0,
		"cancelled": 0,
		"skipped":   0,
	}

	fmt.Printf("Fetching last %d days of data (created>=%s)\n", days, created.Format("2006-01-02"))

	var repos, allRepos []*github.Repository
	var res *github.Response
	var err error
	ctx := context.Background()
	page := 0
	for {
		log.Printf("Fetching repos %s page %d", orgName, page)
		if orgName != "" {
			opts := &github.RepositoryListByOrgOptions{ListOptions: github.ListOptions{Page: page, PerPage: 100}, Type: "all"}
			repos, res, err = client.Repositories.ListByOrg(ctx, orgName, opts)
		}

		if userName != "" {
			opts := &github.RepositoryListOptions{ListOptions: github.ListOptions{Page: page, PerPage: 100}, Type: "all"}
			repos, res, err = client.Repositories.List(ctx, userName, opts)
		}

		if err != nil {
			log.Fatal(err)
		}

		if res.Rate.Limit > 0 && res.Rate.Remaining == 0 {
			panic("Rate limit exceeded")
		}

		allRepos = append(allRepos, repos...)

		log.Printf("Status: %d Page %d, next page: %d", res.StatusCode, page, res.NextPage)

		if len(allRepos) == 0 {
			break
		}
		if res.NextPage == 0 {
			break
		}

		// break
		page = res.NextPage
	}

	allRepos, err = filterRepositories(allRepos, repoList, reposFile)
	if err != nil {
		log.Fatal(err)
	}

	allUsage := time.Second * 0

	for _, repo := range allRepos {
		log.Printf("Found: %s", repo.GetFullName())
		if repo.GetPrivate() {
			totalPrivate++
		} else {
			totalPublic++
		}

		workflowRuns := []*github.WorkflowRun{}
		page := 0
		for {

			opts := &github.ListWorkflowRunsOptions{Created: createdQuery, ListOptions: github.ListOptions{Page: page, PerPage: 100}}

			var runs *github.WorkflowRuns
			log.Printf("Listing workflow runs for: %s", repo.GetFullName())
			if orgName != "" {
				runs, res, err = client.Actions.ListRepositoryWorkflowRuns(ctx, orgName, repo.GetName(), opts)
			}
			if userName != "" {
				realOwner := userName
				// if user is a member of repository
				if userName != *repo.Owner.Login {
					realOwner = *repo.Owner.Login
				}
				runs, res, err = client.Actions.ListRepositoryWorkflowRuns(ctx, realOwner, repo.GetName(), opts)
			}

			if _, ok := repoSummary[repo.GetFullName()]; !ok {
				repoSummary[repo.GetFullName()] = &RepoSummary{
					Counts:    make(map[string]int),
					TotalTime: time.Second * 0,
					Name:      repo.GetFullName(),
				}
			}

			if err != nil {
				log.Fatal(err)
			}

			workflowRuns = append(workflowRuns, runs.WorkflowRuns...)

			if len(workflowRuns) == 0 {
				break
			}

			if res.NextPage == 0 {
				break
			}

			page = res.NextPage
		}

		totalRuns += len(workflowRuns)

		var owner string
		if orgName != "" {
			owner = orgName
		}
		if userName != "" {
			owner = userName
		}
		log.Printf("Found %d workflow runs for %s/%s", len(workflowRuns), owner, repo.GetName())

		var job_events []JobEvent

		for _, run := range workflowRuns {
			log.Printf("Fetching jobs for: run ID: %d, startedAt: %s, conclusion: %s", run.GetID(), run.GetRunStartedAt().Format("2006-01-02 15:04:05"), run.GetConclusion())
			workflowJobs := []*github.WorkflowJob{}

			if a := run.GetActor(); a != nil {
				actors[a.GetLogin()] = true
			}
			page := 0
			for {
				log.Printf("Fetching jobs for: %d, page %d", run.GetID(), page)
				jobs, res, err := client.Actions.ListWorkflowJobs(ctx,
					owner,
					repo.GetName(),
					run.GetID(),
					&github.ListWorkflowJobsOptions{Filter: "all", ListOptions: github.ListOptions{Page: page, PerPage: 100}})
				if err != nil {
					log.Fatal(err)
				}

				summary := repoSummary[owner+"/"+repo.GetName()]

				for _, job := range jobs.Jobs {
					if !job.GetCompletedAt().IsZero() {
                        if job.GetConclusion() != "skipped" {
                            job_events = append(job_events, JobEvent{Ts: job.GetStartedAt(), Tp: JOB_STARTED})
                            job_events = append(job_events, JobEvent{Ts: job.GetCompletedAt(), Tp: JOB_FINISHED})
                        }
						dur := job.GetCompletedAt().Time.Sub(job.GetStartedAt().Time)
						if dur > longestBuild {
							longestBuild = dur
						}
						if dur > summary.LongestBuild {
							summary.LongestBuild = dur
						}

						summary.TotalTime += dur
					}

					if _, ok := conclusion[job.GetConclusion()]; !ok {
						conclusion[job.GetConclusion()] = 0
					}

					conclusion[job.GetConclusion()]++

					summary.Counts[job.GetConclusion()]++
					summary.Jobs++
				}

				workflowJobs = append(workflowJobs, jobs.Jobs...)

				if len(jobs.Jobs) == 0 {
					break
				}

				if res.NextPage == 0 {
					break
				}
				page = res.NextPage
			}

			totalJobs += len(workflowJobs)
			log.Printf("%d jobs for workflow run: %d", len(workflowJobs), run.GetID())
			for _, job := range workflowJobs {
				if !job.GetCompletedAt().IsZero() {
					dur := job.GetCompletedAt().Time.Sub(job.GetStartedAt().Time)
					allUsage += dur
					log.Printf("Job: %d [%s - %s] (%s): %s",
						job.GetID(), job.GetStartedAt().Format("2006-01-02 15:04:05"),
						job.GetCompletedAt().Format("2006-01-02 15:04:05"),
						humanDuration(dur), job.GetConclusion())

					dayOfWeek := job.GetStartedAt().Time.Weekday().String()

					buildsPerDay[dayOfWeek]++
				}
			}
		}

		sort.Slice(job_events, func(i, j int) bool { return job_events[i].Ts.Time.Before(job_events[j].Ts.Time) })
		var live_jobs int = 0
        fname := fmt.Sprintf("concurrent_jobs_%s.csv", repo.GetName())
        csv_file, _ := os.Create(fname)
        w := csv.NewWriter(csv_file)
        w.Write([]string {"Unix time", "Concurrent job count"})
		for _, event := range job_events {
            // print once before and once after incrementing to make the result better suited for plotting a step-chart in excel
            w.Write([]string {strconv.FormatInt(event.Ts.Time.Unix(), 10), strconv.Itoa(live_jobs)})
			if event.Tp == JOB_STARTED {
				live_jobs++
			} else if event.Tp == JOB_FINISHED {
				live_jobs--
			}
            w.Write([]string {strconv.FormatInt(event.Ts.Time.Unix(), 10), strconv.Itoa(live_jobs)})
		}
        w.Flush()
        fmt.Printf("Concurrent jobs data written to %s\n", fname)
	}

	entity := orgName
	if len(orgName) == 0 {
		entity = userName
	}

	daysOfWEek := []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}
	fmt.Printf("\nGenerated by: https://github.com/self-actuated/actions-usage\nReport for %s - last: %d days.\n\n", entity, days)
	fmt.Printf("Total repos: %d\n", len(allRepos))
	fmt.Printf("Total private repos: %d\n", totalPrivate)
	fmt.Printf("Total public repos: %d\n", totalPublic)
	fmt.Println()
	fmt.Printf("Total workflow runs: %d\n", totalRuns)
	fmt.Printf("Total workflow jobs: %d\n", totalJobs)
	fmt.Println()
	fmt.Printf("Total users: %d\n", len(actors))

	if totalJobs > 0 {
		fmt.Println()
		fmt.Printf("Success: %d/%d\n", conclusion["success"], totalJobs)
		fmt.Printf("Failure: %d/%d\n", conclusion["failure"], totalJobs)
		fmt.Printf("Cancelled: %d/%d\n", conclusion["cancelled"], totalJobs)
		if conclusion["skipped"] > 0 {
			fmt.Printf("Skipped: %d/%d\n", conclusion["skipped"], totalJobs)
		}
		fmt.Println()
		fmt.Printf("Longest build: %s\n", longestBuild.Round(time.Second))
		fmt.Printf("Average build time: %s\n", (allUsage / time.Duration(totalJobs)).Round(time.Second))

		fmt.Println()

		if punchCard {
			w := tabwriter.NewWriter(os.Stdout, 15, 4, 1, ' ', tabwriter.TabIndent)
			fmt.Fprintln(w, "Day\tBuilds")
			for _, day := range daysOfWEek {
				fmt.Fprintf(w, "%s\t%d\n", day, buildsPerDay[day])
			}
			fmt.Fprintf(w, "%s\t%d\n", "Total", totalJobs)

			w.Flush()
		}
	}

	if byRepo {
		fmt.Println()
		w := tabwriter.NewWriter(os.Stdout, 15, 4, 1, ' ', tabwriter.TabIndent)
		fmt.Fprintln(w, "Repo\tBuilds\tSuccess\tFailure\tCancelled\tSkipped\tTotal\tAverage\tLongest")

		summaries := []*RepoSummary{}
		for _, summary := range repoSummary {
			summaries = append(summaries, summary)
		}

		sort.Slice(summaries, func(i, j int) bool {
			if summaries[i].Jobs == summaries[j].Jobs {
				return summaries[i].TotalTime > summaries[j].TotalTime
			}

			return summaries[i].Jobs > summaries[j].Jobs
		})

		for _, summary := range summaries {
			repoName := summary.Name

			avg := time.Duration(0)
			if summary.Jobs > 0 {
				avg = summary.TotalTime / time.Duration(summary.Jobs)

				fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\n",
					repoName,
					summary.Jobs,
					summary.Counts["success"],
					summary.Counts["failure"],
					summary.Counts["cancelled"],
					summary.Counts["skipped"],
					summary.TotalTime.Round(time.Second),
					avg.Round(time.Second),
					summary.LongestBuild.Round(time.Second))
			}
		}

		w.Flush()
	}

	fmt.Println()

	mins := fmt.Sprintf("%.0f mins", allUsage.Minutes())
	fmt.Printf("Total usage: %s (%s)\n", allUsage.String(), mins)
	fmt.Println()
}

// types.HumanDuration fixes a long string for a value < 1s
func humanDuration(duration time.Duration) string {
	v := strings.ToLower(units.HumanDuration(duration))

	if v == "less than a second" {
		return fmt.Sprintf("%d ms", duration.Milliseconds())
	} else if v == "about a minute" {
		return fmt.Sprintf("%d seconds", int(duration.Seconds()))
	}

	return v
}

func parseInclude(include string) []string {
	repos := strings.Split(include, ",")
	for i, repo := range repos {
		repos[i] = strings.TrimSpace(repo)
	}
	return repos
}

func parseIncludeFromReader(input io.Reader) ([]string, error) {
	reposBytes, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(reposBytes), "\n")
	repos := make([]string, 0, len(lines))
	for _, line := range lines {
		repo := strings.TrimSpace(line)

		if repo != "" {
			repos = append(repos, repo)
		}
	}

	return repos, nil
}

// getFilter creates a filter map from a comma-separated list of repository names in the
// format 'user/repo'
func getFilterMap(repos []string) (map[string]bool, error) {
	filter := make(map[string]bool)

	for _, repo := range repos {
		if !strings.Contains(repo, "/") {
			return nil, fmt.Errorf("invalid repository name format: %q, must be in the format 'user/repo'", repo)
		}
		filter[repo] = true
	}

	return filter, nil
}

// reduceRepositories takes a list of repositories and a filter map, and returns a filtered list
// based on the repository names.
func reduceRepositories(allRepos []*github.Repository, filter map[string]bool) ([]*github.Repository, error) {
	var filteredRepos []*github.Repository

	for _, repo := range allRepos {
		name := repo.GetFullName()
		if filter[name] {
			filteredRepos = append(filteredRepos, repo)
		}
	}

	if len(filteredRepos) == 0 {
		return nil, fmt.Errorf("no matching repositories found based on the provided filter")
	}

	return filteredRepos, nil
}

func filterRepositories(repos []*github.Repository, repoList, reposFile string) ([]*github.Repository, error) {
	if len(reposFile) > 0 || len(repoList) > 0 {
		var selectedRepos []string

		if len(repoList) > 0 {
			reposFromList := parseInclude(repoList)
			selectedRepos = reposFromList
		}

		if len(reposFile) > 0 {
			var input io.Reader

			if reposFile == "-" {
				stat, _ := os.Stdin.Stat()
				if (stat.Mode() & os.ModeCharDevice) != 0 {
					fmt.Fprintf(os.Stderr, "Reading from STDIN - hit (Control + D) to stop.\n")
				}

				input = os.Stdin
			} else {
				file, err := os.Open(reposFile)
				if err != nil {
					return nil, err
				}
				defer file.Close()

				input = file
			}

			reposFromFile, err := parseIncludeFromReader(input)
			if err != nil {
				return nil, err
			}

			selectedRepos = append(selectedRepos, reposFromFile...)
		}

		filter, err := getFilterMap(selectedRepos)
		if err != nil {
			return nil, err
		}

		repos, err = reduceRepositories(repos, filter)
		if err != nil {
			return nil, err
		}
	}

	return repos, nil
}
