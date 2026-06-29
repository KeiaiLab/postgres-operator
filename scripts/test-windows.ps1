param(
    [ValidateSet("controller", "sharding", "unit", "all")]
    [string]$Preset = "controller",

    [string[]]$Package,
    [string]$Run,
    [string]$GinkgoFocus,
    [switch]$Fresh,
    [switch]$Race,
    [string]$Timeout,
    [switch]$DryRun,
    [switch]$SelfTest
)

Set-StrictMode -Version 2.0
$ErrorActionPreference = "Stop"

function Resolve-Go {
    $cmd = Get-Command go -ErrorAction SilentlyContinue
    if ($cmd -and $cmd.Source) {
        return $cmd.Source
    }

    $candidate = Join-Path $env:LOCALAPPDATA "Programs\go1.26.4\go\bin\go.exe"
    if (Test-Path -LiteralPath $candidate) {
        return $candidate
    }

    throw "go executable not found. Add Go to PATH or install it at $candidate"
}

function Ensure-Directory {
    param([string]$Path)
    New-Item -ItemType Directory -Force -Path $Path | Out-Null
    return (Resolve-Path -LiteralPath $Path).Path
}

function Test-UnderPath {
    param(
        [string]$Child,
        [string]$Parent
    )
    $childFull = [System.IO.Path]::GetFullPath($Child).TrimEnd('\')
    $parentFull = [System.IO.Path]::GetFullPath($Parent).TrimEnd('\')
    return $childFull.Equals($parentFull, [System.StringComparison]::OrdinalIgnoreCase) -or
        $childFull.StartsWith($parentFull + '\', [System.StringComparison]::OrdinalIgnoreCase)
}

function Preset-Packages {
    param([string]$Name)
    switch ($Name) {
        "controller" { return @("./internal/controller") }
        "sharding" { return @("./cmd/instance", "./internal/router", "./cmd/pg-router", "./cmd/reshard-copy-poc", "./api/v1alpha1", "./internal/controller") }
        "unit" { return @("./api/...", "./internal/version/...", "./internal/plugin/...", "./internal/instance/fencing/...", "./internal/instance/supervise/...") }
        "all" { return @("./...") }
        default { throw "unknown preset: $Name" }
    }
}

function Find-EnvtestAssets {
    param([string]$RepoRoot)
    if ($env:KUBEBUILDER_ASSETS) {
        return $env:KUBEBUILDER_ASSETS
    }

    $k8sRoot = Join-Path $RepoRoot "bin\k8s"
    if (-not (Test-Path -LiteralPath $k8sRoot)) {
        return $null
    }

    $apiServer = Get-ChildItem -Path $k8sRoot -Recurse -Filter "kube-apiserver.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $apiServer) {
        $apiServer = Get-ChildItem -Path $k8sRoot -Recurse -Filter "kube-apiserver" -ErrorAction SilentlyContinue | Select-Object -First 1
    }
    if (-not $apiServer) {
        return $null
    }

    return $apiServer.DirectoryName
}

function Build-GoTestCommand {
    param(
        [string]$GoExe,
        [string[]]$Packages,
        [string]$Run,
        [string]$GinkgoFocus,
        [bool]$Fresh,
        [bool]$Race,
        [string]$Timeout
    )

    $args = @("test")
    if ($Fresh) {
        $args += "-count=1"
    }
    if ($Race) {
        $args += "-race"
    }
    if ($Timeout) {
        $args += "-timeout=$Timeout"
    }
    if ($Run) {
        $args += "-run"
        $args += $Run
    }
    $args += $Packages
    if ($GinkgoFocus) {
        $args += "--ginkgo.focus=$GinkgoFocus"
    }

    return @{
        Exe  = $GoExe
        Args = $args
    }
}

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = (Resolve-Path -LiteralPath (Join-Path $scriptDir "..")).Path
$goExe = Resolve-Go
$baseDir = Join-Path $env:LOCALAPPDATA "keiailab\postgres-operator"
$goTmpDir = Ensure-Directory (Join-Path $baseDir "go-tmp")
$goCacheDir = Ensure-Directory (Join-Path $baseDir "go-cache")

if (Test-UnderPath -Child $goTmpDir -Parent $repoRoot) {
    throw "GOTMPDIR must be outside the repository: $goTmpDir"
}
if (Test-UnderPath -Child $goCacheDir -Parent $repoRoot) {
    throw "GOCACHE must be outside the repository: $goCacheDir"
}

$env:GOTMPDIR = $goTmpDir
$env:GOCACHE = $goCacheDir
$envtestAssets = Find-EnvtestAssets -RepoRoot $repoRoot
if ($envtestAssets) {
    $env:KUBEBUILDER_ASSETS = $envtestAssets
}

$packages = if ($Package -and $Package.Count -gt 0) { @($Package) } else { @(Preset-Packages -Name $Preset) }
$command = Build-GoTestCommand -GoExe $goExe -Packages $packages -Run $Run -GinkgoFocus $GinkgoFocus -Fresh:$Fresh.IsPresent -Race:$Race.IsPresent -Timeout $Timeout

if ($SelfTest) {
    foreach ($name in @("controller", "sharding", "unit", "all")) {
        $resolved = @(Preset-Packages -Name $name)
        if (-not $resolved -or $resolved.Count -eq 0) {
            throw "preset $name resolved to no packages"
        }
    }
    if (Test-UnderPath -Child $env:GOTMPDIR -Parent $repoRoot) {
        throw "self-test failed: GOTMPDIR is inside repo"
    }
    if (Test-UnderPath -Child $env:GOCACHE -Parent $repoRoot) {
        throw "self-test failed: GOCACHE is inside repo"
    }
    Write-Host "self-test ok"
    Write-Host "repo=$repoRoot"
    Write-Host "go=$goExe"
    Write-Host "GOTMPDIR=$env:GOTMPDIR"
    Write-Host "GOCACHE=$env:GOCACHE"
    if ($env:KUBEBUILDER_ASSETS) {
        Write-Host "KUBEBUILDER_ASSETS=$env:KUBEBUILDER_ASSETS"
    }
    exit 0
}

Write-Host "repo=$repoRoot"
Write-Host "GOTMPDIR=$env:GOTMPDIR"
Write-Host "GOCACHE=$env:GOCACHE"
if ($env:KUBEBUILDER_ASSETS) {
    Write-Host "KUBEBUILDER_ASSETS=$env:KUBEBUILDER_ASSETS"
}
Write-Host ("command={0} {1}" -f $command.Exe, ($command.Args -join " "))

if ($DryRun) {
    exit 0
}

Push-Location $repoRoot
try {
    & $command.Exe @($command.Args)
    exit $LASTEXITCODE
}
finally {
    Pop-Location
}
