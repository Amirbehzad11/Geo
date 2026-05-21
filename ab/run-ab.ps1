param(
    [ValidateSet("health", "route")]
    [string]$Target = "health",

    [int]$Requests = 1000,
    [int]$Concurrency = 100,

    [switch]$KeepAlive
)

$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$baseUrl = "http://host.docker.internal:8080"
$abArgs = @("-n", $Requests, "-c", $Concurrency)
if ($KeepAlive) {
    $abArgs += "-k"
}

if ($Target -eq "health") {
    docker run --rm httpd:2.4-alpine ab @abArgs "$baseUrl/health"
    exit $LASTEXITCODE
}

$payload = Join-Path $PSScriptRoot "route-tehran-mashhad.json"
$body = Get-Content -Raw $payload
Invoke-RestMethod -Method Post -Uri "http://localhost:8080/route" -ContentType "application/json" -Body $body -TimeoutSec 90 | Out-Null

docker run --rm `
    -v "${repoRoot}:/work" `
    -w /work `
    httpd:2.4-alpine `
    ab @abArgs -p "ab/route-tehran-mashhad.json" -T "application/json" "$baseUrl/route"
