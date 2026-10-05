package gitlab

// The structs below carry only the fields the tools render. This keeps payloads
// small and — importantly — means a secret-bearing field such as `variables` has
// nowhere to land even if a response contains one.

// Version is GET /version.
type Version struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
}

// TokenInfo is GET /personal_access_tokens/self.
type TokenInfo struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresAt string   `json:"expires_at"`
	Active    bool     `json:"active"`
	Revoked   bool     `json:"revoked"`
	UserID    int      `json:"user_id"`
}

// Namespace is the group or user a project lives in.
type Namespace struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	FullPath string `json:"full_path"`
	Kind     string `json:"kind"`
}

// Group is GET /groups[/:id].
type Group struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	FullPath    string `json:"full_path"`
	Description string `json:"description"`
	Visibility  string `json:"visibility"`
	WebURL      string `json:"web_url"`
	ParentID    int    `json:"parent_id"`
}

// Project is GET /projects[/:id].
type Project struct {
	ID                int       `json:"id"`
	Name              string    `json:"name"`
	Path              string    `json:"path"`
	PathWithNamespace string    `json:"path_with_namespace"`
	Description       string    `json:"description"`
	DefaultBranch     string    `json:"default_branch"`
	Visibility        string    `json:"visibility"`
	Archived          bool      `json:"archived"`
	WebURL            string    `json:"web_url"`
	LastActivityAt    string    `json:"last_activity_at"`
	Topics            []string  `json:"topics"`
	StarCount         int       `json:"star_count"`
	OpenIssuesCount   int       `json:"open_issues_count"`
	Namespace         Namespace `json:"namespace"`
}

// TreeEntry is GET /projects/:id/repository/tree.
type TreeEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // tree|blob
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// Branch is GET /projects/:id/repository/branches.
type Branch struct {
	Name               string `json:"name"`
	Merged             bool   `json:"merged"`
	Protected          bool   `json:"protected"`
	Default            bool   `json:"default"`
	CanPush            bool   `json:"can_push"`
	Commit             Commit `json:"commit"`
	DevelopersCanPush  bool   `json:"developers_can_push"`
	DevelopersCanMerge bool   `json:"developers_can_merge"`
}

// Tag is GET /projects/:id/repository/tags.
type Tag struct {
	Name    string `json:"name"`
	Message string `json:"message"`
	Target  string `json:"target"`
	Commit  Commit `json:"commit"`
}

// Commit is a repository commit.
type Commit struct {
	ID            string   `json:"id"`
	ShortID       string   `json:"short_id"`
	Title         string   `json:"title"`
	Message       string   `json:"message"`
	AuthorName    string   `json:"author_name"`
	AuthorEmail   string   `json:"author_email"`
	AuthoredDate  string   `json:"authored_date"`
	CommittedDate string   `json:"committed_date"`
	WebURL        string   `json:"web_url"`
	ParentIDs     []string `json:"parent_ids"`
	LastPipeline  *struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
		Ref    string `json:"ref"`
	} `json:"last_pipeline"`
}

// Diff is one file's diff within a commit, compare or merge request.
type Diff struct {
	OldPath     string `json:"old_path"`
	NewPath     string `json:"new_path"`
	NewFile     bool   `json:"new_file"`
	RenamedFile bool   `json:"renamed_file"`
	DeletedFile bool   `json:"deleted_file"`
	Diff        string `json:"diff"`
}

// Compare is GET /projects/:id/repository/compare.
type Compare struct {
	Commit         Commit   `json:"commit"`
	Commits        []Commit `json:"commits"`
	Diffs          []Diff   `json:"diffs"`
	CompareTimeout bool     `json:"compare_timeout"`
	CompareSameRef bool     `json:"compare_same_ref"`
}

// User is an abbreviated user reference.
type User struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	State    string `json:"state"`
}

// Member is GET /projects/:id/members/all or /groups/:id/members.
type Member struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	Name        string `json:"name"`
	State       string `json:"state"`
	AccessLevel int    `json:"access_level"`
	ExpiresAt   string `json:"expires_at"`
}

// Issue is GET /projects/:id/issues.
type Issue struct {
	IID         int      `json:"iid"`
	ProjectID   int      `json:"project_id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	State       string   `json:"state"`
	Labels      []string `json:"labels"`
	Author      User     `json:"author"`
	Assignees   []User   `json:"assignees"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	ClosedAt    string   `json:"closed_at"`
	DueDate     string   `json:"due_date"`
	WebURL      string   `json:"web_url"`
	References  struct {
		Full string `json:"full"`
	} `json:"references"`
}

// MergeRequest is GET /projects/:id/merge_requests.
type MergeRequest struct {
	IID          int      `json:"iid"`
	ProjectID    int      `json:"project_id"`
	Title        string   `json:"title"`
	Description  string   `json:"description"`
	State        string   `json:"state"`
	Draft        bool     `json:"draft"`
	SourceBranch string   `json:"source_branch"`
	TargetBranch string   `json:"target_branch"`
	Labels       []string `json:"labels"`
	Author       User     `json:"author"`
	Reviewers    []User   `json:"reviewers"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
	MergedAt     string   `json:"merged_at"`
	WebURL       string   `json:"web_url"`
	SHA          string   `json:"sha"`
	MergeStatus  string   `json:"merge_status"`
	HasConflicts bool     `json:"has_conflicts"`
	Pipeline     *struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
	} `json:"head_pipeline"`
}

// MergeRequestChanges is GET /projects/:id/merge_requests/:iid/diffs.
type MergeRequestChanges struct {
	Changes []Diff `json:"changes"`
}

// Note is a comment on an issue or merge request.
type Note struct {
	ID        int    `json:"id"`
	Body      string `json:"body"`
	Author    User   `json:"author"`
	CreatedAt string `json:"created_at"`
	System    bool   `json:"system"`
}

// SearchBlob is a `blobs` or `wiki_blobs` search hit.
type SearchBlob struct {
	Basename  string `json:"basename"`
	Data      string `json:"data"`
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	ProjectID int    `json:"project_id"`
	Ref       string `json:"ref"`
	Startline int    `json:"startline"`
}

// Pipeline is GET /projects/:id/pipelines[/:id].
//
// Note the deliberate absence of a `variables` field: pipeline variables are on
// the deny list, and giving the struct nowhere to put them is a second barrier.
type Pipeline struct {
	ID         int    `json:"id"`
	IID        int    `json:"iid"`
	ProjectID  int    `json:"project_id"`
	Status     string `json:"status"`
	Source     string `json:"source"`
	Ref        string `json:"ref"`
	SHA        string `json:"sha"`
	Name       string `json:"name"`
	WebURL     string `json:"web_url"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	Duration   int    `json:"duration"`
	User       User   `json:"user"`
}

// Job is GET /projects/:id/jobs/:id.
type Job struct {
	ID             int     `json:"id"`
	Name           string  `json:"name"`
	Stage          string  `json:"stage"`
	Status         string  `json:"status"`
	AllowFailure   bool    `json:"allow_failure"`
	CreatedAt      string  `json:"created_at"`
	StartedAt      string  `json:"started_at"`
	FinishedAt     string  `json:"finished_at"`
	Duration       float64 `json:"duration"`
	QueuedDuration float64 `json:"queued_duration"`
	WebURL         string  `json:"web_url"`
	FailureReason  string  `json:"failure_reason"`
	Ref            string  `json:"ref"`
	Tag            bool    `json:"tag"`
	Runner         *struct {
		ID          int    `json:"id"`
		Description string `json:"description"`
	} `json:"runner"`
	Pipeline struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
		Ref    string `json:"ref"`
	} `json:"pipeline"`
	Artifacts []struct {
		FileType string `json:"file_type"`
		Filename string `json:"filename"`
		Size     int64  `json:"size"`
	} `json:"artifacts"`
}

// TestReportSummary is GET /projects/:id/pipelines/:id/test_report_summary.
type TestReportSummary struct {
	Total struct {
		Time       float64 `json:"time"`
		Count      int     `json:"count"`
		Success    int     `json:"success"`
		Failed     int     `json:"failed"`
		Skipped    int     `json:"skipped"`
		Error      int     `json:"error"`
		SuiteError string  `json:"suite_error"`
	} `json:"total"`
	TestSuites []struct {
		Name         string  `json:"name"`
		TotalTime    float64 `json:"total_time"`
		TotalCount   int     `json:"total_count"`
		SuccessCount int     `json:"success_count"`
		FailedCount  int     `json:"failed_count"`
		SkippedCount int     `json:"skipped_count"`
		ErrorCount   int     `json:"error_count"`
	} `json:"test_suites"`
}

// PipelineSchedule is GET /projects/:id/pipeline_schedules.
//
// The single-schedule endpoint embeds a `variables` array; the redactor replaces
// it and this struct has no field for it.
type PipelineSchedule struct {
	ID           int    `json:"id"`
	Description  string `json:"description"`
	Ref          string `json:"ref"`
	Cron         string `json:"cron"`
	CronTimezone string `json:"cron_timezone"`
	NextRunAt    string `json:"next_run_at"`
	Active       bool   `json:"active"`
	Owner        User   `json:"owner"`
}

// LintRequest is the body of POST /projects/:id/ci/lint.
type LintRequest struct {
	Content     string `json:"content"`
	DryRun      bool   `json:"dry_run,omitempty"`
	IncludeJobs bool   `json:"include_jobs,omitempty"`
	Ref         string `json:"ref,omitempty"`
}

// LintResult is the response of the CI lint endpoints.
type LintResult struct {
	Valid      bool     `json:"valid"`
	Errors     []string `json:"errors"`
	Warnings   []string `json:"warnings"`
	MergedYAML string   `json:"merged_yaml"`
	Includes   []struct {
		Type           string `json:"type"`
		Location       string `json:"location"`
		Blob           string `json:"blob"`
		Raw            string `json:"raw"`
		Extra          any    `json:"extra"`
		ContextProject string `json:"context_project"`
		ContextSHA     string `json:"context_sha"`
	} `json:"includes"`
	Jobs []struct {
		Name         string   `json:"name"`
		Stage        string   `json:"stage"`
		BeforeScript []string `json:"before_script"`
		Script       []string `json:"script"`
		AfterScript  []string `json:"after_script"`
		TagList      []string `json:"tag_list"`
		Only         any      `json:"only"`
		Except       any      `json:"except"`
		When         string   `json:"when"`
		AllowFailure bool     `json:"allow_failure"`
	} `json:"jobs"`
}

// CreatePipelineRequest is the body of POST /projects/:id/pipeline.
type CreatePipelineRequest struct {
	Ref       string             `json:"ref"`
	Variables []PipelineVariable `json:"variables,omitempty"`
}

// PipelineVariable is a variable supplied when starting a pipeline or playing a
// job. It is only ever sent, never read back — the read path is deny-listed.
type PipelineVariable struct {
	Key          string `json:"key"`
	Value        string `json:"value"`
	VariableType string `json:"variable_type,omitempty"`
}

// PipelineMetadataRequest is the body of PUT /projects/:id/pipelines/:id/metadata.
type PipelineMetadataRequest struct {
	Name string `json:"name"`
}

// PlayJobRequest is the body of POST /projects/:id/jobs/:id/play.
type PlayJobRequest struct {
	JobVariablesAttributes []PipelineVariable `json:"job_variables_attributes,omitempty"`
}

// AccessLevelName renders a GitLab access level as its role name.
func AccessLevelName(level int) string {
	switch level {
	case 0:
		return "No access"
	case 5:
		return "Minimal"
	case 10:
		return "Guest"
	case 15:
		return "Planner"
	case 20:
		return "Reporter"
	case 30:
		return "Developer"
	case 40:
		return "Maintainer"
	case 50:
		return "Owner"
	default:
		return "level " + itoa(level)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
