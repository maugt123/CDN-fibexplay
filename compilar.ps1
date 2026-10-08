# Compila fibex-cdn para Debian (amd64 y arm64) y Windows usando Docker,
# sin instalar Go. Resultado en .\dist
#   .\compilar.ps1            -> versión "dev"
#   .\compilar.ps1 1.0.0      -> versión 1.0.0
param([string]$Version = "dev")
$ErrorActionPreference = "Stop"
$raiz = $PSScriptRoot
docker run --rm -v "${raiz}:/src" -v fibexcdn-gomod:/go/pkg/mod -w /src -e VERSION=$Version golang:1.26-bookworm sh ./compilar.sh
if ($LASTEXITCODE -ne 0) { throw "la compilación falló" }
Get-ChildItem "$raiz\dist" | Select-Object Name, @{n="MB";e={[math]::Round($_.Length/1MB,1)}}
