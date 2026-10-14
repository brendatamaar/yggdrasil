package agentstream

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// scratchFiles is the fictional content of the scratch repo.
var scratchFiles = map[string]string{
	"README.md": "# acme-shop\n\nA fictional online shop used to test agent runs. Nothing here is real.\n",
	"src/cart.js": "// Fictional cart for the acme-shop demo.\n" +
		"export function total(items) {\n" +
		"  return items.reduce((sum, item) => sum + item.price * item.qty, 0);\n" +
		"}\n",
}

// NewScratchRepo creates root/acme-shop as a throwaway git repo with one
// commit and returns its path. The agent only ever runs here, never in a real
// repo. Git runs with the clean env and without system or global config.
func NewScratchRepo(root string) (string, error) {
	repo := filepath.Join(root, "acme-shop")
	for name, body := range scratchFiles {
		p := filepath.Join(repo, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return "", fmt.Errorf("scratch repo: %w", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			return "", fmt.Errorf("scratch repo: %w", err)
		}
	}
	emptyCfg := filepath.Join(root, "empty.gitconfig")
	if err := os.WriteFile(emptyCfg, nil, 0o600); err != nil {
		return "", fmt.Errorf("scratch repo: %w", err)
	}
	env, err := CleanEnv(os.LookupEnv, map[string]string{
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_CONFIG_GLOBAL":   emptyCfg,
		"GIT_TERMINAL_PROMPT": "0",
		"GIT_AUTHOR_NAME":     "Acme Dev",
		"GIT_AUTHOR_EMAIL":    "dev@acme-shop.invalid",
		"GIT_COMMITTER_NAME":  "Acme Dev",
		"GIT_COMMITTER_EMAIL": "dev@acme-shop.invalid",
	})
	if err != nil {
		return "", err
	}
	for _, args := range [][]string{
		{"-c", "init.defaultBranch=main", "init", "-q"},
		{"add", "-A"},
		{"-c", "commit.gpgsign=false", "commit", "-q", "-m", "Initial acme-shop"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repo, env
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("scratch repo: git %s: %w: %s", args, err, bytes.TrimSpace(out.Bytes()))
		}
	}
	return repo, nil
}
