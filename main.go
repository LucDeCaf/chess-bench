package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
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
	Commits      []commit `json:"commits"`
	Pwd          string
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

	var config appConfig
	if err = json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	config.Pwd = pwd

	for _, c := range config.Commits {
		applySettings(&c.Settings, config.BaseSettings)
	}

	return &config, nil
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

func runCommit(c *commit, dir string) error {
	commitPath := path.Join(dir, c.Hash)

	// Clone repo
	info, err := os.Stat(commitPath)
	if err != nil || !info.IsDir() {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}

		// Clone repo
		fmt.Printf("Cloning into %s...\n", commitPath)
		r, err := git.PlainClone(commitPath, &git.CloneOptions{
			URL:        "https://github.com/LucDeCaf/c-chess",
			NoCheckout: true,
			Progress:   os.Stdout,
		})
		if err != nil {
			return err
		}

		w, err := r.Worktree()
		if err != nil {
			return err
		}

		hash, ok := plumbing.FromHex(c.Hash)
		if !ok {
			return fmt.Errorf("Invalid hash: %s", c.Hash)
		}
		err = w.Checkout(&git.CheckoutOptions{
			Hash: hash,
		})
		if err != nil {
			return err
		}
	} else {
		fmt.Printf("Found commit %s\n", commitPath)
		fmt.Printf("%+v", c)
	}

	// Build commit
	err = os.Chdir(commitPath)
	if err != nil {
		return err
	}
	cmd := exec.Command("bash", "-c", c.Settings.BuildCmd)
	err = cmd.Run()
	if err != nil {
		return err
	}

	// Run commit
	cmd = exec.Command("bash", "-c", "time", c.Settings.RunCmd)
	err = cmd.Run()
	if err != nil {
		return err
	}

	return nil
}

func runCommits(cfg *appConfig) {
	buildPath := path.Join(cfg.Pwd, "build")
	os.MkdirAll(buildPath, os.ModePerm)

	fmt.Println("Running commits")
	for _, c := range cfg.Commits {
		err := runCommit(&c, buildPath)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func main() {
	config, err := getConfig()
	if err != nil {
		log.Fatal(err)
	}

	runCommits(config)
}
