package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"text/template"
	"time"

	git "github.com/libgit2/git2go/v33"
)

var config struct {
	ProjectName       string `json:"project_name"`
	ProjectRepoURL    string `json:"project_repo_url"`
	DiffURLTemplate   string `json:"diff_url_template"`
	CommitURLTemplate string `json:"commit_url_template"`
	CommitHashDigits  int    `json:"commit_hash_digits"`
}

// Don't need to edit below this line

const tmplPreamble = `
[{{.ProjectName}}]({{.ProjectRepoURL}}) Deployment<br>
{{.DateStringIST}} IST, {{.DateStringWIB}} WIB <br>
[Diff: {{.StartCommitID}}...{{.EndCommitID}}]({{.GitlabDiffURL}}) <br>
Authors: {{.AuthorListString}}
`
const commitInfoTableHeader = `
| Commit | Author | Message |
| ------ | ------ | --------|
`
const tmplCommitInfoLine = `|[{{.CommitID}}]({{.CommitURL}})|{{.CommitAuthor}}|{{.CommitMessage}}|`

var opts struct {
	startCommitID string
	endCommitID   string
	localRepoPath string
	outputFile    string
	configFile    string
}

func getCommit(repo *git.Repository, refOrHash string, desc string) *git.Commit {
	// Try to parse as hex bytes
	bytes, err := hex.DecodeString(refOrHash)
	if err == nil {
		commitID := git.NewOidFromBytes(bytes)
		if commitID == nil {
			log.Panicf("getCommit: failed to create oid from %s=%s", desc, refOrHash)
		}
		commit, err := repo.LookupCommit(commitID)
		if err != nil {
			log.Panicf("getCommit: failed to get commit from %s=%s", desc, refOrHash)
		}

		// log.Printf("getCommit: %s: commit message: %s", desc, commit.Message())
		log.Printf("getCommit: %s=%s: commit id: %s", desc, refOrHash, commit.Id().String())

		return commit
	}

	// log.Printf("%s=%s is not a hash string, treating as reference string", desc, refString)

	ref, err := repo.References.Lookup(refOrHash)
	if err != nil {
		log.Panicf("getCommit: failed to lookup reference %s=%s", desc, refOrHash)
	}

	switch ref.Type() {
	case git.ReferenceOid:
		targetOid := ref.Target()
		commit, err := repo.LookupCommit(targetOid)
		if err != nil {
			log.Panicf("getCommit: %s=%s: %v", desc, refOrHash, err)
		}

		// log.Printf("getCommit: %s: commit message: %s", desc, commit.Message())
		log.Printf("getCommit: %s=%s: commit id: %s", desc, refOrHash, commit.Id().String())
		return commit
	case git.ReferenceSymbolic:
		targetRefString := ref.SymbolicTarget()
		log.Printf("%s=%s points to %s", desc, refOrHash, targetRefString)

		targetRef, err := repo.References.Lookup(targetRefString)
		if err != nil {
			log.Panicf("getCommit: failed to lookup ref %s for %s=%s", targetRefString, desc, refOrHash)
		}
		object, err := targetRef.Peel(git.ObjectCommit)
		if err != nil {
			log.Panicf("getCommit: failed to get object %s for %s=%s: %v", targetRefString, desc, refOrHash, err)
		}
		commit, err := object.AsCommit()
		if err != nil {
			log.Panicf("getCommit: failed to get commit object %s for %s=%s: %v", targetRefString, desc, refOrHash, err)
		}
		// log.Printf("getCommit: %s=%s: commit message: %s", desc, refString, commit.Message())
		log.Printf("getCommit: %s=%s: commit id: %s", desc, refOrHash, commit.Id().String())
		return commit

	default:
		log.Panic("getCommit: should be unreachable code")
	}
	return nil
}

func getCommitChain(repo *git.Repository, end, start *git.Commit) []*git.Oid {
	// Check first that end is reachable from start
	reachable, err := repo.DescendantOf(end.Id(), start.Id())
	if err != nil {
		log.Panicf("failed to check if end commit is descendent of start commit: %v", err)
	}

	if !reachable {
		log.Panicf("ERROR: end-commit %s not reachable from start commit %s", end.Id().String(), start.Id().String())
	}

	// From end to start
	commits := make([]*git.Oid, 0)

	revWalker, err := repo.Walk()
	if err != nil {
		log.Panic(err)
	}

	revWalker.Sorting(git.SortTopological)

	err = revWalker.Push(end.Id())
	if err != nil {
		log.Panic(err)
	}

	curCommitID := new(git.Oid)

	for err := revWalker.Next(curCommitID); err == nil; err = revWalker.Next(curCommitID) {
		if curCommitID.Equal(start.Id()) {
			break
		}
		commits = append(commits, curCommitID)
		curCommitID = new(git.Oid) // Need to allocate new object, or Next() would overwrite the current one
	}
	if err != nil {
		log.Panicf("rev-walk stopped due to error: %v", err)
	}
	return commits
}

func firstLineOfMessage(message string) string {
	s := bufio.NewScanner(strings.NewReader(message))
	s.Scan()
	return s.Text()
}

type CommitInfo struct {
	CommitURL     string
	CommitID      string
	CommitAuthor  string
	CommitMessage string
}

func writePreamble(w io.Writer, repo *git.Repository, startCommitID, endCommitID *git.Oid, commitChain []*git.Oid) {
	preambleTemplate, err := template.New("preamble").Parse(tmplPreamble)
	if err != nil {
		log.Panic(err)
	}

	var preambleInfo struct {
		ProjectName      string
		ProjectRepoURL   string
		DateStringIST    string
		DateStringWIB    string
		AuthorListString string
		DiffURLInfo
	}

	preambleInfo.ProjectName = config.ProjectName
	preambleInfo.ProjectRepoURL = config.ProjectRepoURL
	preambleInfo.DiffURLInfo = makeDiffURL(w, startCommitID, endCommitID)

	now := time.Now()
	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		log.Panic(err)
	}

	wib, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		log.Panic(err)
	}

	nowIST := now.In(ist)
	nowWIB := now.In(wib)

	preambleInfo.DateStringIST = fmt.Sprintf("%02d-%s-%d %02d-%02d-%02d", nowIST.Day(), nowIST.Month().String(), nowIST.Year(), nowIST.Hour(), nowIST.Minute(), nowIST.Second())
	preambleInfo.DateStringWIB = fmt.Sprintf("%02d-%s-%d %02d-%02d-%02d", nowWIB.Day(), nowWIB.Month().String(), nowWIB.Year(), nowWIB.Hour(), nowWIB.Minute(), nowWIB.Second())
	preambleInfo.AuthorListString = getAuthorListString(repo, commitChain)

	err = preambleTemplate.Execute(w, &preambleInfo)
	if err != nil {
		log.Panic(err)
	}
	io.WriteString(w, "<br>")
}

type DiffURLInfo struct {
	StartCommitID string
	EndCommitID   string
	GitlabDiffURL string
}

func makeDiffURL(w io.Writer, endCommitID, startCommitID *git.Oid) DiffURLInfo {
	var diffURLInfo DiffURLInfo

	diffURLInfo.StartCommitID = startCommitID.String()
	diffURLInfo.EndCommitID = endCommitID.String()

	diffURLTemplate, err := template.New("diff_url").Parse(config.DiffURLTemplate)
	if err != nil {
		log.Panic(err)
	}

	url := bytes.NewBufferString("")

	err = diffURLTemplate.Execute(url, &diffURLInfo)
	if err != nil {
		log.Panic(err)
	}

	diffURLInfo.GitlabDiffURL = url.String()
	return diffURLInfo
}

func getAuthorListString(repo *git.Repository, commitChain []*git.Oid) string {
	// TODO: commitChain could be a []*git.Commit, i.e. get the structs from
	// the ids using repo.Lookup() once and pass that around.

	authors := make(map[string]struct{})

	for _, commitID := range commitChain {
		commit, err := repo.LookupCommit(commitID)
		if err != nil {
			log.Panic(err)
		}

		authors[commit.Author().Name] = struct{}{}
	}

	var sb strings.Builder

	for name, _ := range authors {
		sb.WriteString(name)
		sb.WriteRune(',')
	}
	return sb.String()
}

func writeCommitChain(repo *git.Repository, commitChain []*git.Oid, w io.Writer) {
	commitURLTemplate, err := template.New("commit_url").Parse(config.CommitURLTemplate)
	if err != nil {
		log.Panic(err)
	}

	commitInfoTemplate, err := template.New("commit_info").Parse(tmplCommitInfoLine)
	if err != nil {
		log.Panic(err)
	}

	io.WriteString(w, commitInfoTableHeader)

	for _, commitID := range commitChain {
		commit, err := repo.LookupCommit(commitID)
		if err != nil {
			log.Panic(err)
		}

		commitInfo := CommitInfo{
			CommitID:      string(truncateBytes([]byte(commit.Id().String()), config.CommitHashDigits)),
			CommitAuthor:  commit.Author().Name,
			CommitMessage: firstLineOfMessage(commit.Message()),
		}

		url := bytes.NewBufferString("")
		commitURLTemplate.Execute(url, &commitInfo)
		commitInfo.CommitURL = url.String()
		commitInfoTemplate.Execute(w, &commitInfo)
		io.WriteString(w, "\n")
		// io.WriteString(w, fmt.Sprintf("%s\t|%s|\t %s\n", commit.Author().Name, commit.Id(), firstLineOfMessage(commit.Message())))
	}
}

func truncateBytes(b []byte, n int) []byte {
	if n == -1 { // special
		return b
	}

	if len(b) < n {
		n = len(b)
	}
	return b[0 : n-1]
}

func parseJSONConfig(filepath string) {
	f, err := os.Open(filepath)
	if err != nil {
		log.Panicf("failed to read config file: %v", err)
	}

	err = json.NewDecoder(f).Decode(&config)
	if err != nil {
		log.Panicf("failed to parse config file: %v", err)
	}

	if config.CommitHashDigits <= 0 {
		config.CommitHashDigits = -1 // special
	}

	log.Printf(`config:
ProjectName       :%s
ProjectRepoURL    :%s
DiffURLTemplate   :%s
CommitURLTemplate :%s`, config.ProjectName, config.ProjectRepoURL, config.DiffURLTemplate, config.CommitURLTemplate)
}

func main() {
	defer func() {
		if err := recover(); err != nil {
			log.Printf("FAILED to generate changelog due to error: %v", err)
		}
	}()

	flag.StringVar(&opts.startCommitID, "start", "", "start commit ID")
	flag.StringVar(&opts.endCommitID, "end", "HEAD", "end commit ID")
	flag.StringVar(&opts.localRepoPath, "repo", "", "path to local repo")
	flag.StringVar(&opts.outputFile, "out", "", "path to output file")
	flag.StringVar(&opts.configFile, "config", "", "path to config.json")
	flag.Parse()

	if opts.configFile == "" {
		log.Panic("expected a config file as -config command-line argument")
	}

	parseJSONConfig(opts.configFile)

	var err error
	out := os.Stdout
	if opts.outputFile != "" {
		out, err = os.OpenFile(opts.outputFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
		if err != nil {
			log.Panic(err)
		}
		defer out.Close()
	}

	log.Default().SetFlags(log.Lshortfile | log.Ltime)

	if opts.localRepoPath == "" {
		opts.localRepoPath = "./"
	}

	repo, err := git.OpenRepository(opts.localRepoPath)

	if err != nil {
		log.Printf("failed to open repository %s: %v", opts.localRepoPath, err)
	}

	endCommit := getCommit(repo, opts.endCommitID, "end-commit")
	startCommit := getCommit(repo, opts.startCommitID, "start-commit")

	log.Printf("endCommit = %v, startCommit = %v", endCommit.Id(), startCommit.Id())

	commits := getCommitChain(repo, endCommit, startCommit)

	writePreamble(out, repo, endCommit.Id(), startCommit.Id(), commits)
	writeCommitChain(repo, commits, out)

	outputFile := opts.outputFile
	if outputFile == "" {
		outputFile = "<stdout>"
	}

	log.Printf("Output file: %s", outputFile)
}
