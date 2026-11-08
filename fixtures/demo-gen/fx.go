package main

import "time"

// Phase 5 fixtures: a .NET repo that builds offline and a frontend+backend pair
// of dependency-free dev servers. The two dev-server repos are meant to be
// cloned into two worktrees and run side by side on different ports.

var (
	ledgerStart = time.Date(2026, 4, 13, 8, 0, 0, 0, time.UTC)
	apiDevStart = time.Date(2026, 4, 20, 8, 0, 0, 0, time.UTC)
	webDevStart = time.Date(2026, 4, 27, 8, 0, 0, 0, time.UTC)
)

// buildShopLedger: .NET solution with a library and a console test project.
// No NuGet package is referenced, so restore, build and run need no network.
// The tests are a plain console program that exits non-zero on failure.
func buildShopLedger(b *builder) {
	b.commit("main", bram, "chore: initial .NET solution",
		f("ShopLedger.sln", ledgerSln),
		f("src/Ledger/Ledger.csproj", ledgerProj),
		f("src/Ledger/Account.cs", ledgerAccount),
		f("tests/Ledger.Tests/Ledger.Tests.csproj", ledgerTestsProj),
		f("tests/Ledger.Tests/Program.cs", ledgerTests),
		f(".gitignore", "bin/\nobj/\n.vs/\n"),
		f(".gitattributes", "*.sln text eol=crlf\n*.cs text eol=lf\n"),
		f("README.md", "# shop-ledger\n\nDouble-entry ledger for acme-shop.\n\n    dotnet build ShopLedger.sln\n    dotnet run --project tests/Ledger.Tests\n"))
	b.commit("main", dana, "feat(account): reject overdrafts",
		f("src/Ledger/Account.cs", ledgerAccountOverdraft),
		f("tests/Ledger.Tests/Program.cs", ledgerTestsOverdraft))
	b.push("main")
	b.checkout("main")
	b.note("clean .NET solution; builds and passes offline")
}

// buildShopAPIDev: Go dev server. `go run .` is the toolchain's dev command.
func buildShopAPIDev(b *builder) {
	b.commit("main", chen, "chore: initial dev API",
		f("go.mod", "module example.com/acme/shop-api-dev\n\ngo 1.22\n"),
		f("main.go", apiDevMain),
		f("main_test.go", apiDevTest),
		f(".gitignore", "bin/\n"),
		f("README.md", "# shop-api-dev\n\nTiny stand-in backend for acme-shop. Listens on $PORT (default 8081)\nand answers GET /api/info with its port and worktree.\n\n    PORT=18081 go run .\n"))
	b.push("main")
	b.checkout("main")
	b.note("dev server: go run . honours PORT")
}

// buildShopWebDev: Node dev server without dependencies. `npm run dev`.
func buildShopWebDev(b *builder) {
	b.commit("main", ada, "chore: initial dev web",
		f("package.json", webDevPackage),
		f("package-lock.json", webDevLock),
		f("server.js", webDevServer),
		f("test.js", webDevTest),
		f(".gitignore", "node_modules/\n"),
		f("README.md", "# shop-web-dev\n\nTiny stand-in storefront for acme-shop. Listens on $PORT (default 3000),\nproxies /api/* to the backend on API_PORT, else YGG_PORT_1, else 8081.\n\n    PORT=13000 YGG_PORT_1=18081 npm run dev\n"))
	b.push("main")
	b.checkout("main")
	b.note("dev server: npm run dev honours PORT")
}

const (
	slnProject = "{9A19103F-16F7-4668-BE54-9A1E7A4F7556}"
	slnLib     = "{3F1B6C52-0A41-4C5E-9B0D-7A2E5D1C0001}"
	slnTests   = "{3F1B6C52-0A41-4C5E-9B0D-7A2E5D1C0002}"
)

// ledgerSln uses CRLF like Visual Studio writes it.
const ledgerSln = "Microsoft Visual Studio Solution File, Format Version 12.00\r\n" +
	"# Visual Studio Version 17\r\n" +
	"VisualStudioVersion = 17.0.31903.59\r\n" +
	"MinimumVisualStudioVersion = 10.0.40219.1\r\n" +
	"Project(\"" + slnProject + "\") = \"Ledger\", \"src\\Ledger\\Ledger.csproj\", \"" + slnLib + "\"\r\nEndProject\r\n" +
	"Project(\"" + slnProject + "\") = \"Ledger.Tests\", \"tests\\Ledger.Tests\\Ledger.Tests.csproj\", \"" + slnTests + "\"\r\nEndProject\r\n" +
	"Global\r\n" +
	"\tGlobalSection(SolutionConfigurationPlatforms) = preSolution\r\n\t\tDebug|Any CPU = Debug|Any CPU\r\n\t\tRelease|Any CPU = Release|Any CPU\r\n\tEndGlobalSection\r\n" +
	"\tGlobalSection(ProjectConfigurationPlatforms) = postSolution\r\n" +
	"\t\t" + slnLib + ".Debug|Any CPU.ActiveCfg = Debug|Any CPU\r\n" +
	"\t\t" + slnLib + ".Debug|Any CPU.Build.0 = Debug|Any CPU\r\n" +
	"\t\t" + slnLib + ".Release|Any CPU.ActiveCfg = Release|Any CPU\r\n" +
	"\t\t" + slnLib + ".Release|Any CPU.Build.0 = Release|Any CPU\r\n" +
	"\t\t" + slnTests + ".Debug|Any CPU.ActiveCfg = Debug|Any CPU\r\n" +
	"\t\t" + slnTests + ".Debug|Any CPU.Build.0 = Debug|Any CPU\r\n" +
	"\t\t" + slnTests + ".Release|Any CPU.ActiveCfg = Release|Any CPU\r\n" +
	"\t\t" + slnTests + ".Release|Any CPU.Build.0 = Release|Any CPU\r\n" +
	"\tEndGlobalSection\r\n" +
	"\tGlobalSection(SolutionProperties) = preSolution\r\n\t\tHideSolutionNode = FALSE\r\n\tEndGlobalSection\r\n" +
	"EndGlobal\r\n"

const ledgerProj = `<Project Sdk="Microsoft.NET.Sdk">

  <PropertyGroup>
    <TargetFramework>net8.0</TargetFramework>
    <Nullable>enable</Nullable>
    <ImplicitUsings>enable</ImplicitUsings>
    <RootNamespace>Acme.Shop.Ledger</RootNamespace>
  </PropertyGroup>

</Project>
`

// The test project references no package: it is a console program, built
// without an apphost so no pack is fetched from NuGet.
const ledgerTestsProj = `<Project Sdk="Microsoft.NET.Sdk">

  <PropertyGroup>
    <OutputType>Exe</OutputType>
    <TargetFramework>net8.0</TargetFramework>
    <Nullable>enable</Nullable>
    <ImplicitUsings>enable</ImplicitUsings>
    <UseAppHost>false</UseAppHost>
    <IsPackable>false</IsPackable>
  </PropertyGroup>

  <ItemGroup>
    <ProjectReference Include="..\..\src\Ledger\Ledger.csproj" />
  </ItemGroup>

</Project>
`

const ledgerAccount = `namespace Acme.Shop.Ledger;

public sealed class Account
{
    public long Balance { get; private set; }

    public void Deposit(long cents) => Balance += cents;

    public void Withdraw(long cents) => Balance -= cents;
}
`

const ledgerAccountOverdraft = `namespace Acme.Shop.Ledger;

public sealed class Account
{
    public long Balance { get; private set; }

    public void Deposit(long cents) => Balance += cents;

    public void Withdraw(long cents)
    {
        if (cents > Balance)
        {
            throw new InvalidOperationException("insufficient funds");
        }
        Balance -= cents;
    }
}
`

const ledgerTests = `using Acme.Shop.Ledger;

var failures = 0;

void Check(bool ok, string name)
{
    if (!ok)
    {
        failures++;
        Console.Error.WriteLine("FAIL " + name);
    }
}

var a = new Account();
a.Deposit(500);
a.Withdraw(200);
Check(a.Balance == 300, "deposit then withdraw");

return failures == 0 ? 0 : 1;
`

const ledgerTestsOverdraft = `using Acme.Shop.Ledger;

var failures = 0;

void Check(bool ok, string name)
{
    if (!ok)
    {
        failures++;
        Console.Error.WriteLine("FAIL " + name);
    }
}

var a = new Account();
a.Deposit(500);
a.Withdraw(200);
Check(a.Balance == 300, "deposit then withdraw");

try
{
    a.Withdraw(1000);
    Check(false, "overdraft is rejected");
}
catch (InvalidOperationException)
{
}

return failures == 0 ? 0 : 1;
`

const apiDevMain = `// Command shop-api-dev is a dependency-free stand-in backend. It listens on
// $PORT (default 8081) and reports which worktree it runs in.
package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

type info struct {
	Service  string ` + "`json:\"service\"`" + `
	Port     string ` + "`json:\"port\"`" + `
	Worktree string ` + "`json:\"worktree\"`" + `
}

func worktreeName() string {
	if v := os.Getenv("YGG_WORKTREE"); v != "" {
		return v
	}
	wd, err := os.Getwd()
	if err != nil {
		return "unknown"
	}
	return filepath.Base(wd)
}

func handler(port string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info{Service: "shop-api-dev", Port: port, Worktree: worktreeName()})
	})
	return mux
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot listen:", err)
		os.Exit(1)
	}
	fmt.Printf("Listening on http://127.0.0.1:%s\n", port)
	if err := http.Serve(ln, handler(port)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
`

const apiDevTest = `package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInfo(t *testing.T) {
	rec := httptest.NewRecorder()
	handler("18081").ServeHTTP(rec, httptest.NewRequest("GET", "/api/info", nil))
	if !strings.Contains(rec.Body.String(), "\"port\":\"18081\"") {
		t.Fatalf("body %q does not report the port", rec.Body.String())
	}
}
`

const webDevPackage = `{
  "name": "shop-web-dev",
  "version": "0.1.0",
  "private": true,
  "scripts": {
    "dev": "node server.js",
    "test": "node test.js"
  }
}
`

const webDevLock = `{
  "name": "shop-web-dev",
  "version": "0.1.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "shop-web-dev",
      "version": "0.1.0"
    }
  }
}
`

const webDevServer = `'use strict';
// Dependency-free stand-in storefront. Listens on PORT (default 3000), serves
// a page naming its worktree and proxies /api/* to the backend. The backend
// port is API_PORT, else YGG_PORT_1, else 8081.
const http = require('node:http');
const path = require('node:path');

const port = Number(process.env.PORT || 3000);
const apiPort = Number(process.env.API_PORT || process.env.YGG_PORT_1 || 8081);
const worktree = process.env.YGG_WORKTREE || path.basename(process.cwd());

function page() {
  return '<!doctype html><title>shop-web-dev</title><h1>shop-web-dev</h1>' +
    '<p id="worktree">' + worktree + '</p><p id="port">' + port + '</p>';
}

const server = http.createServer((req, res) => {
  if (req.url === '/api' || req.url.startsWith('/api/')) {
    const up = http.request({ host: '127.0.0.1', port: apiPort, path: req.url, method: req.method, headers: req.headers }, (r) => {
      res.writeHead(r.statusCode || 502, r.headers);
      r.pipe(res);
    });
    up.on('error', () => {
      res.writeHead(502, { 'Content-Type': 'text/plain' });
      res.end('backend on port ' + apiPort + ' is not reachable\n');
    });
    req.pipe(up);
    return;
  }
  res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
  res.end(page());
});

server.listen(port, '127.0.0.1', () => {
  console.log('Listening on http://127.0.0.1:' + port);
});
`

const webDevTest = `'use strict';
// Starts the server on a free port and checks the startup line and the page.
const assert = require('node:assert');
const { spawn } = require('node:child_process');
const http = require('node:http');
const net = require('node:net');

const probe = net.createServer();
probe.listen(0, '127.0.0.1', () => {
  const port = probe.address().port;
  probe.close(() => {
    const child = spawn(process.execPath, ['server.js'], { env: { ...process.env, PORT: String(port), YGG_WORKTREE: 'wt-test' } });
    let out = '';
    child.stdout.on('data', (d) => {
      out += d;
      if (!out.includes('Listening on http://127.0.0.1:' + port)) return;
      http.get({ host: '127.0.0.1', port, path: '/' }, (res) => {
        let body = '';
        res.on('data', (c) => { body += c; });
        res.on('end', () => {
          child.kill();
          assert.ok(body.includes('wt-test'), 'page names the worktree');
          assert.ok(body.includes(String(port)), 'page names the port');
          console.log('ok');
        });
      });
    });
  });
});
`
