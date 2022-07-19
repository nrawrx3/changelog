/*

libgit2@1.3.0 needs to be installed. Use macports since brew doesn't seem to
have it in official repos at the time of writing this.

Examples:

Commits can be denoted as hashes or refs. Make sure to pull and rebase changes
from origin before running command to avoid any confusion.

Common case for us (since latest deployment is always from head of master at the time of writing):

$ changelog -start refs/heads/master -end refs/heads/develop -out
changelog.md

Other common cases:

Using commit hashes only $ changelog -start
f5a78eba828b905cfb559a427e1afcceb5d337ca -end
9fda7b8c7c77b03f630973d4373d946adfaa76f7 -out /tmp/out.md

Commit hash and reference

$ changelog -start f5a78eba828b905cfb559a427e1afcceb5d337ca -end
refs/heads/develop -out changelog.md $ changelog -start refs/heads/master -end
HEAD -out changelog.md

Only remote references (without rebasing on local). Don't use this. $ changelog
-start refs/remotes/origin/master -end refs/remotes/origin/develop -out
changelog.md
*/

package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
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

// Project specific config. Edit as needed.
const tmplGitlabDiffURL = `https://source.golabs.io/fleet_monetization/campaign-manager-service/-/compare/{{.StartCommitID}}...{{.EndCommitID}}`
const tmplCommitURL = "https://source.golabs.io/fleet_monetization/campaign-manager-service/-/commit/{{.CommitID}}"

const commitHashTruncate = 8 // print this many digits in the commit column of the changelog table

// Don't need to edit below this line

const tmplPreamble = `
[Campaign Manager Service](https://source.golabs.io/fleet_monetization/campaign-manager-service) Deployment<br>
{{.DateStringIST}} IST, {{.DateStringWIB}} WIB <br>
[Diff: {{.StartCommitID}}...{{.EndCommitID}}]({{.GitlabDiffURL}}) <br>
Authors: {{.AuthorListString}}
`
const commitInfoTableHeader = `
| Commit | Author | Message |
| ------ | ------ | --------|
`
const tmplCommitInfoLine = `|[{{.CommitID}}]({{.CommitURL}})|{{.CommitAuthor}}|{{.CommitMessage}}|`

type cliOptions struct {
	startCommitID string
	endCommitID   string
	localRepoPath string
	outputFile    string
}

var opts cliOptions

func getCommit(repo *git.Repository, refString string, desc string) *git.Commit {
	// Try to parse as hex bytes
	bytes, err := hex.DecodeString(refString)
	if err == nil {
		commitID := git.NewOidFromBytes(bytes)
		if commitID == nil {
			log.Fatalf("getCommit: failed to create oid from %s=%s", desc, refString)
		}
		commit, err := repo.LookupCommit(commitID)
		if err != nil {
			log.Fatalf("getCommit: failed to get commit from %s=%s", desc, refString)
		}

		// log.Printf("getCommit: %s: commit message: %s", desc, commit.Message())
		log.Printf("getCommit: %s=%s: commit id: %s", desc, refString, commit.Id().String())

		return commit
	}

	// log.Printf("%s=%s is not parseable as hex, treating as reference", desc, refString)

	ref, err := repo.References.Lookup(refString)
	if err != nil {
		log.Fatalf("getCommit: failed to lookup reference %s=%s", desc, refString)
	}

	switch ref.Type() {
	case git.ReferenceOid:
		targetOid := ref.Target()
		commit, err := repo.LookupCommit(targetOid)
		if err != nil {
			log.Fatalf("getCommit: %s=%s: %v", desc, refString, err)
		}

		// log.Printf("getCommit: %s: commit message: %s", desc, commit.Message())
		log.Printf("getCommit: %s=%s: commit id: %s", desc, refString, commit.Id().String())
		return commit
	case git.ReferenceSymbolic:
		targetRefString := ref.SymbolicTarget()
		log.Printf("%s=%s points to %s", desc, refString, targetRefString)

		targetRef, err := repo.References.Lookup(targetRefString)
		if err != nil {
			log.Fatalf("getCommit: failed to lookup ref %s for %s=%s", targetRefString, desc, refString)
		}
		object, err := targetRef.Peel(git.ObjectCommit)
		if err != nil {
			log.Fatalf("getCommit: failed to get object %s for %s=%s: %v", targetRefString, desc, refString, err)
		}
		commit, err := object.AsCommit()
		if err != nil {
			log.Fatalf("getCommit: failed to get commit object %s for %s=%s: %v", targetRefString, desc, refString, err)
		}
		// log.Printf("getCommit: %s=%s: commit message: %s", desc, refString, commit.Message())
		log.Printf("getCommit: %s=%s: commit id: %s", desc, refString, commit.Id().String())
		return commit

	default:
		log.Fatal("getCommit: should be unreachable code here")
	}
	return nil
}

func getCommitChain(repo *git.Repository, end, start *git.Commit) []*git.Oid {
	// Check first that end is reachable from start
	reachable, err := repo.DescendantOf(end.Id(), start.Id())
	if err != nil {
		log.Fatalf("failed to check if end commit is descendent of start commit: %v", err)
	}

	if !reachable {
		log.Fatalf("ERROR: end-commit %s not reachable from start commit %s", end.Id().String(), start.Id().String())
	}

	// From end to start
	commits := make([]*git.Oid, 0)

	revWalker, err := repo.Walk()
	if err != nil {
		log.Fatal(err)
	}

	revWalker.Sorting(git.SortTopological)

	err = revWalker.Push(end.Id())
	if err != nil {
		log.Fatal(err)
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
		log.Fatalf("rev-walk stopped due to error: %v", err)
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
		log.Fatal(err)
	}

	now := time.Now()
	var preambleInfo struct {
		DateStringIST    string
		DateStringWIB    string
		AuthorListString string
		DiffURLInfo
	}

	preambleInfo.DiffURLInfo = makeDiffURL(w, startCommitID, endCommitID)

	ist, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		log.Fatal(err)
	}

	wib, err := time.LoadLocation("Asia/Jakarta")
	if err != nil {
		log.Fatal(err)
	}

	nowIST := now.In(ist)
	nowWIB := now.In(wib)

	preambleInfo.DateStringIST = fmt.Sprintf("%02d-%s-%d %02d-%02d-%02d", nowIST.Day(), nowIST.Month().String(), nowIST.Year(), nowIST.Hour(), nowIST.Minute(), nowIST.Second())
	preambleInfo.DateStringWIB = fmt.Sprintf("%02d-%s-%d %02d-%02d-%02d", nowWIB.Day(), nowWIB.Month().String(), nowWIB.Year(), nowWIB.Hour(), nowWIB.Minute(), nowWIB.Second())
	preambleInfo.AuthorListString = getAuthorListString(repo, commitChain)

	err = preambleTemplate.Execute(w, &preambleInfo)
	if err != nil {
		log.Fatal(err)
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

	diffURLTemplate, err := template.New("diff_url").Parse(tmplGitlabDiffURL)
	if err != nil {
		log.Fatal(err)
	}

	url := bytes.NewBufferString("")

	err = diffURLTemplate.Execute(url, &diffURLInfo)
	if err != nil {
		log.Fatal(err)
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
			log.Fatal(err)
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
	commitURLTemplate, err := template.New("commit_url").Parse(tmplCommitURL)
	if err != nil {
		log.Fatal(err)
	}

	commitInfoTemplate, err := template.New("commit_info").Parse(tmplCommitInfoLine)
	if err != nil {
		log.Fatal(err)
	}

	io.WriteString(w, commitInfoTableHeader)

	for _, commitID := range commitChain {
		commit, err := repo.LookupCommit(commitID)
		if err != nil {
			log.Fatal(err)
		}

		commitInfo := CommitInfo{
			CommitID:      string(truncateBytes([]byte(commit.Id().String()), commitHashTruncate)),
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
	if len(b) < n {
		n = len(b)
	}
	return b[0 : n-1]
}

func main() {
	flag.StringVar(&opts.startCommitID, "start", "", "start commit ID")
	flag.StringVar(&opts.endCommitID, "end", "HEAD", "end commit ID")
	flag.StringVar(&opts.localRepoPath, "repo", "", "path to local repo")
	flag.StringVar(&opts.outputFile, "out", "", "path to output file")
	flag.Parse()

	var err error

	out := os.Stdout
	if opts.outputFile != "" {
		out, err = os.OpenFile(opts.outputFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0666)
		if err != nil {
			log.Fatal(err)
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
}
