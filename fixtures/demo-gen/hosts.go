package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// hostFixtures are recorded-style answers of the three git host APIs for the
// demo project: merge/pull requests and pipelines/checks. They have the same
// shape as the fixtures in internal/hosts/*/testdata, so a test can serve them
// from an httptest.Server. Every name is made up and every host ends in
// .example, a name that never resolves.
//
// Mapping: shop-api is hosted on the GitLab-style host, shop-web on the
// GitHub-style host and shop-admin on the Gitea-style host.
var hostFixtures = map[string]string{
	"README.md": `# Recorded host answers (fictional)

Serve these files from an httptest.Server; never call a real host.

- gitlab/mrs.json, gitlab/pipelines.json: shop-api (git.acme-shop.example)
- github/pulls.json, github/checkruns.json: shop-web (code.acme-shop.example)
- gitea/pulls.json, gitea/status.json: shop-admin (forge.acme-shop.example)
`,

	"gitlab/mrs.json": `[
  {
    "id": 9042,
    "iid": 42,
    "project_id": 12,
    "title": "Add cart totals",
    "state": "opened",
    "draft": false,
    "updated_at": "2026-10-12T09:30:00.000Z",
    "source_branch": "feature/cart-totals",
    "target_branch": "main",
    "author": { "id": 3, "username": "chen.wei" },
    "web_url": "https://git.acme-shop.example/acme-shop/shop-api/-/merge_requests/42",
    "references": { "full": "acme-shop/shop-api!42" }
  },
  {
    "id": 9043,
    "iid": 43,
    "project_id": 12,
    "title": "Draft: round order totals half-up",
    "state": "opened",
    "draft": true,
    "updated_at": "2026-10-13T11:00:00.000Z",
    "source_branch": "fix/order-rounding",
    "target_branch": "main",
    "author": { "id": 4, "username": "dana.ruiz" },
    "web_url": "https://git.acme-shop.example/acme-shop/shop-api/-/merge_requests/43",
    "references": { "full": "acme-shop/shop-api!43" }
  },
  {
    "id": 9040,
    "iid": 40,
    "project_id": 12,
    "title": "Reject empty carts",
    "state": "merged",
    "draft": false,
    "updated_at": "2026-10-10T16:20:00.000Z",
    "source_branch": "fix/empty-carts",
    "target_branch": "main",
    "author": { "id": 1, "username": "ada.lindqvist" },
    "web_url": "https://git.acme-shop.example/acme-shop/shop-api/-/merge_requests/40",
    "references": { "full": "acme-shop/shop-api!40" }
  }
]
`,
	"gitlab/mrs.headers": "ETag: \"mrs-v1\"\n",

	"gitlab/pipelines.json": `[
  {
    "id": 771,
    "status": "failed",
    "ref": "feature/cart-totals",
    "web_url": "https://git.acme-shop.example/acme-shop/shop-api/-/pipelines/771",
    "updated_at": "2026-10-12T09:45:00.000Z"
  },
  {
    "id": 770,
    "status": "success",
    "ref": "main",
    "web_url": "https://git.acme-shop.example/acme-shop/shop-api/-/pipelines/770",
    "updated_at": "2026-10-11T08:10:00.000Z"
  },
  {
    "id": 772,
    "status": "running",
    "ref": "fix/order-rounding",
    "web_url": "https://git.acme-shop.example/acme-shop/shop-api/-/pipelines/772",
    "updated_at": "2026-10-13T11:05:00.000Z"
  }
]
`,
	"gitlab/pipelines.headers": "ETag: \"pipes-v1\"\n",

	"github/pulls.json": `[
  {
    "number": 17,
    "title": "Checkout page skeleton",
    "state": "open",
    "draft": false,
    "merged_at": null,
    "updated_at": "2026-10-12T09:30:00Z",
    "html_url": "https://code.acme-shop.example/acme-shop/shop-web/pull/17",
    "user": { "login": "ada-lindqvist" },
    "head": { "ref": "feature/checkout-page" },
    "base": { "ref": "main" }
  },
  {
    "number": 16,
    "title": "Raise login timeout",
    "state": "closed",
    "draft": false,
    "merged_at": "2026-10-09T14:00:00Z",
    "updated_at": "2026-10-09T14:00:00Z",
    "html_url": "https://code.acme-shop.example/acme-shop/shop-web/pull/16",
    "user": { "login": "bram-okafor" },
    "head": { "ref": "fix/login-timeout" },
    "base": { "ref": "main" }
  }
]
`,
	"github/pulls.headers": "ETag: \"pulls-v1\"\n",

	"github/checkruns.json": `{
  "total_count": 2,
  "check_runs": [
    {
      "id": 1,
      "name": "ci",
      "status": "completed",
      "conclusion": "success",
      "html_url": "https://code.acme-shop.example/acme-shop/shop-web/actions/runs/31",
      "started_at": "2026-10-12T09:40:00Z",
      "completed_at": "2026-10-12T09:45:00Z"
    },
    {
      "id": 2,
      "name": "lint",
      "status": "completed",
      "conclusion": "failure",
      "html_url": "https://code.acme-shop.example/acme-shop/shop-web/actions/runs/32",
      "started_at": "2026-10-12T09:40:00Z",
      "completed_at": "2026-10-12T09:42:00Z"
    }
  ]
}
`,
	"github/checkruns.headers": "ETag: \"checks-v1\"\n",

	"gitea/pulls.json": `[
  {
    "number": 9,
    "title": "List admin users",
    "state": "open",
    "merged": false,
    "draft": false,
    "updated_at": "2026-10-12T09:30:00Z",
    "html_url": "https://forge.acme-shop.example/acme-shop/shop-admin/pulls/9",
    "user": { "login": "ada-lindqvist" },
    "head": { "ref": "feature/user-roles" },
    "base": { "ref": "main" }
  },
  {
    "number": 8,
    "title": "Export sales as CSV",
    "state": "open",
    "merged": false,
    "draft": true,
    "updated_at": "2026-10-11T12:00:00Z",
    "html_url": "https://forge.acme-shop.example/acme-shop/shop-admin/pulls/8",
    "user": { "login": "chen-wei" },
    "head": { "ref": "feature/sales-report" },
    "base": { "ref": "main" }
  }
]
`,
	"gitea/pulls.headers": "ETag: \"pulls-v1\"\n",

	"gitea/status.json": `{
  "state": "pending",
  "sha": "0123abc",
  "total_count": 1,
  "statuses": [
    {
      "status": "pending",
      "context": "ci",
      "target_url": "https://forge.acme-shop.example/acme-shop/shop-admin/actions/runs/5",
      "updated_at": "2026-10-12T09:45:00Z"
    }
  ]
}
`,
}

// writeHostFixtures writes hostFixtures under dir/hosts. The folder is new
// (the output folder was empty), so nothing is overwritten.
func writeHostFixtures(dir string) error {
	names := make([]string, 0, len(hostFixtures))
	for n := range hostFixtures {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(dir, "hosts", filepath.FromSlash(n))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fmt.Errorf("create folder for hosts/%s: %w", n, err)
		}
		if err := os.WriteFile(p, []byte(hostFixtures[n]), 0o644); err != nil {
			return fmt.Errorf("write hosts/%s: %w", n, err)
		}
	}
	return nil
}
