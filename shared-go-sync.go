package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/google/go-github/v50/github"
	"golang.org/x/mod/modfile"
	"golang.org/x/oauth2"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Dependency Struct to hold the dependency information
type Dependency struct {
	Name    string
	Version string
}

func getGoModFromURL(url string) (*modfile.File, error) {
	// Fetch go.mod file from the provided URL
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("could not fetch go.mod from URL: %v", err)
	}
	defer resp.Body.Close()

	// Parse the go.mod file
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("could not read response body: %v", err)
	}
	modFile, err := modfile.Parse("go.mod", body, nil)

	if err != nil {
		return nil, fmt.Errorf("could not parse go.mod: %v", err)
	}
	return modFile, nil
}

func extractDependencies(modFile *modfile.File) map[string]string {
	deps := make(map[string]string)
	for _, req := range modFile.Require {
		deps[req.Mod.Path] = req.Mod.Version
	}
	return deps
}

func loadIgnoredDependencies(path string) map[string]bool {
	ignored := make(map[string]bool)
	log.Print("Path to ignore file: ")
	log.Print(path)
	if path == "" {
		return ignored
	}
	data, err := os.ReadFile(path)

	if err != nil {
		log.Printf("Warning: could not read ignore file: %v", err)
		return ignored
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			ignored[line] = true
		}
	}
	return ignored
}

func printDifferences(sharedDeps, projectDeps map[string]string, ignored map[string]bool) {
	fmt.Println("Differences in dependencies:")
	for dep, version := range projectDeps {
		if ignored[dep] {
			continue
		}
		if sharedVersion, ok := sharedDeps[dep]; ok {
			if version != sharedVersion {
				fmt.Printf("Dependency: %s\n  Project Version: %s\n  Shared Version: %s\n", dep, version, sharedVersion)
			}
		}
	}
}

func createPR(client *github.Client, ctx context.Context, owner, repo string, updates map[string]string, goModPath string) error {
	baseBranch := "main"
	branchName := "update-deps-" + fmt.Sprint(time.Now().Unix())

	// 1. Get base branch reference (main)
	baseRef, _, err := client.Git.GetRef(ctx, owner, repo, "refs/heads/"+baseBranch)
	if err != nil {
		return fmt.Errorf("failed to get base branch: %v", err)
	}

	// 2. Create a new branch from base
	newRef := &github.Reference{
		Ref: github.String("refs/heads/" + branchName),
		Object: &github.GitObject{
			SHA: baseRef.Object.SHA,
		},
	}
	_, _, err = client.Git.CreateRef(ctx, owner, repo, newRef)
	if err != nil {
		return fmt.Errorf("failed to create branch: %v", err)
	}

	// 3. Get latest go.mod content from the repo
	fileContent, _, _, err := client.Repositories.GetContents(ctx, owner, repo, goModPath, &github.RepositoryContentGetOptions{Ref: baseBranch})
	if err != nil {
		return fmt.Errorf("failed to get go.mod content: %v", err)
	}
	content, err := fileContent.GetContent()
	if err != nil {
		return fmt.Errorf("failed to decode go.mod content: %v", err)
	}

	modFile, err := modfile.Parse(goModPath, []byte(content), nil)
	if err != nil {
		return fmt.Errorf("failed to parse go.mod: %v", err)
	}

	// 4. Update required versions in go.mod
	for path, newVersion := range updates {
		err := modFile.AddRequire(path, newVersion)
		if err != nil {
			log.Printf("warning: could not update %s: %v", path, err)
		}
	}

	newContent, _ := modFile.Format()

	// 5. Commit updated go.mod to new branch
	options := &github.RepositoryContentFileOptions{
		Message: github.String("chore: update dependencies to match shared-deps"),
		Content: newContent,
		SHA:     fileContent.SHA,
		Branch:  github.String(branchName),
	}
	_, _, err = client.Repositories.UpdateFile(ctx, owner, repo, goModPath, options)
	if err != nil {
		return fmt.Errorf("failed to commit updated go.mod: %v", err)
	}

	// 6. Create the pull request
	newPR := &github.NewPullRequest{
		Title: github.String("Sync dependencies with shared-deps"),
		Head:  github.String(branchName),
		Base:  github.String(baseBranch),
		Body:  github.String("This PR updates go.mod to match versions from shared-deps."),
	}
	_, _, err = client.PullRequests.Create(ctx, owner, repo, newPR)
	if err != nil {
		return fmt.Errorf("failed to create PR: %v", err)
	}

	return nil
}

func main() {
	// Define command-line flags
	sharedModURL := flag.String("shared-mod-url", "", "URL to the shared go.mod file.")
	githubToken := flag.String("github-token", "", "GitHub token for authentication.")
	repoOwner := flag.String("repo-owner", "", "GitHub repository owner.")
	repoName := flag.String("repo-name", "", "GitHub repository name.")
	ignoreFile := flag.String("ignore-file", "", "Path to a file listing dependencies to ignore.")
	folderPath := flag.String("folder-path", "", "Path to the folder containing the go.mod file.")
	flag.Parse()

	// Decide the path to go.mod
	goModPath := "go.mod"
	if *folderPath != "" {
		goModPath = *folderPath + "/go.mod"
	}

	// Check required flags
	if *sharedModURL == "" || *githubToken == "" || *repoOwner == "" || *repoName == "" {
		log.Fatal("All flags must be provided.")
	}

	// Fetch the shared go.mod from the provided URL
	sharedModFile, err := getGoModFromURL(*sharedModURL)
	if err != nil {
		log.Fatalf("Error fetching shared go.mod: %v", err)
	}

	// Extract dependencies from the shared go.mod file
	sharedDeps := extractDependencies(sharedModFile)

	// Fetch the go.mod from the project's GitHub repository
	client := github.NewClient(oauth2.NewClient(oauth2.NoContext, oauth2.StaticTokenSource(&oauth2.Token{AccessToken: *githubToken})))
	// GitHub API to fetch the go.mod file from the repository
	fileContent, _, _, err := client.Repositories.GetContents(oauth2.NoContext, *repoOwner, *repoName, goModPath, nil)
	if err != nil {
		log.Fatalf("Error fetching go.mod from repository: %v", err)
	}

	// Decode the content of go.mod from the GitHub API (Base64 encoded)
	decodedContent, err := fileContent.GetContent()
	if err != nil {
		log.Fatalf("Error decoding go.mod content: %v", err)
	}

	// Parse the go.mod file content for the project
	projectModFile, err := modfile.Parse("go.mod", []byte(decodedContent), nil)
	if err != nil {
		log.Fatalf("Error parsing project go.mod: %v", err)
	}

	// Extract dependencies from the project's go.mod file
	projectDeps := extractDependencies(projectModFile)
	ignorePath := *ignoreFile
	if ignorePath == "" {
		ignorePath = ".depignore"
	}
	ignoredDeps := loadIgnoredDependencies(ignorePath)

	// Print the differences
	printDifferences(sharedDeps, projectDeps, ignoredDeps)

	// Build a map of differences
	diff := make(map[string]string)
	for dep, version := range projectDeps {
		if sharedVersion, ok := sharedDeps[dep]; ok && version != sharedVersion {
			diff[dep] = sharedVersion
		}
	}

	if len(diff) > 0 {
		ctx := context.Background()
		err := createPR(client, ctx, *repoOwner, *repoName, diff, goModPath)
		if err != nil {
			log.Fatalf("Failed to create PR: %v", err)
		} else {
			fmt.Println("✅ Pull request created successfully!")
		}
	} else {
		fmt.Println("No changes needed. Versions match shared-deps.")
	}
}
