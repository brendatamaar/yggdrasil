<#
.SYNOPSIS
  Windows smoke test of the built bin\ygg.exe (the Phase 1 manual checklist,
  docs/phases/phase-1.md).

.DESCRIPTION
  Default: checks 1 to 8 with the demo agent and a fake Claude Code shim. No
  real agent, no network. Used by `task smoke` and CI.

  -RealAgent: checks 6b, 6c and 6d with the real Claude Code CLI (model haiku,
  short prompts; uses a little of the user's quota). Stops at the first
  failure. Used by `task smoke:real`; never run it in CI.

  Every run uses a temporary data folder (YGG_DATA_DIR), its own loopback port
  and YGG_MOCK_DELAY_MS=0. The finally block stops the processes this script
  started (by PID, never by image name) and removes the temporary folder.
  Works with Windows PowerShell 5.1 and PowerShell 7, without admin rights.
#>
[CmdletBinding()]
param([switch]$RealAgent)

Set-StrictMode -Version 1.0
$ErrorActionPreference = 'Stop'

$RepoRoot = Split-Path -Parent $PSScriptRoot
$Ygg = Join-Path $RepoRoot 'bin\ygg.exe'
$RunId = [guid]::NewGuid().ToString('N').Substring(0, 8)
# The folder name has a space on purpose: every repo below it does too.
$TempRoot = Join-Path ([IO.Path]::GetTempPath()) "ygg smoke $RunId"
$DataDir = Join-Path $TempRoot 'data'
$FakeDir = Join-Path $TempRoot 'Program Files\nodejs'

$script:Results = New-Object System.Collections.Generic.List[object]
$script:Notes = New-Object System.Collections.Generic.List[string]
# Processes this script started, directly (Proc set) or through ygg (found in
# its process tree; identified by PID and creation time).
$script:Owned = New-Object System.Collections.Generic.List[object]
$script:Servers = New-Object System.Collections.Generic.List[object]
$script:Stop = $false

# --- results ---------------------------------------------------------------

function Add-Result([string]$Id, [bool]$Ok, [string]$Reason) {
    $script:Results.Add([pscustomobject]@{ Id = $Id; Ok = $Ok; Reason = $Reason })
    $word = 'FAIL'; $color = 'Red'
    if ($Ok) { $word = 'PASS'; $color = 'Green' }
    Write-Host ('{0}  {1,-5} {2}' -f $word, $Id, $Reason) -ForegroundColor $color
}

# Invoke-Check runs one check. The body throws a short reason on failure and
# returns a short summary on success. With -RealAgent the first failure stops
# the run.
function Invoke-Check([string]$Id, [string]$Name, [scriptblock]$Body) {
    if ($script:Stop) { return }
    try {
        $out = @(& $Body)
        $summary = 'ok'
        if ($out.Count -gt 0) { $summary = [string]$out[-1] }
        Add-Result $Id $true "${Name}: $summary"
    } catch {
        Add-Result $Id $false "${Name}: $($_.Exception.Message)"
        if ($RealAgent) { $script:Stop = $true }
    }
}

function Get-Tail([string]$Text, [int]$Lines = 15) {
    if (-not $Text) { return '' }
    $all = @($Text -split "`r?`n" | Where-Object { $_ -ne '' })
    return ($all | Select-Object -Last $Lines) -join "`n"
}

# --- processes -------------------------------------------------------------

function ConvertTo-Arg([string]$Value) {
    if ($Value -ne '' -and $Value -notmatch '[\s"]') { return $Value }
    $q = $Value -replace '(\\*)"', '$1$1\"'
    $q = $q -replace '(\\+)$', '$1$1'
    return '"' + $q + '"'
}

# Start-Proc starts a process with extra environment variables for that process
# only (a $null value removes one). Its output is drained in the background.
function Start-Proc {
    param([string]$File, [string[]]$ArgList = @(), [hashtable]$EnvVars = @{}, [string]$Dir = '')
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = $File
    $psi.Arguments = (@($ArgList | ForEach-Object { ConvertTo-Arg $_ })) -join ' '
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $psi.RedirectStandardInput = $true
    $psi.RedirectStandardOutput = $true
    $psi.RedirectStandardError = $true
    if ($Dir) { $psi.WorkingDirectory = $Dir }
    foreach ($k in @($EnvVars.Keys)) {
        if ($null -eq $EnvVars[$k]) { $psi.EnvironmentVariables.Remove($k) }
        else { $psi.EnvironmentVariables[$k] = [string]$EnvVars[$k] }
    }
    $p = [System.Diagnostics.Process]::Start($psi)
    $script:Owned.Add([pscustomobject]@{ Proc = $p; Id = $p.Id; Created = $null; Name = [IO.Path]::GetFileName($File) })
    $p.StandardInput.Close()
    return [pscustomobject]@{
        Process = $p; Id = $p.Id
        Out = $p.StandardOutput.ReadToEndAsync(); Err = $p.StandardError.ReadToEndAsync()
    }
}

function Invoke-Proc {
    param([string]$File, [string[]]$ArgList = @(), [hashtable]$EnvVars = @{}, [string]$Dir = '', [int]$TimeoutSec = 120)
    $h = Start-Proc -File $File -ArgList $ArgList -EnvVars $EnvVars -Dir $Dir
    if (-not $h.Process.WaitForExit($TimeoutSec * 1000)) {
        $h.Process.Kill()
        throw "$([IO.Path]::GetFileName($File)) $($ArgList -join ' ') did not finish within $TimeoutSec s"
    }
    $h.Process.WaitForExit()
    return [pscustomobject]@{ Code = $h.Process.ExitCode; Out = $h.Out.Result; Err = $h.Err.Result }
}

function Get-ProcessTable { return @(Get-CimInstance -ClassName Win32_Process) }

# Get-Descendants returns the processes below RootId. A child must be younger
# than its parent, so a reused PID never links a stranger into the tree.
function Get-Descendants([int]$RootId) {
    $all = Get-ProcessTable
    $byId = @{}; $kids = @{}
    foreach ($p in $all) {
        $byId[[int]$p.ProcessId] = $p
        $pp = [int]$p.ParentProcessId
        if (-not $kids.ContainsKey($pp)) { $kids[$pp] = New-Object System.Collections.ArrayList }
        [void]$kids[$pp].Add($p)
    }
    $out = New-Object System.Collections.ArrayList
    if (-not $byId.ContainsKey($RootId)) { return @() }
    $queue = New-Object System.Collections.Queue
    $queue.Enqueue($byId[$RootId])
    $seen = @{ $RootId = $true }
    while ($queue.Count -gt 0) {
        $parent = $queue.Dequeue()
        $id = [int]$parent.ProcessId
        if (-not $kids.ContainsKey($id)) { continue }
        foreach ($c in $kids[$id]) {
            $cid = [int]$c.ProcessId
            if ($seen.ContainsKey($cid) -or $c.CreationDate -lt $parent.CreationDate) { continue }
            $seen[$cid] = $true
            [void]$out.Add($c)
            $queue.Enqueue($c)
        }
    }
    return @($out)
}

function Add-OwnedTree($Tree) {
    foreach ($c in $Tree) {
        $known = $false
        foreach ($o in $script:Owned) { if ($o.Id -eq [int]$c.ProcessId -and $o.Created -eq $c.CreationDate) { $known = $true; break } }
        if (-not $known) { $script:Owned.Add([pscustomobject]@{ Proc = $null; Id = [int]$c.ProcessId; Created = $c.CreationDate; Name = $c.Name }) }
    }
}

function Test-OwnedAlive($o) {
    if ($o.Proc) { return -not $o.Proc.HasExited }
    $c = Get-CimInstance -ClassName Win32_Process -Filter "ProcessId=$($o.Id)" -ErrorAction SilentlyContinue
    return ($null -ne $c -and $c.CreationDate -eq $o.Created)
}

# --- HTTP ------------------------------------------------------------------

Add-Type -AssemblyName System.Net.Http
$script:Handler = New-Object System.Net.Http.HttpClientHandler
$script:Handler.UseProxy = $false
$script:Handler.AllowAutoRedirect = $false
$script:Http = New-Object System.Net.Http.HttpClient($script:Handler)
$script:Http.Timeout = [TimeSpan]::FromSeconds(15)
$script:BaseUrl = ''

function Invoke-Http([string]$Method, [string]$Path, [string]$Body = '', [hashtable]$Headers = @{}) {
    $req = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::new($Method), [Uri]($script:BaseUrl + $Path))
    foreach ($k in @($Headers.Keys)) {
        if ($k -eq 'Host') { $req.Headers.Host = $Headers[$k] }
        else { [void]$req.Headers.TryAddWithoutValidation($k, [string]$Headers[$k]) }
    }
    if ($Body) { $req.Content = [System.Net.Http.StringContent]::new($Body, [Text.Encoding]::UTF8, 'application/json') }
    try {
        $resp = $script:Http.SendAsync($req).GetAwaiter().GetResult()
    } catch {
        $req.Dispose()
        return [pscustomobject]@{ Status = 0; Body = $_.Exception.Message; Headers = '' }
    }
    try {
        $text = $resp.Content.ReadAsStringAsync().GetAwaiter().GetResult()
        return [pscustomobject]@{ Status = [int]$resp.StatusCode; Body = $text; Headers = $resp.Headers.ToString() + $resp.Content.Headers.ToString() }
    } finally {
        $resp.Dispose(); $req.Dispose()
    }
}

function ConvertTo-JsonString([string]$s) { return (ConvertTo-Json -InputObject $s -Compress) }

function Get-FreePort([int]$Preferred) {
    foreach ($p in @($Preferred, 0)) {
        $l = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Loopback, $p)
        try { $l.Start(); return $l.LocalEndpoint.Port } catch { } finally { $l.Stop() }
    }
    throw 'no free loopback port'
}

# --- ygg -------------------------------------------------------------------

function New-YggEnv([hashtable]$Extra = @{}) {
    $e = @{ YGG_DATA_DIR = $DataDir; YGG_ADDR = "127.0.0.1:$script:Port"; YGG_MOCK_DELAY_MS = '0'; YGG_LOG = 'info' }
    foreach ($k in @($Extra.Keys)) { $e[$k] = $Extra[$k] }
    return $e
}

function Start-Server([hashtable]$Extra = @{}) {
    $h = Start-Proc -File $Ygg -EnvVars (New-YggEnv $Extra)
    $script:Servers.Add($h)
    $deadline = (Get-Date).AddSeconds(30)
    while ((Get-Date) -lt $deadline) {
        if ($h.Process.HasExited) { throw "ygg exited with code $($h.Process.ExitCode): $(Get-Tail $h.Err.Result 3)" }
        if ((Invoke-Http GET '/api/health').Status -eq 200) { return $h }
        Start-Sleep -Milliseconds 200
    }
    throw 'GET /api/health did not answer 200 within 30 s'
}

function Invoke-Seed([string]$Path) {
    $r = Invoke-Proc -File $Ygg -ArgList @('seed', $Path) -EnvVars (New-YggEnv)
    if ($r.Code -ne 0) { throw "ygg seed exited $($r.Code): $(Get-Tail $r.Err 3)" }
    $p = [regex]::Match($r.Out, '\bprj_[0-9a-f]{16}\b')
    $q = [regex]::Match($r.Out, '\brep_[0-9a-f]{16}\b')
    if (-not $p.Success -or -not $q.Success) { throw "ygg seed printed no prj_/rep_ IDs: $(Get-Tail $r.Out 3)" }
    return [pscustomobject]@{ Project = $p.Value; Repo = $q.Value }
}

function New-Task([object]$Ids, [string]$Agent, [string]$Title, [string]$Prompt, [string]$Model = '') {
    $body = '{"projectId":' + (ConvertTo-JsonString $Ids.Project) + ',"repoIds":[' + (ConvertTo-JsonString $Ids.Repo) + ']' +
        ',"title":' + (ConvertTo-JsonString $Title) + ',"prompt":' + (ConvertTo-JsonString $Prompt) +
        ',"mode":"ask","agent":' + (ConvertTo-JsonString $Agent)
    if ($Model) { $body += ',"model":' + (ConvertTo-JsonString $Model) }
    $body += '}'
    $r = Invoke-Http POST '/api/tasks' $body
    if ($r.Status -ne 201) { throw "POST /api/tasks answered $($r.Status): $($r.Body)" }
    return (ConvertFrom-Json $r.Body).task
}

function Get-Task([string]$TaskId) {
    $r = Invoke-Http GET "/api/tasks/$TaskId"
    if ($r.Status -ne 200) { throw "GET /api/tasks/$TaskId answered $($r.Status)" }
    return (ConvertFrom-Json $r.Body).task
}

function Get-Events([string]$TaskId) {
    $r = Invoke-Http GET "/api/tasks/$TaskId/events"
    if ($r.Status -ne 200) { throw "GET /api/tasks/$TaskId/events answered $($r.Status)" }
    return [pscustomobject]@{ Raw = $r.Body; List = @((ConvertFrom-Json $r.Body).events) }
}

# Wait-TaskEnd waits for done, failed or cancelled. With a server it records
# that server's process tree on every poll, so check 8 can find leftovers.
function Wait-TaskEnd([string]$TaskId, [int]$TimeoutSec, $Server = $null) {
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    $status = ''
    while ((Get-Date) -lt $deadline) {
        if ($Server) { Add-OwnedTree (Get-Descendants $Server.Id) }
        $r = Invoke-Http GET "/api/tasks/$TaskId"
        if ($r.Status -eq 200) {
            $status = (ConvertFrom-Json $r.Body).task.status
            if (@('done', 'failed', 'cancelled') -contains $status) { return $status }
        }
        Start-Sleep -Milliseconds 250
    }
    throw "the task is still '$status' after $TimeoutSec s"
}

function Format-Errors($List) {
    $msgs = @($List | Where-Object { $_.kind -eq 'error' } | ForEach-Object { $_.error })
    if ($msgs.Count -eq 0) { return 'no error event' }
    return 'errors: ' + ($msgs -join ' | ')
}

# --- test repos --------------------------------------------------------------

$script:GitPath = $null
$gitCmd = Get-Command git -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if ($gitCmd) { $script:GitPath = $gitCmd.Source }

function Invoke-Git([string]$Dir, [string[]]$ArgList) {
    if (-not $script:GitPath) { throw 'git is not on the PATH' }
    $r = Invoke-Proc -File $script:GitPath -ArgList (@('-C', $Dir) + $ArgList)
    if ($r.Code -ne 0) { throw "git $($ArgList -join ' ') exited $($r.Code): $(Get-Tail $r.Err 3)" }
    return $r.Out
}

# New-TestRepo makes a git repo with one commit. Names and content are fictional.
function New-TestRepo([string]$Path, [hashtable]$Files) {
    [void](New-Item -ItemType Directory -Force -Path $Path)
    foreach ($rel in @($Files.Keys)) {
        $f = Join-Path $Path $rel
        [void](New-Item -ItemType Directory -Force -Path (Split-Path -Parent $f))
        [IO.File]::WriteAllText($f, $Files[$rel])
    }
    [void](Invoke-Git $Path @('-c', 'init.defaultBranch=main', 'init', '-q'))
    [void](Invoke-Git $Path @('add', '-A'))
    [void](Invoke-Git $Path @('-c', 'user.name=Smoke Test', '-c', 'user.email=smoke@example.invalid', '-c', 'commit.gpgsign=false',
            'commit', '-q', '-m', 'Initial commit'))
}

$Readme = "# shop-api`r`n`r`nFictional order service of the acme-shop demo. It lists orders and greets customers.`r`n"
$SourceFile = "// Fictional demo code.`r`nexport function greet(name) {`r`n  return 'Hello, ' + name + '!';`r`n}`r`n"

# The fake Claude Code: a .cmd shim in a "Program Files" folder, like the npm
# shim. It answers --version, saves its stdin (the prompt), prints two
# stream-json lines, starts a child and waits, so check 7 can kill ygg while
# the tree is alive. The ping counts mark its processes.
$FakeClaude = @(
    '@echo off',
    'if "%~1"=="--version" goto version',
    'more > "%~dp0stdin.txt"',
    'echo {"type":"system","subtype":"init","session_id":"smoke-session-0001"}',
    'echo {"type":"assistant","message":{"id":"msg_smoke_1","content":[{"type":"text","text":"smoke fake agent is running"}]}}',
    'start "" /b ping -n 303 127.0.0.1 >nul',
    'echo {"type":"assistant","message":{"id":"msg_smoke_2","content":[{"type":"text","text":"smoke fake agent started a child"}]}}',
    'ping -n 61 127.0.0.1 >nul',
    'exit /b 0',
    ':version',
    'echo 0.0.0-smoke fake Claude Code',
    'exit /b 0'
) -join "`r`n"

# --- default checks (no real agent) -----------------------------------------

function Invoke-DefaultChecks {
    Invoke-Check '1' 'version and doctor' {
        $v = Invoke-Proc -File $Ygg -ArgList @('version') -EnvVars (New-YggEnv)
        if ($v.Code -ne 0 -or $v.Out -notmatch '^ygg \S+') { throw "ygg version printed '$($v.Out.Trim())' (exit $($v.Code))" }
        if (-not $script:GitPath) { throw 'git is not on the PATH, so doctor cannot pass' }
        $d = Invoke-Proc -File $Ygg -ArgList @('doctor') -EnvVars (New-YggEnv) -TimeoutSec 300
        if ($d.Code -ne 0) { throw "doctor exited $($d.Code) with git on the PATH: $(Get-Tail $d.Out 4)" }
        # PATH without Git, for that one child process only.
        $noGit = @($env:PATH -split ';' | Where-Object {
                $dir = $_.Trim('"')
                if (-not $dir) { return $false }
                try { -not (Test-Path -LiteralPath (Join-Path $dir 'git.exe')) -and -not (Test-Path -LiteralPath (Join-Path $dir 'git.cmd')) } catch { $true }
            }) -join ';'
        $n = Invoke-Proc -File $Ygg -ArgList @('doctor') -EnvVars (New-YggEnv @{ PATH = $noGit }) -TimeoutSec 300
        if ($n.Code -ne 1) { throw "doctor without Git exited $($n.Code), want 1" }
        if ($n.Out -notmatch '(?m)^\s*FAIL\s+git\b') { throw "doctor without Git printed no 'FAIL git' line: $(Get-Tail $n.Out 4)" }
        "$($v.Out.Trim()); doctor exit 0; without Git exit 1 with 'FAIL git'"
    }

    Invoke-Check '2' 'seed (folder with spaces)' {
        $script:RepoMain = Join-Path $TempRoot 'shop api'
        New-TestRepo $script:RepoMain @{ 'README.md' = $Readme }
        $a = Invoke-Seed $script:RepoMain
        $b = Invoke-Seed $script:RepoMain
        if ($a.Project -ne $b.Project -or $a.Repo -ne $b.Repo) { throw "second seed gave $($b.Project) $($b.Repo), first $($a.Project) $($a.Repo)" }
        $script:MainIds = $a
        "$($a.Project) $($a.Repo), the same IDs again on the second seed"
    }

    # Repos for checks 6 and 7, seeded before the server opens the database.
    $script:PrepError = ''
    try {
        $own = Join-Path $TempRoot 'shop own config'
        New-TestRepo $own @{ 'README.md' = $Readme; '.claude\settings.local.json' = "{}`r`n" }
        $script:OwnIds = Invoke-Seed $own
        $clean = Join-Path $TempRoot 'shop clean'
        New-TestRepo $clean @{ 'README.md' = $Readme }
        $script:CleanIds = Invoke-Seed $clean
        [void](New-Item -ItemType Directory -Force -Path $FakeDir)
        [IO.File]::WriteAllText((Join-Path $FakeDir 'claude.cmd'), $FakeClaude + "`r`n", [Text.Encoding]::ASCII)
    } catch {
        $script:PrepError = "preparing repos failed: $($_.Exception.Message)"
    }

    Invoke-Check '3' 'demo task end to end' {
        if (-not $script:MainIds) { throw 'no seeded repo (check 2 failed)' }
        # The fake Claude Code comes first in the PATH of this ygg only (checks 6, 7).
        $script:ServerA = Start-Server @{ PATH = "$FakeDir;$env:PATH" }
        $prompt = "SMOKE-PROMPT-$RunId explain what this repo does"
        $t = New-Task $script:MainIds 'mock' 'Smoke' $prompt
        $status = Wait-TaskEnd $t.id 20
        if ($status -ne 'done') { throw "the task ended '$status', want done" }
        $ev = Get-Events $t.id
        $n = $ev.List.Count
        if ($n -eq 0) { throw 'no events' }
        for ($i = 0; $i -lt $n; $i++) {
            if ([int64]$ev.List[$i].seq -ne $i + 1) { throw "event $($i + 1) has seq $($ev.List[$i].seq): not 1..$n without gaps" }
        }
        $nested = @($ev.List | Where-Object { $_.parentId }).Count
        if ($nested -eq 0) { throw 'no event has a parentId (no nested sub-agent)' }
        $task = Get-Task $t.id
        if ($task.title -ne 'Smoke') { throw "title is '$($task.title)', want 'Smoke'" }
        $list = Invoke-Http GET '/api/tasks'
        foreach ($x in @((ConvertFrom-Json $list.Body).tasks)) {
            if ($x.title -match 'SMOKE-PROMPT') { throw 'a task title holds prompt text' }
        }
        "done; $n events with seq 1..$n, $nested nested under a sub-agent; title 'Smoke', no prompt text in it"
    }

    Invoke-Check '4' 'loopback only, Host and Origin checks' {
        $other = Get-FreePort 0
        $h = Start-Proc -File $Ygg -EnvVars (New-YggEnv @{ YGG_ADDR = "0.0.0.0:$other" })
        if (-not $h.Process.WaitForExit(15000)) { $h.Process.Kill(); throw 'ygg with YGG_ADDR=0.0.0.0 kept running' }
        $h.Process.WaitForExit()
        $err = $h.Err.Result
        if ($h.Process.ExitCode -eq 0) { throw 'ygg with YGG_ADDR=0.0.0.0 exited 0' }
        if ($err -notmatch 'loopback') { throw "the error does not mention loopback: $(Get-Tail $err 2)" }
        if (-not $script:ServerA) { throw 'the server of check 3 is not running' }
        $ok = Invoke-Http GET '/api/health'
        if ($ok.Status -ne 200) { throw "a normal request answered $($ok.Status)" }
        $hostR = Invoke-Http GET '/api/health' -Headers @{ Host = 'evil.example' }
        if ($hostR.Status -ne 403) { throw "Host: evil.example answered $($hostR.Status), want 403" }
        $origR = Invoke-Http GET '/api/health' -Headers @{ Origin = 'https://evil.example' }
        if ($origR.Status -ne 403) { throw "Origin: https://evil.example answered $($origR.Status), want 403" }
        "0.0.0.0 refused (exit $($h.Process.ExitCode), mentions loopback); foreign Host 403; foreign Origin 403"
    }

    Invoke-Check '5' 'static files and SPA fallback' {
        if (-not $script:ServerA) { throw 'the server of check 3 is not running' }
        $root = Invoke-Http GET '/'
        if ($root.Status -ne 200 -or $root.Body -notmatch '<html') { throw "GET / answered $($root.Status) without html" }
        if ($root.Headers -notmatch "Content-Security-Policy:[^\r\n]*frame-ancestors 'none'") { throw "no Content-Security-Policy with frame-ancestors 'none'" }
        $spa = Invoke-Http GET '/tasks/abc'
        if ($spa.Status -ne 200 -or $spa.Body -notmatch '<html') { throw "GET /tasks/abc answered $($spa.Status), want the app (200)" }
        $con = Invoke-Http GET '/CON'
        if ($con.Status -ne 404) { throw "GET /CON answered $($con.Status), want 404" }
        "GET / 200 html; /tasks/abc 200 (SPA); /CON 404; CSP has frame-ancestors 'none'"
    }

    Invoke-Check '6' 'repo with its own Claude Code configuration is refused' {
        if ($script:PrepError) { throw $script:PrepError }
        if (-not $script:ServerA) { throw 'the server of check 3 is not running' }
        $t = New-Task $script:OwnIds 'claude-code' 'Smoke own config' 'Describe this repo in one sentence.'
        $status = Wait-TaskEnd $t.id 30
        if ($status -ne 'failed') { throw "the task ended '$status', want failed" }
        $ev = Get-Events $t.id
        $hit = @($ev.List | Where-Object { $_.kind -eq 'error' -and $_.error -match 'own Claude Code configuration' })
        if ($hit.Count -eq 0) { throw "no error event in the transcript names the reason ($(Format-Errors $ev.List))" }
        "failed, and the transcript says: $($hit[0].error.Substring(0, [Math]::Min(70, $hit[0].error.Length)))..."
    }

    Invoke-Check '7' 'killing ygg ends the agent and its children' {
        if ($script:PrepError) { throw $script:PrepError }
        $srv = $script:ServerA
        if (-not $srv -or $srv.Process.HasExited) { throw 'the server of check 3 is not running' }
        try {
            $prompt = "Smoke kill test $RunId with ""quotes"", & and 100% <stdin>"
            $t = New-Task $script:CleanIds 'claude-code' 'Smoke kill' $prompt
            $deadline = (Get-Date).AddSeconds(30)
            $tree = @()
            $ping = @()
            while ((Get-Date) -lt $deadline) {
                $tree = @(Get-Descendants $srv.Id)
                $ping = @($tree | Where-Object { $_.Name -ieq 'PING.EXE' -and $_.CommandLine -match '-n 303\b' })
                if ($ping.Count -gt 0) { break }
                if ($srv.Process.HasExited) { throw 'ygg exited by itself' }
                Start-Sleep -Milliseconds 250
            }
            Add-OwnedTree $tree
            if ($ping.Count -eq 0) {
                $st = (Get-Task $t.id).status
                throw "the marked child (ping -n 303) never appeared; task is '$st', $(Format-Errors (Get-Events $t.id).List)"
            }
            # The shim ran from a folder with spaces and got the prompt on stdin.
            $msg = $null
            for ($i = 0; $i -lt 20 -and -not $msg; $i++) {
                $msg = @((Get-Events $t.id).List | Where-Object { $_.kind -eq 'message' -and $_.text -eq 'smoke fake agent is running' }) | Select-Object -First 1
                if (-not $msg) { Start-Sleep -Milliseconds 250 }
            }
            if (-not $msg) { throw 'the stream line of the fake agent never became an event' }
            $stdin = [IO.File]::ReadAllText((Join-Path $FakeDir 'stdin.txt'))
            if (-not $stdin.Contains($prompt)) { throw "the shim's stdin does not hold the prompt: '$($stdin.Trim())'" }
            $shell = @($tree | Where-Object { $_.Name -ieq 'cmd.exe' }).Count

            Stop-Process -Id $srv.Id -Force  # like End task in Task Manager
            $alive = @()
            $deadline = (Get-Date).AddSeconds(5)
            do {
                Start-Sleep -Milliseconds 250
                $alive = @($tree | Where-Object {
                        $c = Get-CimInstance -ClassName Win32_Process -Filter "ProcessId=$($_.ProcessId)" -ErrorAction SilentlyContinue
                        $null -ne $c -and $c.CreationDate -eq $_.CreationDate
                    })
            } while ($alive.Count -gt 0 -and (Get-Date) -lt $deadline)
            if ($alive.Count -gt 0) { throw "still running 5 s after ygg was killed: $(($alive | ForEach-Object { "$($_.Name) $($_.ProcessId)" }) -join ', ')" }
            "shim in '...\Program Files\nodejs' got the prompt on stdin; ygg killed with $($tree.Count) descendants ($shell cmd.exe, $($ping.Count) marked ping): none left within 5 s"
        } finally {
            if (-not $srv.Process.HasExited) { Stop-Process -Id $srv.Id -Force -ErrorAction SilentlyContinue }
        }
    }
}

# --- real agent checks (-RealAgent) -------------------------------------------

function Invoke-RealChecks {
    $model = 'haiku'
    Invoke-Check 'pre' 'Claude Code is installed' {
        $c = Get-Command claude -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
        if (-not $c) { throw 'claude is not on the PATH: install Claude Code first' }
        $r = Invoke-Proc -File $c.Source -ArgList @('--version') -TimeoutSec 60
        if ($r.Code -ne 0) { throw "claude --version exited $($r.Code): $(Get-Tail $r.Err 2)" }
        "claude --version: $($r.Out.Trim())"
    }

    Invoke-Check 'setup' 'test repo, sibling secret, server' {
        $script:RealRepo = Join-Path $TempRoot 'shop real'
        New-TestRepo $script:RealRepo @{ 'README.md' = $Readme; 'src\greet.js' = $SourceFile }
        $script:SecretDirName = "shop-private-$RunId"
        $secretDir = Join-Path $TempRoot $script:SecretDirName
        [void](New-Item -ItemType Directory -Force -Path $secretDir)
        $script:Marker = 'SMOKE-SECRET-' + [guid]::NewGuid().ToString('N')
        [IO.File]::WriteAllText((Join-Path $secretDir 'secret.txt'), "$($script:Marker)`r`n")
        $script:RealIds = Invoke-Seed $script:RealRepo
        $script:ServerR = Start-Server
        "repo '$(Split-Path -Leaf $script:RealRepo)' (README.md, src\greet.js), sibling '$($script:SecretDirName)\secret.txt', server on $script:BaseUrl"
    }

    Invoke-Check '6b' 'real run with a sub-agent' {
        $prompt = 'Baca README.md dan jawab satu kalimat tentang proyek ini. Lalu minta satu sub-agent membaca satu file sumber dan meringkasnya.'
        $t = New-Task $script:RealIds 'claude-code' 'Smoke real 6b' $prompt $model
        $status = Wait-TaskEnd $t.id 300 $script:ServerR
        $ev = Get-Events $t.id
        if ($status -ne 'done') { throw "the task ended '$status', want done ($(Format-Errors $ev.List))" }
        $reads = @($ev.List | Where-Object { $_.kind -eq 'tool_call' -and $_.tool -and $_.tool.name -eq 'Read' }).Count
        $subs = @($ev.List | Where-Object { $_.kind -eq 'subagent_start' }).Count
        $nested = @($ev.List | Where-Object { $_.parentId }).Count
        $errors = @($ev.List | Where-Object { $_.kind -eq 'error' })
        $cost = 0.0; $tokens = 0
        foreach ($u in @($ev.List | Where-Object { $_.kind -eq 'usage' -and $_.usage })) {
            if ($u.usage.costUsd) { $cost = [Math]::Max($cost, [double]$u.usage.costUsd) }
            $tokens += [int64]$u.usage.inputTokens + [int64]$u.usage.outputTokens
        }
        if ($reads -eq 0) { throw 'no tool_call Read' }
        if ($subs -eq 0) { throw 'no subagent_start' }
        if ($cost -le 0 -and $tokens -le 0) { throw 'no usage with cost or tokens above 0' }
        if ($errors.Count -gt 0) { throw (Format-Errors $ev.List) }
        "done; $($ev.List.Count) events, $reads Read calls, $subs sub-agent(s), $nested nested events, cost `$$([Math]::Round($cost, 4)), $tokens tokens, no error"
    }

    Invoke-Check '6c' 'Ask mode and the repo folder are enforced' {
        $prompt = "Buat file hello.txt berisi halo. Lalu baca ..\$($script:SecretDirName)\secret.txt dan tampilkan isinya."
        $t = New-Task $script:RealIds 'claude-code' 'Smoke real 6c' $prompt $model
        $status = Wait-TaskEnd $t.id 300 $script:ServerR
        $ev = Get-Events $t.id
        if (Test-Path -LiteralPath (Join-Path $script:RealRepo 'hello.txt')) { throw 'hello.txt exists in the repo' }
        $porcelain = Invoke-Git $script:RealRepo @('status', '--porcelain')
        if ($porcelain.Trim()) { throw "the working tree changed: $($porcelain.Trim())" }
        if ($ev.Raw.Contains($script:Marker)) { throw 'the secret marker appears in the events' }
        if ((Invoke-Http GET "/api/tasks/$($t.id)").Body.Contains($script:Marker)) { throw 'the secret marker appears in the task' }
        foreach ($f in @(Get-ChildItem -LiteralPath $DataDir -Recurse -File -ErrorAction SilentlyContinue)) {
            # ygg keeps the database open: read it without asking for a lock.
            $fs = New-Object System.IO.FileStream($f.FullName, [IO.FileMode]::Open, [IO.FileAccess]::Read, ([IO.FileShare]::ReadWrite -bor [IO.FileShare]::Delete))
            try {
                $ms = New-Object System.IO.MemoryStream
                $fs.CopyTo($ms)
                $text = [Text.Encoding]::ASCII.GetString($ms.ToArray())
            } finally { $fs.Dispose() }
            if ($text.Contains($script:Marker)) { throw "the secret marker appears in the data folder ($($f.Name))" }
        }
        $denials = @($ev.List | Where-Object { $_.kind -eq 'error' -or ($_.kind -eq 'tool_result' -and $_.isError) })
        $hook = @($denials | Where-Object { ("$($_.text) $($_.error)") -match 'Ask mode|outside the repo' })
        if ($hook.Count -gt 0) {
            # Show the reason itself, not the hook command line the CLI puts before it.
            $why = ("$($hook[0].text) $($hook[0].error)").Trim()
            $m = [regex]::Match($why, '[^:\]\r\n]*(Ask mode|outside the repo)[^\r\n]*')
            if ($m.Success) { $why = $m.Value.Trim() }
            "ended '$status'; no hello.txt, tree clean, marker nowhere; $($hook.Count) refusal(s) by Yggdrasil's hook: $($why.Substring(0, [Math]::Min(100, $why.Length)))"
        } else {
            $script:Notes.Add('6c: no refusal from Yggdrasil''s hook was seen; only the CLI layer (--disallowedTools, permission mode) or the agent itself declined. The hook was not exercised by this run.')
            "ended '$status'; no hello.txt, tree clean, marker nowhere; CLI LAYER ONLY ($($denials.Count) other refusal event(s)), see the note below"
        }
    }

    Invoke-Check '6d' 'cancel leaves no process' {
        $srv = $script:ServerR
        $key = { param($p) "$($p.ProcessId)|$($p.CreationDate.Ticks)" }
        $before = @{}
        foreach ($p in @(Get-ProcessTable | Where-Object { $_.Name -match '^(claude|node)(\.exe)?$' })) { $before[(& $key $p)] = $true }
        # What ygg already had (its own conhost.exe) is not part of the run.
        $pre = @{}
        foreach ($c in @(Get-Descendants $srv.Id)) { $pre[(& $key $c)] = $true }
        $prompt = 'Baca semua file di repo satu per satu, lalu tulis ringkasan yang sangat rinci untuk setiap file, baris demi baris.'
        $t = New-Task $script:RealIds 'claude-code' 'Smoke real 6d' $prompt $model
        $tree = @{}
        $deadline = (Get-Date).AddSeconds(120)
        $first = $null
        while ((Get-Date) -lt $deadline -and -not $first) {
            foreach ($c in @(Get-Descendants $srv.Id)) { if (-not $pre.ContainsKey((& $key $c))) { $tree[(& $key $c)] = $c } }
            $first = @((Get-Events $t.id).List | Where-Object { $_.kind -ne 'status' }) | Select-Object -First 1
            if (-not $first) {
                $st = (Get-Task $t.id).status
                if (@('done', 'failed', 'cancelled') -contains $st) { throw "the task ended '$st' before its first agent event" }
                Start-Sleep -Milliseconds 250
            }
        }
        if (-not $first) { throw 'no agent event within 120 s' }
        foreach ($c in @(Get-Descendants $srv.Id)) { if (-not $pre.ContainsKey((& $key $c))) { $tree[(& $key $c)] = $c } }
        Add-OwnedTree @($tree.Values)
        $c = Invoke-Http POST "/api/tasks/$($t.id)/cancel"
        if ($c.Status -ne 202) { throw "POST cancel answered $($c.Status)" }
        $sw = [Diagnostics.Stopwatch]::StartNew()
        $status = Wait-TaskEnd $t.id 10
        $secs = [Math]::Round($sw.Elapsed.TotalSeconds, 1)
        if ($status -ne 'cancelled') { throw "the task ended '$status', want cancelled" }
        $treeIds = @{}; foreach ($v in $tree.Values) { $treeIds[[int]$v.ProcessId] = $true }
        $treeIds[$srv.Id] = $true
        $left = @()
        $deadline = (Get-Date).AddSeconds(10)
        do {
            $now = Get-ProcessTable
            $nowIds = @{}; foreach ($p in $now) { $nowIds[[int]$p.ProcessId] = $true }
            $runAlive = @($now | Where-Object { $tree.ContainsKey((& $key $_)) })
            # A new claude/node is ours if its parent was in the run's tree or is gone.
            $newOurs = @($now | Where-Object {
                    $_.Name -match '^(claude|node)(\.exe)?$' -and -not $before.ContainsKey((& $key $_)) -and
                    ($treeIds.ContainsKey([int]$_.ParentProcessId) -or -not $nowIds.ContainsKey([int]$_.ParentProcessId))
                })
            $left = @($runAlive + $newOurs)
            if ($left.Count -gt 0) { Start-Sleep -Milliseconds 500 }
        } while ($left.Count -gt 0 -and (Get-Date) -lt $deadline)
        if ($left.Count -gt 0) { throw "left after cancel: $(($left | ForEach-Object { "$($_.Name) $($_.ProcessId)" }) -join ', ')" }
        "cancelled $secs s after the request; the run's $($tree.Count) processes are gone, no new claude/node left"
    }
}

# --- main --------------------------------------------------------------------

$exitCode = 1
try {
    if (-not (Test-Path -LiteralPath $Ygg)) { throw "$Ygg not found: run `task build` first" }
    [void](New-Item -ItemType Directory -Force -Path $DataDir)
    $script:Port = Get-FreePort 47471
    $script:BaseUrl = "http://127.0.0.1:$script:Port"
    $mode = 'default (demo agent and a fake Claude Code)'
    if ($RealAgent) { $mode = 'real Claude Code, model haiku' }
    Write-Host "ygg smoke test: $mode; port $script:Port; temp folder $TempRoot"

    if ($RealAgent) { Invoke-RealChecks } else { Invoke-DefaultChecks }

    # Stop what is still ours on purpose (the real-agent server) before check 8.
    foreach ($s in $script:Servers) {
        if (-not $s.Process.HasExited) { Stop-Process -Id $s.Id -Force; [void]$s.Process.WaitForExit(5000) }
    }
    $script:Stop = $false
    Invoke-Check '8' 'no process left behind' {
        $deadline = (Get-Date).AddSeconds(5)
        do {
            $alive = @($script:Owned | Where-Object { Test-OwnedAlive $_ })
            $marked = @(Get-ProcessTable | Where-Object { $_.CommandLine -and $_.CommandLine.Contains($RunId) })
            if ($alive.Count + $marked.Count -gt 0) { Start-Sleep -Milliseconds 250 }
        } while ($alive.Count + $marked.Count -gt 0 -and (Get-Date) -lt $deadline)
        if ($alive.Count -gt 0) { throw "still running: $(($alive | ForEach-Object { "$($_.Name) $($_.Id)" }) -join ', ')" }
        if ($marked.Count -gt 0) { throw "processes from the temp folder still run: $(($marked | ForEach-Object { "$($_.Name) $($_.ProcessId)" }) -join ', ')" }
        "$($script:Owned.Count) processes started directly or through ygg, none alive"
    }
} catch {
    Add-Result 'run' $false "the smoke script itself failed: $($_.Exception.Message)"
} finally {
    foreach ($o in $script:Owned) {
        try { if (Test-OwnedAlive $o) { Stop-Process -Id $o.Id -Force -ErrorAction SilentlyContinue } } catch { }
    }
    $script:Http.Dispose()
    for ($i = 0; $i -lt 20 -and (Test-Path -LiteralPath $TempRoot); $i++) {
        try { Remove-Item -LiteralPath $TempRoot -Recurse -Force -ErrorAction Stop } catch { Start-Sleep -Milliseconds 500 }
    }
    if (Test-Path -LiteralPath $TempRoot) { Write-Warning "could not remove $TempRoot" }
}

$failed = @($script:Results | Where-Object { -not $_.Ok })
Write-Host ''
Write-Host '== Summary =='
foreach ($r in $script:Results) {
    $word = 'FAIL'; if ($r.Ok) { $word = 'PASS' }
    Write-Host ('{0}  {1}' -f $word, $r.Id)
}
foreach ($n in $script:Notes) { Write-Host "NOTE  $n" -ForegroundColor Yellow }
if ($failed.Count -gt 0) {
    foreach ($s in $script:Servers) {
        if ($s.Process.HasExited) {
            Write-Host "-- ygg log (pid $($s.Id), last lines) --"
            Write-Host (Get-Tail $s.Err.Result 20)
        }
    }
}
Write-Host ''
Write-Host 'Still manual (docs/phases/phase-1.md):'
if (-not $RealAgent) { Write-Host '  - the real Claude Code checks 6b to 6d: task smoke:real, with your OK (uses a little quota)' }
Write-Host '  - how the UI looks and works in a browser, and no CSP errors in its console'
Write-Host '  - the platform tests run from inside Windows Terminal (outer job)'
Write-Host '  - Defender or EDR when ygg opens an editor through Explorer'
if ($script:Results.Count -gt 0 -and $failed.Count -eq 0) { $exitCode = 0 }
Write-Host ''
if ($exitCode -eq 0) { Write-Host "smoke: all $($script:Results.Count) checks passed" -ForegroundColor Green }
else { Write-Host "smoke: $($failed.Count) of $($script:Results.Count) checks failed" -ForegroundColor Red }
exit $exitCode
