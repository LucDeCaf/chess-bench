package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"gonum.org/v1/gonum/stat"
)

type settings struct {
	Runs     int    `json:"runs"`
	Depth    int    `json:"depth"`
	BuildCmd string `json:"buildCmd"`
	RunCmd   string `json:"runCmd"`
}

type commit struct {
	Hash     string   `json:"hash"`
	Label    string   `json:"label"`
	Settings settings `json:"settings"`
}

type appConfig struct {
	BaseSettings settings `json:"baseSettings"`
	RemoteURL    string   `json:"remote"`
	Commits      []commit `json:"commits"`
	Pwd          string
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	return info.IsDir()
}

func applySettings(cfgSettings *settings, baseSettings settings) {
	if cfgSettings.Runs == 0 {
		cfgSettings.Runs = baseSettings.Runs
	}
	if cfgSettings.Depth == 0 {
		cfgSettings.Depth = baseSettings.Depth
	}
	if cfgSettings.BuildCmd == "" {
		cfgSettings.BuildCmd = baseSettings.BuildCmd
	}
	if cfgSettings.RunCmd == "" {
		cfgSettings.RunCmd = baseSettings.RunCmd
	}
}

func getConfig() (*appConfig, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	configPath := path.Join(pwd, "config.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var appConfig appConfig
	if err = json.Unmarshal(data, &appConfig); err != nil {
		return nil, err
	}
	appConfig.Pwd = pwd

	for i := range appConfig.Commits {
		commitSettings := &appConfig.Commits[i].Settings
		applySettings(commitSettings, appConfig.BaseSettings)
	}

	return &appConfig, nil
}

func getHash(h string, cfg *appConfig) (*plumbing.Hash, error) {
	hash := plumbing.ZeroHash

	if h == "HEAD" {
		remote := git.NewRemote(nil, &config.RemoteConfig{
			Name: "origin",
			URLs: []string{cfg.RemoteURL},
		})

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		refs, err := remote.ListContext(ctx, &git.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("ListContextError: %v", err)
		}

		var target plumbing.ReferenceName
		for _, r := range refs {
			if r.Name() == plumbing.HEAD {
				if r.Type() == plumbing.SymbolicReference {
					target = r.Target()
				} else {
					hash = r.Hash()
				}
				break
			}
		}

		// If head is symbolic reference, find head from target
		if target != "" {
			for _, r := range refs {
				if r.Name() == target {
					hash = r.Hash()
					break
				}
			}
		}

		if hash == plumbing.ZeroHash {
			return nil, fmt.Errorf("Failed to find HEAD commit hash")
		}
	} else {
		var ok bool
		hash, ok = plumbing.FromHex(h)
		if !ok {
			return nil, fmt.Errorf("Failed to parse commit hash: %v", h)
		}
	}

	return &hash, nil
}

func buildCommit(c *commit, cfg *appConfig) error {
	hash, err := getHash(c.Hash, cfg)
	if err != nil {
		return fmt.Errorf("Error getting hash: %v", err)
	}
	partialHash := hash.String()[:7]

	commitDir := path.Join(cfg.Pwd, "build", hash.String())

	// Clone repo
	if !dirExists(commitDir) {
		fmt.Printf("Cloning into %s...\n", commitDir)
		r, err := git.PlainClone(commitDir, &git.CloneOptions{
			URL:        cfg.RemoteURL,
			NoCheckout: true,
			Progress:   os.Stdout,
		})
		if err != nil {
			return fmt.Errorf("Error cloning repo: %v", err)
		}

		w, err := r.Worktree()
		if err != nil {
			return fmt.Errorf("Error getting worktree: %v", err)
		}

		err = w.Checkout(&git.CheckoutOptions{
			Hash: *hash,
		})
		if err != nil {
			return fmt.Errorf("Error checking out commit: %v", err)
		}
	}

	// Check SHA of commit settings
	json, err := json.Marshal(c.Settings)
	if err != nil {
		return fmt.Errorf("Error marshalling commit settings: %v", err)
	}
	fmt.Printf("Checking SHA: %v\n", partialHash)
	sha := sha256.Sum256(json)
	shaFile := path.Join(commitDir, "__bench")
	rebuild := false

	_, err = os.Stat(shaFile)
	if err == nil {
		// File exists
		data, err := os.ReadFile(shaFile)
		if err != nil {
			return fmt.Errorf("Error reading SHA file: %v")
		}

		// Only rebuild if settings have changed
		rebuild = !bytes.Equal(data, sha[:])
		if rebuild {
			fmt.Println("SHA mismatch")
		} else {
			fmt.Println("SHA verified")
		}
	}

	if rebuild || errors.Is(err, os.ErrNotExist) {
		err = os.WriteFile(shaFile, sha[:], 0644)
		if err != nil {
			return fmt.Errorf("Failed to write SHA: %v", err)
		}
		fmt.Println("SHA written")

		rebuild = true
	} else {
		return fmt.Errorf("Error with Stat: %v", err)
	}

	// Build project
	if rebuild {
		fmt.Printf("Building %s...\n", partialHash)
		err = os.Chdir(commitDir)
		if err != nil {
			return fmt.Errorf("Error changing dirs: %v", err)
		}
		cmd := exec.Command("bash", "-c", c.Settings.BuildCmd)
		err = cmd.Run()
		if err != nil {
			return fmt.Errorf("Error building commit: %v", err)
		}
		fmt.Println("Build complete")
	}

	return nil
}

func buildCommits(cfg *appConfig) error {
	buildPath := path.Join(cfg.Pwd, "build")
	os.MkdirAll(buildPath, os.ModePerm)

	var err error
	for i := range cfg.Commits {
		err = buildCommit(&cfg.Commits[i], cfg)
		if err != nil {
			log.Fatalf("Build error: %v", err)
		}
	}

	return nil
}

func runCommit(c *commit, cfg *appConfig) error {
	// Get commit hash and build dir path
	hash, err := getHash(c.Hash, cfg)
	if err != nil {
		return fmt.Errorf("Error getting hash: %v", err)
	}
	partialHash := hash.String()
	if len(partialHash) >= 8 {
		partialHash = partialHash[:7]
	}

	// Make sure commit is present and built
	commitDir := path.Join(cfg.Pwd, "build", hash.String())
	binDir := path.Join(commitDir, "bin")
	if !dirExists(commitDir) {
		return fmt.Errorf("Failed to locate commit directory for %v", partialHash)
	}
	if !dirExists(binDir) {
		return fmt.Errorf("Failed to locate bin directory for %v", partialHash)
	}

	err = os.Chdir(commitDir)
	if err != nil {
		return fmt.Errorf("Error changing dirs: %v", err)
	}

	// Run commit
	var cmd *exec.Cmd
	runCmd := strings.ReplaceAll(c.Settings.RunCmd, "%p", strconv.Itoa(c.Settings.Depth))

	// Run once to 'warm up' program
	cmd = exec.Command("bash", "-c", runCmd)
	err = cmd.Run()
	if err != nil {
		return fmt.Errorf("Failed to pre-bench run: %v\n", err)
	}

	fmt.Printf("Benchmarking %s... (%s)\n", partialHash, c.Label)

	runs := make([]float64, c.Settings.Runs, c.Settings.Runs)
	for i := 0; i < c.Settings.Runs; i++ {
		cmd = exec.Command("bash", "-c", runCmd)

		start := time.Now()
		err = cmd.Run()
		elapsed := time.Since(start)
		if err != nil {
			return fmt.Errorf("Failed to perform run %d: %v\n", i+1, err)
		}

		// Store milliseconds as micros / 1000 for more precision
		runs[i] = float64(elapsed.Microseconds()) / 1000.0
	}

	avgMillis := stat.Mean(runs, nil)
	stdDev := stat.StdDev(runs, nil)

	// Print results
	fmt.Printf("Runtimes (n=%d): { %.2f", c.Settings.Runs, runs[0])
	for _, r := range runs[1:] {
		fmt.Printf(", %.2f", r)
	}
	fmt.Printf(" }\n")
	fmt.Printf("Average runtime: %.2fms (σ=%.3f)\n", avgMillis, stdDev)

	return nil
}

func runCommits(cfg *appConfig) {
	buildPath := path.Join(cfg.Pwd, "build")
	os.MkdirAll(buildPath, os.ModePerm)

	fmt.Println("Running commits")
	for _, c := range cfg.Commits {
		err := runCommit(&c, cfg)
		if err != nil {
			log.Fatalf("Run error: %v", err)
		}
	}
}

func main() {
	config, err := getConfig()
	if err != nil {
		log.Fatal(err)
	}

	buildCommits(config)
	runCommits(config)
}
